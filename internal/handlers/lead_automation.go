package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/internal/templateutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// noReplyBatchSize caps how many leads one org processes per tick.
const noReplyBatchSize = 100

// GetNoReplyRule returns the organization's no-reply rule.
func (a *App) GetNoReplyRule(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionRead)
	if err != nil {
		return nil
	}
	var org models.Organization
	if err := a.DB.Select("id", "settings").Where("id = ?", orgID).First(&org).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Organization not found", nil, "")
	}
	if id := string(r.RequestCtx.QueryArgs().Peek("pipeline_id")); id != "" {
		// A pipeline without its own rule reports disabled; the org rule applies
		return r.SendEnvelope(models.NoReplyPipelineRulesFromSettings(org.Settings)[id])
	}
	return r.SendEnvelope(models.NoReplyRuleFromSettings(org.Settings))
}

// UpdateNoReplyRule validates and saves the organization's no-reply rule.
func (a *App) UpdateNoReplyRule(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}

	var rule models.NoReplyRule
	if err := a.decodeRequest(r, &rule); err != nil {
		return nil
	}
	rule.TaskTitle = strings.TrimSpace(rule.TaskTitle)
	if len(rule.TaskTitle) > 255 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "task_title must be at most 255 characters", nil, "")
	}
	if rule.Enabled {
		if rule.Hours < models.NoReplyMinHours || rule.Hours > models.NoReplyMaxHours {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
				fmt.Sprintf("hours must be between %d and %d", models.NoReplyMinHours, models.NoReplyMaxHours), nil, "")
		}
		if !rule.CreateTask && rule.TemplateID == nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "choose at least one action: create_task or template_id", nil, "")
		}
	}
	if rule.TemplateID != nil {
		if msg := a.validateNoReplyTemplate(orgID, *rule.TemplateID, rule.TemplateParams); msg != "" {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
		}
	}
	if len(rule.Steps) > models.NoReplyMaxSteps {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			fmt.Sprintf("a rule can have at most %d extra steps", models.NoReplyMaxSteps), nil, "")
	}
	for i := range rule.Steps {
		step := &rule.Steps[i]
		step.TaskTitle = strings.TrimSpace(step.TaskTitle)
		if len(step.TaskTitle) > 255 {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "task_title must be at most 255 characters", nil, "")
		}
		if step.Hours < models.NoReplyMinHours || step.Hours > models.NoReplyMaxHours {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
				fmt.Sprintf("hours must be between %d and %d", models.NoReplyMinHours, models.NoReplyMaxHours), nil, "")
		}
		if !step.CreateTask && step.TemplateID == nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "choose at least one action: create_task or template_id", nil, "")
		}
		if step.TemplateID != nil {
			if msg := a.validateNoReplyTemplate(orgID, *step.TemplateID, step.TemplateParams); msg != "" {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
			}
		}
	}
	rule.UpdatedByID = userID

	if id := string(r.RequestCtx.QueryArgs().Peek("pipeline_id")); id != "" {
		pipelineID, err := uuid.Parse(id)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid pipeline_id", nil, "")
		}
		if _, err := a.loadPipeline(orgID, pipelineID); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Pipeline not found", nil, "")
		}
		var org models.Organization
		if err := a.DB.Select("id", "settings").Where("id = ?", orgID).First(&org).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Organization not found", nil, "")
		}
		rules := models.NoReplyPipelineRulesFromSettings(org.Settings)
		rules[id] = rule
		if err := a.setOrgSetting(orgID, models.NoReplyPipelineRulesKey, rules); err != nil {
			a.Log.Error("Failed to save no-reply rule", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save rule", nil, "")
		}
		return r.SendEnvelope(rule)
	}

	if err := a.setOrgSetting(orgID, models.NoReplyRuleSettingsKey, rule); err != nil {
		a.Log.Error("Failed to save no-reply rule", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save rule", nil, "")
	}
	return r.SendEnvelope(rule)
}

