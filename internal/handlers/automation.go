package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/assignment"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	maxAutomationActions = 10
	maxAutomationDelay   = 720 // hours (30 days)
	automationJobBatch   = 200
	// automationSendWindow stops one rule sending one template to the same
	// lead more than once in this period, however often the trigger fires.
	automationSendWindow = 24 * time.Hour
)

// AutomationRuleRequest creates or replaces a rule.
type AutomationRuleRequest struct {
	Name       string                    `json:"name"`
	Enabled    bool                      `json:"enabled"`
	Trigger    string                    `json:"trigger"`
	PipelineID *uuid.UUID                `json:"pipeline_id"`
	StageID    *uuid.UUID                `json:"stage_id"`
	Tag        string                    `json:"tag"`
	Source     string                    `json:"source"`
	Actions    []models.AutomationAction `json:"actions"`
}

// AutomationRuleResponse is the API form of a rule.
type AutomationRuleResponse struct {
	ID         uuid.UUID                 `json:"id"`
	Name       string                    `json:"name"`
	Enabled    bool                      `json:"enabled"`
	Trigger    string                    `json:"trigger"`
	PipelineID *uuid.UUID                `json:"pipeline_id,omitempty"`
	StageID    *uuid.UUID                `json:"stage_id,omitempty"`
	Tag        string                    `json:"tag"`
	Source     string                    `json:"source"`
	Actions    []models.AutomationAction `json:"actions"`
}

func decodeAutomationActions(raw models.JSONBArray) []models.AutomationAction {
	actions := []models.AutomationAction{}
	b, err := json.Marshal(raw)
	if err != nil {
		return actions
	}
	if err := json.Unmarshal(b, &actions); err != nil {
		return []models.AutomationAction{}
	}
	return actions
}

func encodeAutomationActions(actions []models.AutomationAction) (models.JSONBArray, error) {
	b, err := json.Marshal(actions)
	if err != nil {
		return nil, err
	}
	var out models.JSONBArray
	return out, json.Unmarshal(b, &out)
}

func automationRuleToResponse(rule *models.AutomationRule) AutomationRuleResponse {
	return AutomationRuleResponse{
		ID:         rule.ID,
		Name:       rule.Name,
		Enabled:    rule.Enabled,
		Trigger:    rule.Trigger,
		PipelineID: rule.PipelineID,
		StageID:    rule.StageID,
		Tag:        rule.Tag,
		Source:     rule.Source,
		Actions:    decodeAutomationActions(rule.Actions),
	}
}

