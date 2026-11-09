package handlers

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/assignment"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CaptureLeadRequest is the body of POST /api/leads, meant for website forms,
// ad platforms and other systems (authenticate with X-API-Key).
type CaptureLeadRequest struct {
	PhoneNumber     string            `json:"phone_number"`
	Name            string            `json:"name"`
	WhatsAppAccount string            `json:"whatsapp_account"`
	PipelineID      *uuid.UUID        `json:"pipeline_id"` // default pipeline when empty
	Stage           string            `json:"stage"`       // stage name; first stage when empty
	Lead            map[string]string `json:"lead"`        // lead fields (source, budget, ...)
	Tags            []string          `json:"tags"`
	Note            string            `json:"note"`
	// Assignment: an explicit user wins; otherwise TeamID uses the team's
	// strategy (round robin / load balanced). Existing owners are kept.
	AssignedUserID *uuid.UUID `json:"assigned_user_id"`
	TeamID         *uuid.UUID `json:"team_id"`
	// FollowUpInMinutes > 0 creates a follow-up task for the owner, due then.
	FollowUpInMinutes int    `json:"follow_up_in_minutes"`
	FollowUpTitle     string `json:"follow_up_title"`
}

// CaptureLeadResponse is returned by POST /api/leads.
type CaptureLeadResponse struct {
	ContactID      uuid.UUID  `json:"contact_id"`
	Created        bool       `json:"created"`
	PipelineID     *uuid.UUID `json:"pipeline_id,omitempty"`
	StageID        *uuid.UUID `json:"stage_id,omitempty"`
	StageName      string     `json:"stage_name,omitempty"`
	AssignedUserID *uuid.UUID `json:"assigned_user_id,omitempty"`
	TaskID         *uuid.UUID `json:"task_id,omitempty"`
}

// maxFollowUpMinutes caps follow_up_in_minutes at 30 days.
const maxFollowUpMinutes = 30 * 24 * 60

// CaptureLead creates or updates a contact from an external source and puts
// it on the pipeline. It is idempotent per phone number: sending the same lead
// twice updates the contact instead of creating a duplicate.
//
// Behaviour for an existing contact: lead fields are merged, tags are added,
// the stage moves only when "stage" or "pipeline_id" is given, and an existing
// owner is never replaced.
func (a *App) CaptureLead(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceContacts, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req CaptureLeadRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	resp, status, msg := a.captureLead(orgID, userID, req)
	if resp == nil {
		return r.SendErrorEnvelope(status, msg, nil, "")
	}
	return r.SendEnvelope(*resp)
}