// validateNoReplyTemplate checks the template can be sent without a person:
// approved, no media header, and every body/header variable has a value.
func (a *App) validateNoReplyTemplate(orgID, templateID uuid.UUID, params map[string]string) string {
	var tpl models.Template
	if err := a.DB.Where("id = ? AND organization_id = ?", templateID, orgID).First(&tpl).Error; err != nil {
		return "template not found"
	}
	if tpl.Status != "APPROVED" {
		return fmt.Sprintf("template is not approved (status: %s)", tpl.Status)
	}
	switch tpl.HeaderType {
	case "IMAGE", "VIDEO", "DOCUMENT":
		return "templates with a media header cannot be sent automatically"
	}
	names := templateutil.ExtParamNames(tpl.BodyContent)
	if tpl.HeaderType == "TEXT" {
		names = append(names, templateutil.ExtParamNames(tpl.HeaderContent)...)
	}
	var missing []string
	for _, n := range names {
		if strings.TrimSpace(params[n]) == "" {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return "missing template_params: " + strings.Join(missing, ", ")
	}
	return ""
}

// ProcessNoReplyLeads runs every enabled org's no-reply rules. Leads are
// claimed with UPDATE ... SKIP LOCKED so several instances never act twice.
func (a *App) ProcessNoReplyLeads(now time.Time) {
	var orgs []models.Organization
	if err := a.DB.Select("id", "settings").
		Where("settings->'" + models.NoReplyRuleSettingsKey + "'->>'enabled' = 'true' OR settings->'" + models.NoReplyPipelineRulesKey + "' IS NOT NULL").
		Find(&orgs).Error; err != nil {
		a.Log.Error("Failed to load no-reply rules", "error", err)
		return
	}
	for _, org := range orgs {
		pipelineRules := models.NoReplyPipelineRulesFromSettings(org.Settings)
		overridden := make([]uuid.UUID, 0, len(pipelineRules))
		for id, rule := range pipelineRules {
			pipelineID, err := uuid.Parse(id)
			if err != nil {
				continue
			}
			overridden = append(overridden, pipelineID)
			if rule.Enabled && rule.Hours >= models.NoReplyMinHours {
				a.processNoReplyForOrg(org.ID, rule, &pipelineID, nil, now)
			}
		}
		rule := models.NoReplyRuleFromSettings(org.Settings)
		if rule.Enabled && rule.Hours >= models.NoReplyMinHours {
			a.processNoReplyForOrg(org.ID, rule, nil, overridden, now)
		}
	}
}

// processNoReplyForOrg runs a rule's steps. With pipelineID set it covers that
// pipeline only; otherwise it covers every pipeline except those in excluded.
func (a *App) processNoReplyForOrg(orgID uuid.UUID, rule models.NoReplyRule, pipelineID *uuid.UUID, excluded []uuid.UUID, now time.Time) {
	scope := ""
	var scopeArgs []any
	switch {
	case pipelineID != nil:
		scope = " AND c.pipeline_id = ?"
		scopeArgs = []any{*pipelineID}
	case len(excluded) > 0:
		scope = " AND c.pipeline_id NOT IN ?"
		scopeArgs = []any{excluded}
	}

	for index, step := range rule.AllSteps() {
		cutoff := now.Add(-time.Duration(step.Hours) * time.Hour)

		// The first step needs our message to be the last one, older than the
		// cutoff, with no action since the customer last wrote. Later steps
		// wait on the previous step's time, in the same silence.
		condition := `c.last_message_at IS NOT NULL AND c.last_message_at < ?
			  AND (c.last_inbound_at IS NULL OR c.last_inbound_at < c.last_message_at)
			  AND (c.no_reply_handled_at IS NULL
			       OR (c.last_inbound_at IS NOT NULL AND c.no_reply_handled_at < c.last_inbound_at))`
		args := []any{now, index + 1, models.StageKindOpen, orgID, cutoff}
		if index > 0 {
			condition = `c.no_reply_step = ? AND c.no_reply_handled_at < ?
			  AND (c.last_inbound_at IS NULL OR c.last_inbound_at < c.no_reply_handled_at)`
			args = []any{now, index + 1, models.StageKindOpen, orgID, index, cutoff}
		}
		args = append(append(args, scopeArgs...), noReplyBatchSize)

		var contacts []models.Contact
		err := a.DB.Raw(`
			UPDATE contacts SET no_reply_handled_at = ?, no_reply_step = ?
			WHERE id IN (
				SELECT c.id FROM contacts c
				JOIN pipeline_stages ps ON ps.id = c.stage_id AND ps.kind = ?
				WHERE c.organization_id = ? AND c.deleted_at IS NULL
				  AND `+condition+scope+`
				ORDER BY c.last_message_at
				LIMIT ?
				FOR UPDATE OF c SKIP LOCKED
			)
			RETURNING *`, args...).Scan(&contacts).Error
		if err != nil {
			a.Log.Error("Failed to claim no-reply leads", "error", err, "org_id", orgID)
			return
		}

		var tpl *models.Template
		if step.TemplateID != nil && len(contacts) > 0 {
			var t models.Template
			if err := a.DB.Where("id = ? AND organization_id = ? AND status = ?", *step.TemplateID, orgID, "APPROVED").First(&t).Error; err == nil {
				tpl = &t
			} else {
				a.Log.Error("No-reply template unavailable", "template_id", *step.TemplateID, "org_id", orgID)
			}
		}

		for i := range contacts {
			a.applyNoReplyStep(orgID, rule.UpdatedByID, step, tpl, &contacts[i])
		}
	}
}

func (a *App) applyNoReplyStep(orgID, fallbackActor uuid.UUID, step models.NoReplyStep, tpl *models.Template, contact *models.Contact) {
	actor := fallbackActor
	if contact.AssignedUserID != nil {
		actor = *contact.AssignedUserID
	}
	if actor == uuid.Nil {
		a.Log.Error("No-reply rule has no user to act as", "org_id", orgID)
		return
	}

	var done []string

	if step.CreateTask {
		title := step.TaskTitle
		if title == "" {
			title = fmt.Sprintf("No reply for %dh: follow up", step.Hours)
		}
		due := time.Now()
		task := models.Task{
			OrganizationID: orgID,
			ContactID:      contact.ID,
			AssigneeID:     contact.AssignedUserID,
			CreatedByID:    actor,
			Title:          title,
			DueAt:          &due,
		}
		if err := a.DB.Create(&task).Error; err != nil {
			a.Log.Error("No-reply rule: failed to create task", "error", err, "contact_id", contact.ID)
		} else {
			done = append(done, "created a follow-up")
			a.broadcastTaskChanged(orgID, TaskResponse{ID: task.ID, ContactID: contact.ID}, false)
		}
	}

	if tpl != nil && templateBlocked(contact, tpl) {
		done = append(done, fmt.Sprintf("skipped template %q: contact opted out", tpl.Name))
	} else if tpl != nil {
		if _, err := a.sendNoReplyTemplate(orgID, tpl, step.TemplateParams, contact); err != nil {
			a.Log.Error("No-reply rule: failed to send template", "error", err, "contact_id", contact.ID)
			done = append(done, fmt.Sprintf("could not send template %q (%s)", tpl.Name, err.Error()))
		} else {
			done = append(done, fmt.Sprintf("sent template %q", tpl.Name))
		}
	}

	if len(done) == 0 {
		return
	}
	note := models.ConversationNote{
		OrganizationID: orgID,
		ContactID:      contact.ID,
		CreatedByID:    actor,
		Content:        fmt.Sprintf("No reply for %dh: %s", step.Hours, strings.Join(done, ", ")),
	}
	if err := a.DB.Create(&note).Error; err != nil {
		a.Log.Error("No-reply rule: failed to create note", "error", err)
	}
}

// sendNoReplyTemplate sends tpl to the contact, filling {{name}} / {{phone}}
// in the configured parameter values. It sends synchronously so a rejection
// from Meta is returned as an error (callers run in the background tick or
// in an explicit test request).
func (a *App) sendNoReplyTemplate(orgID uuid.UUID, tpl *models.Template, params map[string]string, contact *models.Contact) (*models.Message, error) {
	accountName := tpl.WhatsAppAccount
	if accountName == "" {
		accountName = contact.WhatsAppAccount
	}
	account, err := a.resolveWhatsAppAccount(orgID, accountName)
	if err != nil {
		return nil, err
	}

	name := contact.ProfileName
	if name == "" {
		name = "there"
	}
	filled := make(map[string]string, len(params))
	for k, v := range params {
		v = strings.ReplaceAll(v, "{{name}}", name)
		v = strings.ReplaceAll(v, "{{phone}}", contact.PhoneNumber)
		filled[k] = v
	}

	opts := DefaultSendOptions()
	opts.Async = false
	msg, err := a.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
		Account:      account,
		Contact:      contact,
		Type:         models.MessageTypeTemplate,
		Template:     tpl,
		BodyParams:   filled,
		HeaderParams: filled,
	}, opts)
	if err != nil {
		return nil, err
	}
	// The sender records a Meta rejection on the message instead of returning it
	var stored models.Message
	if err := a.DB.Where("id = ?", msg.ID).First(&stored).Error; err == nil {
		if stored.Status == models.MessageStatusFailed {
			return &stored, fmt.Errorf("WhatsApp rejected the message: %s", stored.ErrorMessage)
		}
		return &stored, nil
	}
	return msg, nil
}