// validateAutomationRule trims the request in place and returns an error
// message, or "" when valid.
func (a *App) validateAutomationRule(orgID uuid.UUID, req *AutomationRuleRequest) string {
	req.Name = strings.TrimSpace(req.Name)
	req.Tag = strings.TrimSpace(req.Tag)
	req.Source = strings.TrimSpace(req.Source)
	if req.Name == "" || len(req.Name) > 100 {
		return "name is required and must be at most 100 characters"
	}
	if len(req.Tag) > 50 || len(req.Source) > 100 {
		return "tag or source is too long"
	}
	switch req.Trigger {
	case models.AutomationTriggerLeadCreated, models.AutomationTriggerFirstMessage,
		models.AutomationTriggerStageChanged, models.AutomationTriggerTagAdded:
	default:
		return "trigger must be lead_created, first_message, stage_changed or tag_added"
	}
	if req.PipelineID != nil {
		if _, err := a.loadPipeline(orgID, *req.PipelineID); err != nil {
			return "pipeline not found"
		}
	}
	if req.StageID != nil && !a.stageInOrg(orgID, *req.StageID) {
		return "stage not found"
	}
	if len(req.Actions) == 0 || len(req.Actions) > maxAutomationActions {
		return fmt.Sprintf("a rule needs between 1 and %d actions", maxAutomationActions)
	}
	for i := range req.Actions {
		action := &req.Actions[i]
		if action.DelayHours < 0 || action.DelayHours > maxAutomationDelay {
			return fmt.Sprintf("delay_hours must be between 0 and %d", maxAutomationDelay)
		}
		switch action.Type {
		case models.AutomationActionAssignUser:
			if action.UserID == nil || !a.isOrgMember(*action.UserID, orgID) {
				return "assign_user needs a user in this organization"
			}
		case models.AutomationActionAssignTeam:
			var count int64
			if action.TeamID != nil {
				a.DB.Model(&models.Team{}).Where("id = ? AND organization_id = ?", *action.TeamID, orgID).Count(&count)
			}
			if count == 0 {
				return "assign_team needs a team in this organization"
			}
		case models.AutomationActionAddTag:
			action.Tag = strings.TrimSpace(action.Tag)
			if action.Tag == "" || len(action.Tag) > 50 {
				return "add_tag needs a tag of at most 50 characters"
			}
		case models.AutomationActionMoveStage:
			if action.StageID == nil || !a.stageInOrg(orgID, *action.StageID) {
				return "move_stage needs a stage in this organization"
			}
		case models.AutomationActionCreateTask:
			action.TaskTitle = strings.TrimSpace(action.TaskTitle)
			if action.TaskTitle == "" || len(action.TaskTitle) > 255 {
				return "create_task needs a title of at most 255 characters"
			}
			if action.TaskDueHours < 0 || action.TaskDueHours > maxAutomationDelay {
				return fmt.Sprintf("task_due_hours must be between 0 and %d", maxAutomationDelay)
			}
		case models.AutomationActionSendTemplate:
			if action.TemplateID == nil {
				return "send_template needs a template"
			}
			if msg := a.validateNoReplyTemplate(orgID, *action.TemplateID, action.TemplateParams); msg != "" {
				return msg
			}
		default:
			return "unknown action type: " + action.Type
		}
	}
	return ""
}

func (a *App) stageInOrg(orgID, stageID uuid.UUID) bool {
	var count int64
	a.DB.Model(&models.PipelineStage{}).Where("id = ? AND organization_id = ?", stageID, orgID).Count(&count)
	return count > 0
}

// ListAutomationRules returns the organization's rules.
func (a *App) ListAutomationRules(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionRead)
	if err != nil {
		return nil
	}
	var rules []models.AutomationRule
	if err := a.DB.Where("organization_id = ?", orgID).Order("created_at ASC").Find(&rules).Error; err != nil {
		a.Log.Error("Failed to list automations", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list automations", nil, "")
	}
	out := make([]AutomationRuleResponse, len(rules))
	for i := range rules {
		out[i] = automationRuleToResponse(&rules[i])
	}
	return r.SendEnvelope(map[string]any{"rules": out})
}

// CreateAutomationRule adds a rule.
func (a *App) CreateAutomationRule(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req AutomationRuleRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if msg := a.validateAutomationRule(orgID, &req); msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	actions, err := encodeAutomationActions(req.Actions)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid actions", nil, "")
	}
	rule := models.AutomationRule{
		OrganizationID: orgID,
		Name:           req.Name,
		Enabled:        req.Enabled,
		Trigger:        req.Trigger,
		PipelineID:     req.PipelineID,
		StageID:        req.StageID,
		Tag:            req.Tag,
		Source:         req.Source,
		Actions:        actions,
		CreatedByID:    userID,
	}
	if err := a.DB.Create(&rule).Error; err != nil {
		a.Log.Error("Failed to create automation", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create automation", nil, "")
	}
	return r.SendEnvelope(automationRuleToResponse(&rule))
}

// UpdateAutomationRule replaces a rule. Waiting drip steps of a disabled rule
// do not run.
func (a *App) UpdateAutomationRule(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "automation")
	if err != nil {
		return nil
	}
	rule, err := findByIDAndOrg[models.AutomationRule](a.DB, r, id, orgID, "Automation")
	if err != nil {
		return nil
	}
	var req AutomationRuleRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if msg := a.validateAutomationRule(orgID, &req); msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	actions, err := encodeAutomationActions(req.Actions)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid actions", nil, "")
	}
	if err := a.DB.Model(rule).Updates(map[string]any{
		"name": req.Name, "enabled": req.Enabled, "trigger": req.Trigger,
		"pipeline_id": req.PipelineID, "stage_id": req.StageID,
		"tag": req.Tag, "source": req.Source, "actions": actions, "created_by_id": userID,
	}).Error; err != nil {
		a.Log.Error("Failed to update automation", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update automation", nil, "")
	}
	updated, err := findByIDAndOrg[models.AutomationRule](a.DB, r, id, orgID, "Automation")
	if err != nil {
		return nil
	}
	return r.SendEnvelope(automationRuleToResponse(updated))
}