// captureLead is the shared path for every lead source. On failure it returns
// a nil response with an HTTP status and message.
func (a *App) captureLead(orgID, userID uuid.UUID, req CaptureLeadRequest) (*CaptureLeadResponse, int, string) {
	phone := a.normalizeOrgPhone(orgID, req.PhoneNumber)
	if phone == "" {
		return nil, fasthttp.StatusBadRequest, "phone_number is required and must contain digits"
	}
	if len(phone) > 20 {
		return nil, fasthttp.StatusBadRequest, "phone_number is too long"
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) > 255 {
		return nil, fasthttp.StatusBadRequest, "name must be at most 255 characters"
	}
	if req.FollowUpInMinutes < 0 || req.FollowUpInMinutes > maxFollowUpMinutes {
		return nil, fasthttp.StatusBadRequest, "follow_up_in_minutes must be between 0 and 43200"
	}

	var leadSet map[string]string
	if len(req.Lead) > 0 {
		set, _, msg := normalizeLeadFields(req.Lead, a.leadFields(orgID))
		if msg != "" {
			return nil, fasthttp.StatusBadRequest, msg
		}
		leadSet = set // empty values are ignored here, never removed
	}

	if req.AssignedUserID != nil && !a.isOrgMember(*req.AssignedUserID, orgID) {
		return nil, fasthttp.StatusBadRequest, "assigned_user_id is not a member of this organization"
	}
	if req.TeamID != nil {
		var count int64
		a.DB.Model(&models.Team{}).Where("id = ? AND organization_id = ?", *req.TeamID, orgID).Count(&count)
		if count == 0 {
			return nil, fasthttp.StatusBadRequest, "team_id not found"
		}
	}

	// Resolve the target stage before writing anything
	var stage *models.PipelineStage
	var contact models.Contact
	findErr := a.DB.Unscoped().Where("organization_id = ? AND phone_number = ?", orgID, phone).First(&contact).Error
	created := findErr != nil
	wantsStage := created || req.Stage != "" || req.PipelineID != nil
	if wantsStage {
		s, msg := a.resolveCaptureStage(orgID, req.PipelineID, req.Stage)
		if msg != "" {
			return nil, fasthttp.StatusBadRequest, msg
		}
		stage = s
	}

	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if created {
			contact = models.Contact{
				BaseModel:       models.BaseModel{ID: uuid.New()},
				OrganizationID:  orgID,
				PhoneNumber:     phone,
				ProfileName:     req.Name,
				WhatsAppAccount: req.WhatsAppAccount,
				Tags:            tagsToJSONB(req.Tags),
			}
			// A concurrent request may have created the same phone just now:
			// then fall through and treat it as an existing contact.
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&contact)
			if res.Error != nil || res.RowsAffected == 1 {
				return res.Error
			}
			created = false
			// Fresh struct: First on a populated struct would also filter by its new ID
			var existing models.Contact
			if err := tx.Unscoped().Where("organization_id = ? AND phone_number = ?", orgID, phone).First(&existing).Error; err != nil {
				return err
			}
			contact = existing
		}

		updates := map[string]any{}
		if contact.DeletedAt.Valid {
			updates["deleted_at"] = nil
		}
		if req.Name != "" && contact.ProfileName == "" {
			updates["profile_name"] = req.Name
		}
		if req.WhatsAppAccount != "" && contact.WhatsAppAccount == "" {
			updates["whats_app_account"] = req.WhatsAppAccount
		}
		if merged, changed := mergeTags(contact.Tags, req.Tags); changed {
			updates["tags"] = merged
		}
		if len(updates) == 0 {
			return nil
		}
		return tx.Unscoped().Model(&contact).Updates(updates).Error
	})
	if err != nil {
		a.Log.Error("Failed to capture lead", "error", err)
		return nil, fasthttp.StatusInternalServerError, "Failed to save contact"
	}

	if err := mergeLeadFields(a.DB, contact.ID, leadSet, nil); err != nil {
		a.Log.Error("Failed to save lead fields", "error", err)
		return nil, fasthttp.StatusInternalServerError, "Failed to save lead fields"
	}

	// Move onto the stage (records the note + webhook like a manual move)
	if stage != nil && (contact.StageID == nil || *contact.StageID != stage.ID) {
		var from *models.PipelineStage
		if contact.StageID != nil {
			var s models.PipelineStage
			if a.DB.Where("id = ?", *contact.StageID).First(&s).Error == nil {
				from = &s
			}
		}
		if err := a.placeOnStage(contact.ID, orgID, stage); err != nil {
			a.Log.Error("Failed to place lead on stage", "error", err)
			return nil, fasthttp.StatusInternalServerError, "Failed to place lead on pipeline"
		}
		a.recordStageChange(orgID, userID, &contact, from, stage)
	}

	// Assignment: never replace an existing owner
	if contact.AssignedUserID == nil {
		var owner *uuid.UUID
		switch {
		case req.AssignedUserID != nil:
			owner = req.AssignedUserID
		case req.TeamID != nil && a.Assigner != nil:
			owner = a.Assigner.AssignToTeam(*req.TeamID, orgID, nil, assignment.LeadLoadCounter)
		}
		if owner != nil {
			if err := a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).Update("assigned_user_id", *owner).Error; err != nil {
				a.Log.Error("Failed to assign lead", "error", err)
			} else {
				contact.AssignedUserID = owner
			}
		}
	}

	if req.Note != "" || created {
		a.addCaptureNote(orgID, userID, contact.ID, created, leadSet["source"], req.Note)
	}

	var taskID *uuid.UUID
	if req.FollowUpInMinutes > 0 {
		title := strings.TrimSpace(req.FollowUpTitle)
		if title == "" {
			title = "Contact new lead"
		}
		if len(title) > 255 {
			title = title[:255]
		}
		due := time.Now().Add(time.Duration(req.FollowUpInMinutes) * time.Minute)
		task := models.Task{
			OrganizationID: orgID,
			ContactID:      contact.ID,
			AssigneeID:     contact.AssignedUserID,
			CreatedByID:    userID,
			Title:          title,
			DueAt:          &due,
		}
		if err := a.DB.Create(&task).Error; err != nil {
			a.Log.Error("Failed to create follow-up task", "error", err)
		} else {
			taskID = &task.ID
			a.broadcastTaskChanged(orgID, TaskResponse{ID: task.ID, ContactID: contact.ID}, false)
		}
	}

	if created {
		a.fireAutomations(orgID, automationEvent{
			Trigger: models.AutomationTriggerLeadCreated, ContactID: contact.ID,
			PipelineID: contactPipelineAfterCapture(stage), StageID: contactStageAfterCapture(stage),
		})
		a.DispatchWebhook(orgID, models.WebhookEventContactCreated, ContactEventData{
			ContactID:       contact.ID.String(),
			ContactPhone:    contact.PhoneNumber,
			ContactName:     contact.ProfileName,
			WhatsAppAccount: contact.WhatsAppAccount,
		})
	}

	// Reload for the response and live board update
	if err := a.DB.Preload("AssignedUser").Where("id = ?", contact.ID).First(&contact).Error; err != nil {
		return nil, fasthttp.StatusInternalServerError, "Failed to load contact"
	}
	if contact.PipelineID != nil {
		a.broadcastStageChanged(orgID, &contact)
	}

	resp := CaptureLeadResponse{
		ContactID:      contact.ID,
		Created:        created,
		PipelineID:     contact.PipelineID,
		StageID:        contact.StageID,
		AssignedUserID: contact.AssignedUserID,
		TaskID:         taskID,
	}
	if stage != nil {
		resp.StageName = stage.Name
	} else if contact.StageID != nil {
		var s models.PipelineStage
		if a.DB.Where("id = ?", *contact.StageID).First(&s).Error == nil {
			resp.StageName = s.Name
		}
	}
	return &resp, 0, ""
}