// TestNoReplyRuleRequest sends the rule's template to one contact right away.
// Template fields are optional; the saved rule's template is used when empty.
type TestNoReplyRuleRequest struct {
	ContactID      uuid.UUID         `json:"contact_id"`
	TemplateID     *uuid.UUID        `json:"template_id"`
	TemplateParams map[string]string `json:"template_params"`
}

// TestNoReplyRule sends the no-reply template to one contact now, so admins
// can check the template and their WhatsApp account without waiting for the
// rule's delay. It does not change the contact's no-reply state.
func (a *App) TestNoReplyRule(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req TestNoReplyRuleRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	templateID, params := req.TemplateID, req.TemplateParams
	if templateID == nil {
		var org models.Organization
		if err := a.DB.Select("id", "settings").Where("id = ?", orgID).First(&org).Error; err == nil {
			rule := models.NoReplyRuleFromSettings(org.Settings)
			templateID, params = rule.TemplateID, rule.TemplateParams
		}
	}
	if templateID == nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "choose a template first", nil, "")
	}
	if msg := a.validateNoReplyTemplate(orgID, *templateID, params); msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	if !a.contactVisible(req.ContactID, userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	var contact models.Contact
	if err := a.DB.Where("id = ?", req.ContactID).First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}
	var tpl models.Template
	if err := a.DB.Where("id = ? AND organization_id = ?", *templateID, orgID).First(&tpl).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "template not found", nil, "")
	}

	if templateBlocked(&contact, &tpl) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "This contact has opted out", nil, "")
	}

	msg, err := a.sendNoReplyTemplate(orgID, &tpl, params, &contact)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]any{
		"message_id":          msg.ID,
		"status":              msg.Status,
		"whatsapp_message_id": msg.WhatsAppMessageID,
	})
}