// DeleteAutomationRule removes a rule and its waiting drip steps.
func (a *App) DeleteAutomationRule(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "automation")
	if err != nil {
		return nil
	}
	rule, err := findByIDAndOrg[models.AutomationRule](a.DB, r, id, orgID, "Automation")
	if err != nil {
		return nil
	}
	if err := a.DB.Where("rule_id = ? AND processed_at IS NULL", rule.ID).Delete(&models.AutomationJob{}).Error; err != nil {
		a.Log.Error("Failed to delete automation jobs", "error", err)
	}
	if err := a.DB.Delete(rule).Error; err != nil {
		a.Log.Error("Failed to delete automation", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete automation", nil, "")
	}
	return r.SendEnvelope(map[string]any{"message": "Automation deleted"})
}

// automationEvent says what happened to a lead.
type automationEvent struct {
	Trigger    string
	ContactID  uuid.UUID
	PipelineID *uuid.UUID
	StageID    *uuid.UUID
	Tag        string
}

// fireAutomations runs matching rules in the background. Changes made by the
// actions themselves never fire rules, so rules cannot loop.
func (a *App) fireAutomations(orgID uuid.UUID, event automationEvent) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.runAutomations(orgID, event)
	}()
}

func (a *App) runAutomations(orgID uuid.UUID, event automationEvent) {
	var rules []models.AutomationRule
	if err := a.DB.Where("organization_id = ? AND enabled = ? AND trigger = ?", orgID, true, event.Trigger).
		Order("created_at ASC").Find(&rules).Error; err != nil {
		a.Log.Error("Failed to load automations", "error", err)
		return
	}
	if len(rules) == 0 {
		return
	}
	var contact models.Contact
	if err := a.DB.Where("id = ?", event.ContactID).First(&contact).Error; err != nil {
		return
	}
	for i := range rules {
		rule := &rules[i]
		if !automationMatches(rule, &contact, event) {
			continue
		}
		for _, action := range decodeAutomationActions(rule.Actions) {
			if action.DelayHours > 0 {
				a.scheduleAutomationAction(rule, &contact, action, event)
				continue
			}
			a.runAutomationAction(rule.ID, rule.CreatedByID, orgID, event.ContactID, action)
		}
	}
}

func automationMatches(rule *models.AutomationRule, contact *models.Contact, event automationEvent) bool {
	if rule.PipelineID != nil && (event.PipelineID == nil || *rule.PipelineID != *event.PipelineID) {
		return false
	}
	if rule.StageID != nil && (event.StageID == nil || *rule.StageID != *event.StageID) {
		return false
	}
	if rule.Tag != "" {
		if event.Trigger == models.AutomationTriggerTagAdded {
			if !strings.EqualFold(rule.Tag, event.Tag) {
				return false
			}
		} else if !contactHasTag(contact, rule.Tag) {
			return false
		}
	}
	if rule.Source != "" && !strings.EqualFold(rule.Source, contactLeadSource(contact)) {
		return false
	}
	return true
}

func contactHasTag(contact *models.Contact, tag string) bool {
	for _, t := range contact.Tags {
		if s, ok := t.(string); ok && strings.EqualFold(s, tag) {
			return true
		}
	}
	return false
}

func contactLeadSource(contact *models.Contact) string {
	lead, ok := contact.Metadata[models.LeadMetadataKey].(map[string]any)
	if !ok {
		return ""
	}
	source, _ := lead["source"].(string)
	return source
}