func contactPipelineAfterCapture(stage *models.PipelineStage) *uuid.UUID {
	if stage == nil {
		return nil
	}
	return &stage.PipelineID
}

func contactStageAfterCapture(stage *models.PipelineStage) *uuid.UUID {
	if stage == nil {
		return nil
	}
	return &stage.ID
}

// resolveCaptureStage picks the stage: by name inside the pipeline (default
// pipeline when none given), or the pipeline's first stage when no name.
func (a *App) resolveCaptureStage(orgID uuid.UUID, pipelineID *uuid.UUID, stageName string) (*models.PipelineStage, string) {
	var pipeline *models.Pipeline
	var err error
	if pipelineID != nil {
		pipeline, err = a.loadPipeline(orgID, *pipelineID)
		if err != nil {
			return nil, "pipeline_id not found"
		}
	} else {
		pipeline, err = a.defaultPipeline(orgID)
		if err != nil {
			return nil, "no pipeline available"
		}
	}
	if len(pipeline.Stages) == 0 {
		return nil, "pipeline has no stages"
	}
	stageName = strings.TrimSpace(stageName)
	if stageName == "" {
		return &pipeline.Stages[0], ""
	}
	for i := range pipeline.Stages {
		if strings.EqualFold(pipeline.Stages[i].Name, stageName) {
			return &pipeline.Stages[i], ""
		}
	}
	return nil, fmt.Sprintf("stage %q not found in pipeline %q", stageName, pipeline.Name)
}

// placeOnStage puts a contact at the end of a stage column.
func (a *App) placeOnStage(contactID, orgID uuid.UUID, stage *models.PipelineStage) error {
	var maxPos *float64
	a.DB.Model(&models.Contact{}).Where("organization_id = ? AND stage_id = ?", orgID, stage.ID).
		Select("MAX(pipeline_position)").Scan(&maxPos)
	position := float64(pipelinePositionGap)
	if maxPos != nil {
		position = *maxPos + pipelinePositionGap
	}
	return a.DB.Model(&models.Contact{}).Where("id = ?", contactID).Updates(map[string]any{
		"pipeline_id":       stage.PipelineID,
		"stage_id":          stage.ID,
		"stage_entered_at":  time.Now(),
		"pipeline_position": position,
	}).Error
}

func (a *App) addCaptureNote(orgID, userID, contactID uuid.UUID, created bool, source, extra string) {
	content := "Lead updated via API"
	if created {
		content = "Lead captured via API"
	}
	if source != "" {
		content += " (source: " + source + ")"
	}
	if extra = strings.TrimSpace(extra); extra != "" {
		content += "\n" + extra
	}
	note := models.ConversationNote{
		OrganizationID: orgID,
		ContactID:      contactID,
		CreatedByID:    userID,
		Content:        content,
	}
	if err := a.DB.Create(&note).Error; err != nil {
		a.Log.Error("Failed to create capture note", "error", err)
	}
}

// normalizeLeadPhone keeps digits only (drops +, spaces, dashes, brackets).
func normalizeLeadPhone(s string) string {
	var b strings.Builder
	for _, c := range s {
		if unicode.IsDigit(c) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func tagsToJSONB(tags []string) models.JSONBArray {
	out := models.JSONBArray{}
	merged, _ := mergeTags(out, tags)
	return merged
}

// mergeTags adds new non-empty tags to existing ones, keeping order.
func mergeTags(existing models.JSONBArray, add []string) (models.JSONBArray, bool) {
	seen := make(map[string]bool, len(existing))
	out := make(models.JSONBArray, 0, len(existing)+len(add))
	for _, t := range existing {
		if s, ok := t.(string); ok {
			seen[s] = true
		}
		out = append(out, t)
	}
	changed := false
	for _, t := range add {
		t = strings.TrimSpace(t)
		if t == "" || len(t) > 50 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		changed = true
	}
	return out, changed
}