func (a *App) scheduleAutomationAction(rule *models.AutomationRule, contact *models.Contact, action models.AutomationAction, event automationEvent) {
	b, err := json.Marshal(action)
	if err != nil {
		return
	}
	var payload models.JSONB
	if err := json.Unmarshal(b, &payload); err != nil {
		return
	}
	job := models.AutomationJob{
		ID:             uuid.New(),
		OrganizationID: rule.OrganizationID,
		RuleID:         rule.ID,
		ContactID:      contact.ID,
		Action:         payload,
		RunAt:          time.Now().Add(time.Duration(action.DelayHours) * time.Hour),
	}
	if event.Trigger == models.AutomationTriggerStageChanged {
		job.GuardStageID = event.StageID
	}
	if err := a.DB.Create(&job).Error; err != nil {
		a.Log.Error("Failed to schedule automation step", "error", err, "contact_id", contact.ID)
	}
}

// ProcessAutomationJobs runs drip steps whose time has come. A step is dropped
// if the rule was disabled, the lead replied, or the lead left the stage.
// Claiming with SKIP LOCKED means several instances never run a step twice.
func (a *App) ProcessAutomationJobs(now time.Time) {
	var jobs []models.AutomationJob
	err := a.DB.Raw(`
		UPDATE automation_jobs SET processed_at = ?
		WHERE id IN (
			SELECT id FROM automation_jobs
			WHERE processed_at IS NULL AND run_at <= ?
			ORDER BY run_at
			LIMIT ?
			FOR UPDATE SKIP LOCKED
		)
		RETURNING *`, now, now, automationJobBatch).Scan(&jobs).Error
	if err != nil {
		a.Log.Error("Failed to claim automation jobs", "error", err)
		return
	}
	for i := range jobs {
		job := &jobs[i]
		var rule models.AutomationRule
		if err := a.DB.Where("id = ? AND enabled = ?", job.RuleID, true).First(&rule).Error; err != nil {
			continue
		}
		var contact models.Contact
		if err := a.DB.Where("id = ?", job.ContactID).First(&contact).Error; err != nil {
			continue
		}
		if contact.LastInboundAt != nil && contact.LastInboundAt.After(job.CreatedAt) {
			continue
		}
		if job.GuardStageID != nil && (contact.StageID == nil || *contact.StageID != *job.GuardStageID) {
			continue
		}
		var action models.AutomationAction
		b, err := json.Marshal(job.Action)
		if err != nil || json.Unmarshal(b, &action) != nil {
			a.Log.Error("Invalid automation step", "job_id", job.ID)
			continue
		}
		a.runAutomationAction(rule.ID, rule.CreatedByID, job.OrganizationID, job.ContactID, action)
	}
}

// runAutomationAction applies one action to the lead as it is now.
func (a *App) runAutomationAction(ruleID, ruleOwner, orgID, contactID uuid.UUID, action models.AutomationAction) {
	var contact models.Contact
	if err := a.DB.Where("id = ?", contactID).First(&contact).Error; err != nil {
		return
	}
	actor := ruleOwner
	if contact.AssignedUserID != nil {
		actor = *contact.AssignedUserID
	}

	switch action.Type {
	case models.AutomationActionAssignUser:
		// Same rule as lead capture: never replace an existing owner
		if contact.AssignedUserID == nil && action.UserID != nil && a.isOrgMember(*action.UserID, orgID) {
			a.setAutomationOwner(&contact, *action.UserID)
		}
	case models.AutomationActionAssignTeam:
		if contact.AssignedUserID == nil && action.TeamID != nil && a.Assigner != nil {
			if owner := a.Assigner.AssignToTeam(*action.TeamID, orgID, nil, assignment.LeadLoadCounter); owner != nil {
				a.setAutomationOwner(&contact, *owner)
			}
		}
	case models.AutomationActionAddTag:
		if merged, changed := mergeTags(contact.Tags, []string{action.Tag}); changed {
			if err := a.DB.Model(&contact).Update("tags", merged).Error; err != nil {
				a.Log.Error("Automation: failed to add tag", "error", err, "contact_id", contact.ID)
				return
			}
			a.broadcastStageChanged(orgID, &contact)
		}
	case models.AutomationActionMoveStage:
		a.automationMoveStage(orgID, actor, &contact, action)
	case models.AutomationActionCreateTask:
		a.automationCreateTask(orgID, actor, &contact, action)
	case models.AutomationActionSendTemplate:
		a.automationSendTemplate(ruleID, orgID, actor, &contact, action)
	}
}

func (a *App) setAutomationOwner(contact *models.Contact, owner uuid.UUID) {
	res := a.DB.Model(&models.Contact{}).
		Where("id = ? AND assigned_user_id IS NULL", contact.ID).Update("assigned_user_id", owner)
	if res.Error != nil {
		a.Log.Error("Automation: failed to assign", "error", res.Error, "contact_id", contact.ID)
		return
	}
	contact.AssignedUserID = &owner
	a.broadcastStageChanged(contact.OrganizationID, contact)
}

func (a *App) automationMoveStage(orgID, actor uuid.UUID, contact *models.Contact, action models.AutomationAction) {
	if action.StageID == nil || (contact.StageID != nil && *contact.StageID == *action.StageID) {
		return
	}
	var to models.PipelineStage
	if err := a.DB.Where("id = ? AND organization_id = ?", *action.StageID, orgID).First(&to).Error; err != nil {
		return
	}
	var from *models.PipelineStage
	if contact.StageID != nil {
		var s models.PipelineStage
		if a.DB.Where("id = ?", *contact.StageID).First(&s).Error == nil {
			from = &s
		}
	}
	if err := a.placeOnStage(contact.ID, orgID, &to); err != nil {
		a.Log.Error("Automation: failed to move stage", "error", err, "contact_id", contact.ID)
		return
	}
	contact.PipelineID, contact.StageID = &to.PipelineID, &to.ID
	a.recordStageChange(orgID, actor, contact, from, &to)
	a.broadcastStageChanged(orgID, contact)
}

func (a *App) automationCreateTask(orgID, actor uuid.UUID, contact *models.Contact, action models.AutomationAction) {
	due := time.Now().Add(time.Duration(action.TaskDueHours) * time.Hour)
	task := models.Task{
		OrganizationID: orgID,
		ContactID:      contact.ID,
		AssigneeID:     contact.AssignedUserID,
		CreatedByID:    actor,
		Title:          action.TaskTitle,
		DueAt:          &due,
	}
	if err := a.DB.Create(&task).Error; err != nil {
		a.Log.Error("Automation: failed to create task", "error", err, "contact_id", contact.ID)
		return
	}
	a.broadcastTaskChanged(orgID, TaskResponse{ID: task.ID, ContactID: contact.ID}, false)
}

func (a *App) automationSendTemplate(ruleID, orgID, actor uuid.UUID, contact *models.Contact, action models.AutomationAction) {
	if action.TemplateID == nil {
		return
	}
	var tpl models.Template
	if err := a.DB.Where("id = ? AND organization_id = ? AND status = ?", *action.TemplateID, orgID, "APPROVED").First(&tpl).Error; err != nil {
		a.addLeadNote(orgID, actor, contact.ID, "Automation could not send a template: it is no longer approved")
		return
	}
	if templateBlocked(contact, &tpl) {
		return
	}
	// Templates cost money, and bulk moves or form spam can refire a trigger
	key := fmt.Sprintf("automation:sent:%s:%s:%s", ruleID, contact.ID, tpl.ID)
	claimed, err := a.Redis.SetNX(context.Background(), key, 1, automationSendWindow).Result()
	if err != nil {
		a.Log.Error("Automation: send guard unavailable", "error", err)
		return
	}
	if !claimed {
		return
	}
	if _, err := a.sendNoReplyTemplate(orgID, &tpl, action.TemplateParams, contact); err != nil {
		a.Log.Error("Automation: failed to send template", "error", err, "contact_id", contact.ID)
		a.addLeadNote(orgID, actor, contact.ID, fmt.Sprintf("Automation could not send template %q (%s)", tpl.Name, err.Error()))
		return
	}
	a.addLeadNote(orgID, actor, contact.ID, fmt.Sprintf("Automation sent template %q", tpl.Name))
}

// BulkTemplateRequest sends one approved template to many leads.
type BulkTemplateRequest struct {
	ContactIDs     []uuid.UUID       `json:"contact_ids"`
	TemplateID     uuid.UUID         `json:"template_id"`
	TemplateParams map[string]string `json:"template_params"`
}

// BulkSendTemplate queues a template to selected leads from the board. It
// answers at once and sends in the background; a failed send is written as a
// note on that lead. Needs contacts:write because templates cost money.
func (a *App) BulkSendTemplate(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceContacts, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req BulkTemplateRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if len(req.ContactIDs) == 0 || len(req.ContactIDs) > maxBulkContacts {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			fmt.Sprintf("contact_ids must have between 1 and %d items", maxBulkContacts), nil, "")
	}
	if msg := a.validateNoReplyTemplate(orgID, req.TemplateID, req.TemplateParams); msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	var tpl models.Template
	if err := a.DB.Where("id = ? AND organization_id = ?", req.TemplateID, orgID).First(&tpl).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "template not found", nil, "")
	}

	var contacts []models.Contact
	q := a.DB.Where("id IN ? AND organization_id = ?", req.ContactIDs, orgID)
	if err := a.scopeAssignedContact(q, userID, orgID).Find(&contacts).Error; err != nil {
		a.Log.Error("Failed to load contacts for template send", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to send template", nil, "")
	}

	targets := make([]models.Contact, 0, len(contacts))
	for i := range contacts {
		if templateBlocked(&contacts[i], &tpl) {
			continue
		}
		targets = append(targets, contacts[i])
	}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for i := range targets {
			contact := &targets[i]
			if _, err := a.sendNoReplyTemplate(orgID, &tpl, req.TemplateParams, contact); err != nil {
				a.Log.Error("Bulk template send failed", "error", err, "contact_id", contact.ID)
				a.addLeadNote(orgID, userID, contact.ID, fmt.Sprintf("Could not send template %q (%s)", tpl.Name, err.Error()))
			}
		}
	}()

	return r.SendEnvelope(map[string]any{"queued": len(targets), "skipped": len(req.ContactIDs) - len(targets)})
}

// TeamReportRow is one agent's line in the manager view.
type TeamReportRow struct {
	UserID             uuid.UUID `json:"user_id"`
	Name               string    `json:"name"`
	OpenLeads          int64     `json:"open_leads"`
	AwaitingReply      int64     `json:"awaiting_reply"`
	Untouched          int64     `json:"untouched"`
	OverdueTasks       int64     `json:"overdue_tasks"`
	TasksDone          int64     `json:"tasks_done"`
	MessagesSent       int64     `json:"messages_sent"`
	Won                int64     `json:"won"`
	AvgResponseSeconds *float64  `json:"avg_response_seconds"`
	WonValueMonth      float64   `json:"won_value_month"`
	MonthlyTarget      float64   `json:"monthly_target"`
}

// TeamReport is the response of GET /api/reports/team.
type TeamReport struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	NewLeads int64     `json:"new_leads"`
	Won      int64     `json:"won"`
	Lost     int64     `json:"lost"`
	Unowned  int64     `json:"unowned"`
	// Open pipeline value, and the same weighted by stage probability
	OpenValue float64         `json:"open_value"`
	Forecast  float64         `json:"forecast"`
	Agents    []TeamReportRow `json:"agents"`
}

// GetTeamReport shows whether each agent is working their leads. Open-lead
// counts are live; the rest cover from..to (RFC3339, default last 24 hours).
// Needs contacts:read because it spans every agent's leads.
func (a *App) GetTeamReport(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionRead)
	if err != nil {
		return nil
	}
	// Full view with contacts:read; team managers see only their members
	seeAll := a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID)
	var managed []uuid.UUID
	if !seeAll {
		managed = a.managedUserIDs(userID, orgID)
		if len(managed) == 0 {
			return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You do not have permission to view the team report", nil, "")
		}
	}
	to := time.Now()
	from := to.Add(-24 * time.Hour)
	for param, target := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := string(r.RequestCtx.QueryArgs().Peek(param)); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid "+param+", expected RFC3339", nil, "")
			}
			*target = t
		}
	}

	var users []models.User
	usersQuery := a.DB.Select("id", "full_name").
		Where("organization_id = ? OR id IN (?)", orgID,
			a.DB.Model(&models.UserOrganization{}).Select("user_id").Where("organization_id = ?", orgID))
	if !seeAll {
		usersQuery = usersQuery.Where("id IN ?", managed)
	}
	if err := usersQuery.Order("full_name").Find(&users).Error; err != nil {
		a.Log.Error("Failed to load team", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load team report", nil, "")
	}

	report := TeamReport{From: from, To: to, Agents: make([]TeamReportRow, len(users))}
	index := make(map[uuid.UUID]*TeamReportRow, len(users))
	for i, u := range users {
		report.Agents[i] = TeamReportRow{UserID: u.ID, Name: u.FullName}
		index[u.ID] = &report.Agents[i]
	}

	type countRow struct {
		UserID uuid.UUID
		Count  int64
	}
	var firstErr error
	collect := func(query string, apply func(*TeamReportRow, int64), args ...any) {
		var rows []countRow
		if err := a.DB.Raw(query, args...).Scan(&rows).Error; err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		for _, row := range rows {
			if agent, ok := index[row.UserID]; ok {
				apply(agent, row.Count)
			}
		}
	}
	const openLeads = `FROM contacts c JOIN pipeline_stages ps ON ps.id = c.stage_id AND ps.kind = 'open'
		WHERE c.organization_id = ? AND c.deleted_at IS NULL AND c.assigned_user_id IS NOT NULL`

	collect(`SELECT c.assigned_user_id AS user_id, COUNT(*) AS count `+openLeads+` GROUP BY 1`,
		func(row *TeamReportRow, n int64) { row.OpenLeads = n }, orgID)
	collect(`SELECT c.assigned_user_id AS user_id, COUNT(*) AS count `+openLeads+`
		AND c.last_inbound_at IS NOT NULL AND c.last_inbound_at >= c.last_message_at GROUP BY 1`,
		func(row *TeamReportRow, n int64) { row.AwaitingReply = n }, orgID)
	collect(`SELECT c.assigned_user_id AS user_id, COUNT(*) AS count `+openLeads+`
		AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.contact_id = c.id AND m.direction = 'outgoing') GROUP BY 1`,
		func(row *TeamReportRow, n int64) { row.Untouched = n }, orgID)
	collect(`SELECT assignee_id AS user_id, COUNT(*) AS count FROM tasks
		WHERE organization_id = ? AND deleted_at IS NULL AND completed_at IS NULL AND due_at < ? AND assignee_id IS NOT NULL GROUP BY 1`,
		func(row *TeamReportRow, n int64) { row.OverdueTasks = n }, orgID, time.Now())
	collect(`SELECT assignee_id AS user_id, COUNT(*) AS count FROM tasks
		WHERE organization_id = ? AND deleted_at IS NULL AND completed_at >= ? AND completed_at < ? AND assignee_id IS NOT NULL GROUP BY 1`,
		func(row *TeamReportRow, n int64) { row.TasksDone = n }, orgID, from, to)
	collect(`SELECT sent_by_user_id AS user_id, COUNT(*) AS count FROM messages
		WHERE organization_id = ? AND direction = 'outgoing' AND sent_by_user_id IS NOT NULL AND created_at >= ? AND created_at < ? GROUP BY 1`,
		func(row *TeamReportRow, n int64) { row.MessagesSent = n }, orgID, from, to)
	collect(`SELECT c.assigned_user_id AS user_id, COUNT(DISTINCT sh.contact_id) AS count FROM stage_history sh
		JOIN contacts c ON c.id = sh.contact_id
		WHERE sh.organization_id = ? AND sh.to_stage_kind = 'won' AND sh.changed_at >= ? AND sh.changed_at < ? AND c.assigned_user_id IS NOT NULL GROUP BY 1`,
		func(row *TeamReportRow, n int64) { row.Won = n }, orgID, from, to)

	// Agent reply time: an outgoing message directly after an incoming one
	var replies []struct {
		UserID uuid.UUID
		Avg    float64
	}
	if err := a.DB.Raw(`
		SELECT sent_by_user_id AS user_id, AVG(EXTRACT(EPOCH FROM created_at - prev_at)) AS avg
		FROM (
			SELECT direction, created_at, sent_by_user_id,
			       LAG(direction) OVER w AS prev_direction, LAG(created_at) OVER w AS prev_at
			FROM messages
			WHERE organization_id = ? AND created_at >= ? AND created_at < ?
			WINDOW w AS (PARTITION BY contact_id ORDER BY created_at)
		) m
		WHERE direction = 'outgoing' AND prev_direction = 'incoming' AND sent_by_user_id IS NOT NULL
		GROUP BY 1`, orgID, from, to).Scan(&replies).Error; err != nil && firstErr == nil {
		firstErr = err
	}
	for _, row := range replies {
		if agent, ok := index[row.UserID]; ok {
			avg := row.Avg
			agent.AvgResponseSeconds = &avg
		}
	}

	// Month to date against each agent's target
	monthStart := time.Date(to.Year(), to.Month(), 1, 0, 0, 0, 0, to.Location())
	var monthRows []struct {
		UserID uuid.UUID
		Value  float64
	}
	if err := a.DB.Raw(`
		SELECT c.assigned_user_id AS user_id, COALESCE(SUM(f.deal_value), 0) AS value
		FROM (SELECT DISTINCT ON (contact_id, deal_seq) contact_id, to_stage_kind, deal_value, changed_at
		      FROM stage_history WHERE organization_id = ?
		      ORDER BY contact_id, deal_seq, changed_at DESC) f
		JOIN contacts c ON c.id = f.contact_id
		WHERE f.to_stage_kind = 'won' AND f.changed_at >= ? AND c.assigned_user_id IS NOT NULL
		GROUP BY 1`, orgID, monthStart).Scan(&monthRows).Error; err != nil && firstErr == nil {
		firstErr = err
	}
	for _, row := range monthRows {
		if agent, ok := index[row.UserID]; ok {
			agent.WonValueMonth = row.Value
		}
	}
	targets := a.loadSalesSettings(orgID).MonthlyTargets
	for i := range report.Agents {
		report.Agents[i].MonthlyTarget = targets[report.Agents[i].UserID.String()]
	}

	forecast := a.DB.Model(&models.Contact{}).
		Joins("JOIN pipeline_stages ps ON ps.id = contacts.stage_id AND ps.kind = ?", models.StageKindOpen).
		Where("contacts.organization_id = ?", orgID)
	if !seeAll {
		forecast = forecast.Where("contacts.assigned_user_id IN ?", managed)
	}
	var pipelineValue struct {
		OpenValue float64
		Forecast  float64
	}
	if err := forecast.Select("COALESCE(SUM(contacts.deal_value), 0) AS open_value, COALESCE(SUM(contacts.deal_value * ps.probability / 100), 0) AS forecast").
		Scan(&pipelineValue).Error; err != nil && firstErr == nil {
		firstErr = err
	}
	report.OpenValue, report.Forecast = pipelineValue.OpenValue, pipelineValue.Forecast

	if firstErr != nil {
		a.Log.Error("Failed to load team report", "error", firstErr)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load team report", nil, "")
	}

	a.DB.Model(&models.Contact{}).Where("organization_id = ? AND created_at >= ? AND created_at < ?", orgID, from, to).Count(&report.NewLeads)
	a.DB.Model(&models.StageHistory{}).Where("organization_id = ? AND to_stage_kind = ? AND changed_at >= ? AND changed_at < ?", orgID, models.StageKindWon, from, to).Count(&report.Won)
	a.DB.Model(&models.StageHistory{}).Where("organization_id = ? AND to_stage_kind = ? AND changed_at >= ? AND changed_at < ?", orgID, models.StageKindLost, from, to).Count(&report.Lost)
	a.DB.Model(&models.Contact{}).
		Joins("JOIN pipeline_stages ps ON ps.id = contacts.stage_id AND ps.kind = ?", models.StageKindOpen).
		Where("contacts.organization_id = ? AND contacts.assigned_user_id IS NULL", orgID).Count(&report.Unowned)

	return r.SendEnvelope(report)
}
