package handlers

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/internal/utils"
	"github.com/limegreen-studio/wapcrm/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// Pipelines are viewed and worked (stage moves) with chat permissions, so every
// agent can use the board for the contacts they can already see. Creating and
// editing pipelines is an admin task gated on settings.general:write. This avoids
// a new permission resource that existing system roles would not receive.

// pipelinePositionGap is the spacing used when appending a card to a stage.
const pipelinePositionGap = 1024

// boardDefaultPerStage is how many cards a board column returns by default.
const boardDefaultPerStage = 50

// PipelineStageRequest is one stage inside a create/update pipeline request.
// ID is set for existing stages and empty for new ones.
type PipelineStageRequest struct {
	ID    *uuid.UUID `json:"id"`
	Name  string     `json:"name"`
	Color string     `json:"color"`
	Kind  string     `json:"kind"`
	// Probability is the percent chance of winning, for the revenue forecast
	Probability int `json:"probability"`
}

// PipelineRequest is the request body for creating/updating a pipeline.
// Stages are given in display order.
type PipelineRequest struct {
	Name      string                 `json:"name"`
	IsDefault bool                   `json:"is_default"`
	Stages    []PipelineStageRequest `json:"stages"`
}

// PipelineStageResponse is the API response for a pipeline stage.
type PipelineStageResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Color       string    `json:"color"`
	Position    int       `json:"position"`
	Kind        string    `json:"kind"`
	Probability int       `json:"probability"`
}

// PipelineResponse is the API response for a pipeline.
type PipelineResponse struct {
	ID        uuid.UUID               `json:"id"`
	Name      string                  `json:"name"`
	IsDefault bool                    `json:"is_default"`
	Stages    []PipelineStageResponse `json:"stages"`
	CreatedAt time.Time               `json:"created_at"`
	UpdatedAt time.Time               `json:"updated_at"`
}

// BoardCard is a contact as shown on a Kanban card.
type BoardCard struct {
	ID                 uuid.UUID  `json:"id"`
	PhoneNumber        string     `json:"phone_number"`
	ProfileName        string     `json:"profile_name"`
	Tags               []string   `json:"tags"`
	AssignedUserID     *uuid.UUID `json:"assigned_user_id,omitempty"`
	AssignedUserName   string     `json:"assigned_user_name,omitempty"`
	LastMessageAt      *time.Time `json:"last_message_at,omitempty"`
	LastMessagePreview string     `json:"last_message_preview"`
	PipelineID         *uuid.UUID `json:"pipeline_id,omitempty"`
	StageID            *uuid.UUID `json:"stage_id,omitempty"`
	StageEnteredAt     *time.Time `json:"stage_entered_at,omitempty"`
	PipelinePosition   float64    `json:"pipeline_position"`
	ServiceWindowOpen  bool       `json:"service_window_open"`
	OpenTasks          int        `json:"open_tasks"`
	NextTaskDueAt      *time.Time `json:"next_task_due_at,omitempty"`
	DealValue          float64    `json:"deal_value"`
	CloseReason        string     `json:"close_reason,omitempty"`
	AwaitingReply      bool       `json:"awaiting_reply"` // the customer wrote last
	WindowExpiresAt    *time.Time `json:"window_expires_at,omitempty"`
}

// BoardColumn is one stage of the board with its cards.
type BoardColumn struct {
	PipelineStageResponse
	Total int64       `json:"total"`
	Value float64     `json:"value"` // sum of deal values in this stage
	Cards []BoardCard `json:"cards"`
}

// ContactStageRequest moves a contact onto a stage. A null stage_id removes the
// contact from its pipeline. Position is optional; when omitted the card is
// appended to the end of the stage.
type ContactStageRequest struct {
	StageID  *uuid.UUID `json:"stage_id"`
	Position *float64   `json:"position"`
}

// StageChangedEventData is the webhook payload for contact.stage_changed.
type StageChangedEventData struct {
	ContactID     string  `json:"contact_id"`
	ContactPhone  string  `json:"contact_phone"`
	ContactName   string  `json:"contact_name"`
	PipelineID    *string `json:"pipeline_id"`
	PipelineName  string  `json:"pipeline_name,omitempty"`
	FromStageID   *string `json:"from_stage_id"`
	FromStageName string  `json:"from_stage_name,omitempty"`
	ToStageID     *string `json:"to_stage_id"`
	ToStageName   string  `json:"to_stage_name,omitempty"`
	ChangedByID   string  `json:"changed_by_id,omitempty"`
}

// ListPipelines returns the organization's pipelines with their stages. The
// first call for an organization creates a default "Sales" pipeline.
func (a *App) ListPipelines(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceChat, models.ActionRead)
	if err != nil {
		return nil
	}

	pipelines, err := a.loadPipelines(orgID)
	if err != nil {
		a.Log.Error("Failed to list pipelines", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list pipelines", nil, "")
	}

	if len(pipelines) == 0 {
		p, err := a.createDefaultPipeline(orgID)
		if err != nil {
			a.Log.Error("Failed to create default pipeline", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create default pipeline", nil, "")
		}
		pipelines = []models.Pipeline{*p}
	}

	result := make([]PipelineResponse, len(pipelines))
	for i, p := range pipelines {
		result[i] = pipelineToResponse(p)
	}

	return r.SendEnvelope(map[string]any{"pipelines": result})
}

// CreatePipeline creates a new pipeline with its stages.
func (a *App) CreatePipeline(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req PipelineRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	if msg := validatePipelineRequest(&req); msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}

	pipeline := models.Pipeline{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		Name:           req.Name,
		IsDefault:      req.IsDefault,
	}

	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if req.IsDefault {
			if err := clearDefaultPipeline(tx, orgID); err != nil {
				return err
			}
		}
		if err := tx.Create(&pipeline).Error; err != nil {
			return err
		}
		for i, s := range req.Stages {
			stage := models.PipelineStage{
				OrganizationID: orgID,
				PipelineID:     pipeline.ID,
				Name:           s.Name,
				Color:          s.Color,
				Kind:           s.Kind,
				Position:       i,
				Probability:    s.Probability,
			}
			if err := tx.Create(&stage).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		a.Log.Error("Failed to create pipeline", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create pipeline", nil, "")
	}

	created, err := a.loadPipeline(orgID, pipeline.ID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load pipeline", nil, "")
	}
	return r.SendEnvelope(pipelineToResponse(*created))
}

// UpdatePipeline renames a pipeline and replaces its stage list. Stages with an
// id are updated, stages without one are created, and stages left out are
// deleted. A stage that still holds contacts cannot be deleted.
func (a *App) UpdatePipeline(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}

	pipelineID, err := parsePathUUID(r, "id", "pipeline")
	if err != nil {
		return nil
	}

	pipeline, err := a.loadPipeline(orgID, pipelineID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Pipeline not found", nil, "")
	}

	var req PipelineRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	if msg := validatePipelineRequest(&req); msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}

	existing := make(map[uuid.UUID]bool, len(pipeline.Stages))
	for _, s := range pipeline.Stages {
		existing[s.ID] = true
	}
	kept := make(map[uuid.UUID]bool, len(req.Stages))
	for _, s := range req.Stages {
		if s.ID != nil {
			if !existing[*s.ID] {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Stage does not belong to this pipeline", nil, "")
			}
			kept[*s.ID] = true
		}
	}
	var removed []uuid.UUID
	for id := range existing {
		if !kept[id] {
			removed = append(removed, id)
		}
	}

	if len(removed) > 0 {
		var inUse int64
		a.DB.Model(&models.Contact{}).
			Where("organization_id = ? AND stage_id IN ?", orgID, removed).
			Count(&inUse)
		if inUse > 0 {
			return r.SendErrorEnvelope(fasthttp.StatusConflict,
				fmt.Sprintf("Cannot delete a stage that still has %d contact(s). Move them first.", inUse), nil, "")
		}
	}

	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if req.IsDefault && !pipeline.IsDefault {
			if err := clearDefaultPipeline(tx, orgID); err != nil {
				return err
			}
		}
		if err := tx.Model(&models.Pipeline{}).Where("id = ?", pipeline.ID).
			Updates(map[string]any{"name": req.Name, "is_default": req.IsDefault}).Error; err != nil {
			return err
		}
		if len(removed) > 0 {
			if err := tx.Where("id IN ?", removed).Delete(&models.PipelineStage{}).Error; err != nil {
				return err
			}
		}
		for i, s := range req.Stages {
			if s.ID != nil {
				if err := tx.Model(&models.PipelineStage{}).Where("id = ?", *s.ID).
					Updates(map[string]any{"name": s.Name, "color": s.Color, "kind": s.Kind, "position": i, "probability": s.Probability}).Error; err != nil {
					return err
				}
				continue
			}
			stage := models.PipelineStage{
				OrganizationID: orgID,
				PipelineID:     pipeline.ID,
				Name:           s.Name,
				Color:          s.Color,
				Kind:           s.Kind,
				Position:       i,
				Probability:    s.Probability,
			}
			if err := tx.Create(&stage).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		a.Log.Error("Failed to update pipeline", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update pipeline", nil, "")
	}

	updated, err := a.loadPipeline(orgID, pipeline.ID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load pipeline", nil, "")
	}
	return r.SendEnvelope(pipelineToResponse(*updated))
}

// DeletePipeline deletes a pipeline and its stages. Contacts on it are taken off
// the pipeline; the contacts themselves are kept.
func (a *App) DeletePipeline(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}

	pipelineID, err := parsePathUUID(r, "id", "pipeline")
	if err != nil {
		return nil
	}

	pipeline, err := findByIDAndOrg[models.Pipeline](a.DB, r, pipelineID, orgID, "Pipeline")
	if err != nil {
		return nil
	}

	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Contact{}).
			Where("organization_id = ? AND pipeline_id = ?", orgID, pipeline.ID).
			Updates(map[string]any{"pipeline_id": nil, "stage_id": nil, "stage_entered_at": nil, "pipeline_position": 0}).Error; err != nil {
			return err
		}
		if err := tx.Where("pipeline_id = ?", pipeline.ID).Delete(&models.PipelineStage{}).Error; err != nil {
			return err
		}
		return tx.Delete(pipeline).Error
	})
	if err != nil {
		a.Log.Error("Failed to delete pipeline", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete pipeline", nil, "")
	}

	return r.SendEnvelope(map[string]any{"message": "Pipeline deleted"})
}

// GetPipelineBoard returns a pipeline's stages with the contacts on each stage,
// ordered by pipeline_position. Contacts are scoped the same way as the contact
// list: users without contacts:read only see contacts assigned to them.
//
// Query params: search, assigned_user_id (uuid or "unassigned"), tag, lead_key
// with lead_value, created_after and created_before (RFC3339), limit (per stage).
// With stage_id (and offset), only that column is returned: used by a column's
// "load more" to fetch the next cards.
func (a *App) GetPipelineBoard(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionRead)
	if err != nil {
		return nil
	}

	pipelineID, err := parsePathUUID(r, "id", "pipeline")
	if err != nil {
		return nil
	}

	pipeline, err := a.loadPipeline(orgID, pipelineID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Pipeline not found", nil, "")
	}

	pg := parsePaginationWithDefaults(r, boardDefaultPerStage, 200)
	offset := 0
	stages := pipeline.Stages
	if v := string(r.RequestCtx.QueryArgs().Peek("stage_id")); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid stage_id", nil, "")
		}
		stages = nil
		for _, s := range pipeline.Stages {
			if s.ID == id {
				stages = []models.PipelineStage{s}
			}
		}
		if stages == nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Stage does not belong to this pipeline", nil, "")
		}
		offset = r.RequestCtx.QueryArgs().GetUintOrZero("offset")
	}
	search := string(r.RequestCtx.QueryArgs().Peek("search"))
	assignedParam := string(r.RequestCtx.QueryArgs().Peek("assigned_user_id"))
	tag := string(r.RequestCtx.QueryArgs().Peek("tag"))
	leadKey := string(r.RequestCtx.QueryArgs().Peek("lead_key"))
	leadValue := string(r.RequestCtx.QueryArgs().Peek("lead_value"))
	var createdBounds []struct {
		op string
		at time.Time
	}
	for param, op := range map[string]string{"created_after": ">=", "created_before": "<"} {
		v := string(r.RequestCtx.QueryArgs().Peek(param))
		if v == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid "+param+", expected RFC3339", nil, "")
		}
		createdBounds = append(createdBounds, struct {
			op string
			at time.Time
		}{op, at})
	}

	base := func() *gorm.DB {
		q := a.DB.Model(&models.Contact{}).Where("contacts.organization_id = ? AND contacts.pipeline_id = ?", orgID, pipeline.ID)
		q = a.scopeAssignedContact(q, userID, orgID)
		if search != "" {
			if len(search) > 1000 {
				search = search[:1000]
			}
			pattern := "%" + search + "%"
			q = q.Where("phone_number LIKE ? OR profile_name ILIKE ?", pattern, pattern)
		}
		switch assignedParam {
		case "":
		case "unassigned":
			q = q.Where("assigned_user_id IS NULL")
		default:
			if id, err := uuid.Parse(assignedParam); err == nil {
				q = q.Where("assigned_user_id = ?", id)
			}
		}
		if tag != "" {
			q = q.Where("contacts.tags @> ?::jsonb", jsonStringArray(tag))
		}
		if leadKey != "" && leadValue != "" {
			q = q.Where("contacts.metadata->'"+models.LeadMetadataKey+"'->>? ILIKE ?", leadKey, "%"+leadValue+"%")
		}
		for _, b := range createdBounds {
			q = q.Where("contacts.created_at "+b.op+" ?", b.at)
		}
		return q
	}

	// Per-stage totals in one query
	var counts []struct {
		StageID uuid.UUID
		Count   int64
		Value   float64
	}
	if err := base().Select("stage_id, COUNT(*) AS count, COALESCE(SUM(deal_value), 0) AS value").Group("stage_id").Scan(&counts).Error; err != nil {
		a.Log.Error("Failed to count board contacts", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load board", nil, "")
	}
	totals := make(map[uuid.UUID]int64, len(counts))
	values := make(map[uuid.UUID]float64, len(counts))
	for _, c := range counts {
		totals[c.StageID] = c.Count
		values[c.StageID] = c.Value
	}

	shouldMask := a.ShouldMaskPhoneNumbers(orgID)
	columns := make([]BoardColumn, len(stages))
	for i, s := range stages {
		columns[i] = BoardColumn{
			PipelineStageResponse: stageToResponse(s),
			Total:                 totals[s.ID],
			Value:                 values[s.ID],
			Cards:                 []BoardCard{},
		}
		if totals[s.ID] == 0 {
			continue
		}
		var contacts []models.Contact
		if err := base().Where("stage_id = ?", s.ID).
			Preload("AssignedUser").
			Order("pipeline_position ASC, stage_entered_at ASC").
			Offset(offset).Limit(pg.Limit).
			Find(&contacts).Error; err != nil {
			a.Log.Error("Failed to load board contacts", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load board", nil, "")
		}
		cards := make([]BoardCard, len(contacts))
		for j := range contacts {
			cards[j] = contactToBoardCard(&contacts[j], shouldMask)
		}
		columns[i].Cards = cards
	}

	a.attachTaskSummaries(columns)

	return r.SendEnvelope(map[string]any{
		"pipeline": pipelineToResponse(*pipeline),
		"columns":  columns,
	})
}

// UpdateContactStage moves a contact to a pipeline stage (or off its pipeline).
// A real stage change resets stage_entered_at, writes a conversation note so the
// move shows in chat, broadcasts over WebSocket and fires contact.stage_changed.
func (a *App) UpdateContactStage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}

	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}

	var req ContactStageRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	var contact models.Contact
	q := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID)
	if err := a.scopeAssignedContact(q, userID, orgID).First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	var fromStage, toStage *models.PipelineStage
	if contact.StageID != nil {
		var s models.PipelineStage
		if err := a.DB.Where("id = ?", *contact.StageID).First(&s).Error; err == nil {
			fromStage = &s
		}
	}

	updates := map[string]any{}
	if req.StageID == nil {
		if contact.PipelineID == nil {
			return r.SendEnvelope(contactToBoardCard(&contact, a.ShouldMaskPhoneNumbers(orgID)))
		}
		updates["pipeline_id"] = nil
		updates["stage_id"] = nil
		updates["stage_entered_at"] = nil
		updates["pipeline_position"] = 0
	} else {
		var s models.PipelineStage
		if err := a.DB.Where("id = ? AND organization_id = ?", *req.StageID, orgID).First(&s).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Stage not found", nil, "")
		}
		toStage = &s

		position := 0.0
		if req.Position != nil {
			position = *req.Position
		} else {
			var maxPos *float64
			a.DB.Model(&models.Contact{}).
				Where("organization_id = ? AND stage_id = ? AND id <> ?", orgID, s.ID, contact.ID).
				Select("MAX(pipeline_position)").Scan(&maxPos)
			if maxPos != nil {
				position = *maxPos + pipelinePositionGap
			} else {
				position = pipelinePositionGap
			}
		}

		updates["pipeline_id"] = s.PipelineID
		updates["stage_id"] = s.ID
		updates["pipeline_position"] = position
		if contact.StageID == nil || *contact.StageID != s.ID {
			updates["stage_entered_at"] = time.Now()
		}
	}

	if err := a.DB.Model(&contact).Updates(updates).Error; err != nil {
		a.Log.Error("Failed to update contact stage", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update stage", nil, "")
	}

	if err := a.DB.Preload("AssignedUser").Where("id = ?", contact.ID).First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load contact", nil, "")
	}
	card := a.boardCard(&contact, orgID)

	stageChanged := (fromStage == nil) != (toStage == nil) ||
		(fromStage != nil && toStage != nil && fromStage.ID != toStage.ID)
	if stageChanged {
		a.recordStageChange(orgID, userID, &contact, fromStage, toStage)
		if toStage != nil {
			a.fireAutomations(orgID, automationEvent{
				Trigger: models.AutomationTriggerStageChanged, ContactID: contact.ID,
				PipelineID: &toStage.PipelineID, StageID: &toStage.ID,
			})
		}
	}

	a.broadcastStageChanged(orgID, &contact)

	return r.SendEnvelope(card)
}

// PipelineStats is the response of GET /api/pipelines/{id}/stats.
type PipelineStats struct {
	Total          int64         `json:"total"`
	ByStage        []StageCount  `json:"by_stage"`
	BySource       []SourceCount `json:"by_source"`
	Won            int64         `json:"won"`
	Lost           int64         `json:"lost"`
	Open           int64         `json:"open"`
	WinRate        *float64      `json:"win_rate"` // won / (won + lost); null when nothing closed
	OverdueTasks   int64         `json:"overdue_tasks"`
	StaleOpenLeads int64         `json:"stale_open_leads"` // open leads quiet for 7+ days (last message, else creation)
	Since          *time.Time    `json:"since,omitempty"`
}

// StageCount is the number of contacts on one stage.
type StageCount struct {
	StageID uuid.UUID `json:"stage_id"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Count   int64     `json:"count"`
}

// SourceCount is the number of contacts per metadata.lead.source.
type SourceCount struct {
	Source string `json:"source"` // "" = not set
	Count  int64  `json:"count"`
	Won    int64  `json:"won"`
}

// GetPipelineStats returns conversion numbers for a pipeline, scoped like the
// board. Query params: since (RFC3339, filters on contact created_at),
// assigned_user_id ("me", "unassigned" or a uuid).
func (a *App) GetPipelineStats(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionRead)
	if err != nil {
		return nil
	}

	pipelineID, err := parsePathUUID(r, "id", "pipeline")
	if err != nil {
		return nil
	}

	pipeline, err := a.loadPipeline(orgID, pipelineID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Pipeline not found", nil, "")
	}

	stats := PipelineStats{ByStage: []StageCount{}, BySource: []SourceCount{}}
	args := r.RequestCtx.QueryArgs()

	var since *time.Time
	if v := string(args.Peek("since")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid since, expected RFC3339", nil, "")
		}
		since = &t
		stats.Since = since
	}
	assignedParam := string(args.Peek("assigned_user_id"))

	base := func() *gorm.DB {
		q := a.DB.Model(&models.Contact{}).
			Joins("JOIN pipeline_stages ps ON ps.id = contacts.stage_id").
			Where("contacts.organization_id = ? AND contacts.pipeline_id = ?", orgID, pipeline.ID)
		q = a.scopeAssignedContact(q, userID, orgID)
		if since != nil {
			q = q.Where("contacts.created_at >= ?", *since)
		}
		switch assignedParam {
		case "":
		case "me":
			q = q.Where("contacts.assigned_user_id = ?", userID)
		case "unassigned":
			q = q.Where("contacts.assigned_user_id IS NULL")
		default:
			if id, err := uuid.Parse(assignedParam); err == nil {
				q = q.Where("contacts.assigned_user_id = ?", id)
			}
		}
		return q
	}

	var stageRows []struct {
		StageID uuid.UUID
		Count   int64
	}
	if err := base().Select("contacts.stage_id, COUNT(*) AS count").Group("contacts.stage_id").Scan(&stageRows).Error; err != nil {
		a.Log.Error("Failed to load stage counts", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load stats", nil, "")
	}
	counts := make(map[uuid.UUID]int64, len(stageRows))
	for _, row := range stageRows {
		counts[row.StageID] = row.Count
	}
	for _, s := range pipeline.Stages {
		n := counts[s.ID]
		stats.ByStage = append(stats.ByStage, StageCount{StageID: s.ID, Name: s.Name, Kind: s.Kind, Count: n})
		stats.Total += n
		switch s.Kind {
		case models.StageKindWon:
			stats.Won += n
		case models.StageKindLost:
			stats.Lost += n
		default:
			stats.Open += n
		}
	}
	if closed := stats.Won + stats.Lost; closed > 0 {
		rate := float64(stats.Won) / float64(closed)
		stats.WinRate = &rate
	}

	sourceExpr := "COALESCE(contacts.metadata->'" + models.LeadMetadataKey + "'->>'source', '')"
	if err := base().
		Select(sourceExpr + " AS source, COUNT(*) AS count, COUNT(*) FILTER (WHERE ps.kind = 'won') AS won").
		Group(sourceExpr).Order("count DESC").Limit(20).
		Scan(&stats.BySource).Error; err != nil {
		a.Log.Error("Failed to load source counts", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load stats", nil, "")
	}

	base().Where("ps.kind = ? AND COALESCE(contacts.last_message_at, contacts.created_at) < ?",
		models.StageKindOpen, time.Now().Add(-7*24*time.Hour)).Count(&stats.StaleOpenLeads)

	a.DB.Model(&models.Task{}).
		Where("tasks.contact_id IN (?)", base().Select("contacts.id")).
		Where("tasks.completed_at IS NULL AND tasks.due_at < ?", time.Now()).
		Count(&stats.OverdueTasks)

	return r.SendEnvelope(stats)
}

// stageChangedPayload is the org-wide WS payload for a stage change. It
// carries ids only: every user in the org receives it, including agents who
// may not see this contact, so name, phone and message preview must not be
// included. Clients reload their board through their own permission scope.
func stageChangedPayload(c *models.Contact) map[string]any {
	return map[string]any{
		"contact_id":       c.ID,
		"pipeline_id":      c.PipelineID,
		"stage_id":         c.StageID,
		"stage_entered_at": c.StageEnteredAt,
	}
}

func (a *App) broadcastStageChanged(orgID uuid.UUID, c *models.Contact) {
	if a.WSHub == nil {
		return
	}
	a.WSHub.BroadcastToOrg(orgID, websocket.WSMessage{
		Type:    websocket.TypeContactStageChanged,
		Payload: stageChangedPayload(c),
	})
}

// boardCard builds one card including its task summary.
func (a *App) boardCard(c *models.Contact, orgID uuid.UUID) BoardCard {
	cols := []BoardColumn{{Cards: []BoardCard{contactToBoardCard(c, a.ShouldMaskPhoneNumbers(orgID))}}}
	a.attachTaskSummaries(cols)
	return cols[0].Cards[0]
}

// attachTaskSummaries fills OpenTasks / NextTaskDueAt on every loaded card
// with one grouped query.
func (a *App) attachTaskSummaries(columns []BoardColumn) {
	var ids []uuid.UUID
	for _, col := range columns {
		for _, card := range col.Cards {
			ids = append(ids, card.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	var rows []struct {
		ContactID uuid.UUID
		Open      int
		NextDue   *time.Time
	}
	if err := a.DB.Model(&models.Task{}).
		Select("contact_id, COUNT(*) AS open, MIN(due_at) AS next_due").
		Where("contact_id IN ? AND completed_at IS NULL", ids).
		Group("contact_id").Scan(&rows).Error; err != nil {
		a.Log.Error("Failed to load task summaries", "error", err)
		return
	}
	byContact := make(map[uuid.UUID]int, len(rows))
	for i, row := range rows {
		byContact[row.ContactID] = i
	}
	for c := range columns {
		for k := range columns[c].Cards {
			card := &columns[c].Cards[k]
			if i, ok := byContact[card.ID]; ok {
				card.OpenTasks = rows[i].Open
				card.NextTaskDueAt = rows[i].NextDue
			}
		}
	}
}

// recordStageChange writes the history row and note, and fires the webhook.
func (a *App) recordStageChange(orgID, userID uuid.UUID, contact *models.Contact, from, to *models.PipelineStage) {
	a.writeStageHistory(orgID, userID, contact, from, to)

	pipelineName := ""
	var pipelineID *uuid.UUID
	if to != nil {
		pipelineID = &to.PipelineID
	} else if from != nil {
		pipelineID = &from.PipelineID
	}
	if pipelineID != nil {
		var p models.Pipeline
		if err := a.DB.Unscoped().Where("id = ?", *pipelineID).First(&p).Error; err == nil {
			pipelineName = p.Name
		}
	}

	var content string
	switch {
	case from == nil:
		content = fmt.Sprintf("Added to pipeline %q at stage %q", pipelineName, to.Name)
	case to == nil:
		content = fmt.Sprintf("Removed from pipeline %q (was %q)", pipelineName, from.Name)
	default:
		content = fmt.Sprintf("Stage changed: %s → %s", from.Name, to.Name)
	}

	note := models.ConversationNote{
		OrganizationID: orgID,
		ContactID:      contact.ID,
		CreatedByID:    userID,
		Content:        content,
	}
	if err := a.DB.Create(&note).Error; err != nil {
		a.Log.Error("Failed to create stage change note", "error", err)
	} else if a.WSHub != nil {
		var user models.User
		a.DB.First(&user, "id = ?", userID)
		note.CreatedBy = &user
		a.WSHub.BroadcastToContact(orgID, contact.ID, websocket.WSMessage{
			Type:    websocket.TypeConversationNoteCreated,
			Payload: noteToResponse(note),
		})
	}

	data := StageChangedEventData{
		ContactID:    contact.ID.String(),
		ContactPhone: contact.PhoneNumber,
		ContactName:  contact.ProfileName,
		PipelineName: pipelineName,
	}
	if userID != uuid.Nil {
		data.ChangedByID = userID.String()
	}
	if pipelineID != nil {
		id := pipelineID.String()
		data.PipelineID = &id
	}
	if from != nil {
		id := from.ID.String()
		data.FromStageID = &id
		data.FromStageName = from.Name
	}
	if to != nil {
		id := to.ID.String()
		data.ToStageID = &id
		data.ToStageName = to.Name
	}
	a.DispatchWebhook(orgID, models.WebhookEventStageChanged, data)
}

// writeStageHistory appends one row to stage_history. Failure is logged, not
// returned: the move itself has already happened.
func (a *App) writeStageHistory(orgID, userID uuid.UUID, contact *models.Contact, from, to *models.PipelineStage) {
	row := models.StageHistory{
		ID:             uuid.New(),
		OrganizationID: orgID,
		ContactID:      contact.ID,
		ChangedAt:      time.Now(),
		DealSeq:        contact.DealSeq,
		DealValue:      contact.DealValue,
		CloseReason:    contact.CloseReason,
	}
	if userID != uuid.Nil {
		row.ChangedByID = &userID
	}
	if from != nil {
		row.PipelineID, row.FromStageID, row.FromStageName = &from.PipelineID, &from.ID, from.Name
	}
	if to != nil {
		row.PipelineID, row.ToStageID, row.ToStageName, row.ToStageKind = &to.PipelineID, &to.ID, to.Name, to.Kind
	}
	if err := a.DB.Create(&row).Error; err != nil {
		a.Log.Error("Failed to write stage history", "error", err, "contact_id", contact.ID)
	}
}

// loadPipelines returns all pipelines of an org with stages ordered by position.
func (a *App) loadPipelines(orgID uuid.UUID) ([]models.Pipeline, error) {
	var pipelines []models.Pipeline
	err := a.DB.Where("organization_id = ?", orgID).
		Preload("Stages", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC") }).
		Order("is_default DESC, created_at ASC").
		Find(&pipelines).Error
	return pipelines, err
}

// loadPipeline returns one pipeline of an org with stages ordered by position.
func (a *App) loadPipeline(orgID, pipelineID uuid.UUID) (*models.Pipeline, error) {
	var p models.Pipeline
	err := a.DB.Where("id = ? AND organization_id = ?", pipelineID, orgID).
		Preload("Stages", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC") }).
		First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// createDefaultPipeline creates the "Sales" pipeline with DefaultPipelineStages.
func (a *App) createDefaultPipeline(orgID uuid.UUID) (*models.Pipeline, error) {
	pipeline := models.Pipeline{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		Name:           "Sales",
		IsDefault:      true,
	}
	existing := false
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// Concurrent first requests (two leads at once) must create only one
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "default-pipeline:"+orgID.String()).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.Pipeline{}).Where("organization_id = ?", orgID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			existing = true
			return nil
		}
		if err := tx.Create(&pipeline).Error; err != nil {
			return err
		}
		for i, s := range models.DefaultPipelineStages() {
			s.OrganizationID = orgID
			s.PipelineID = pipeline.ID
			s.Position = i
			if err := tx.Create(&s).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if existing {
		pipelines, err := a.loadPipelines(orgID)
		if err != nil {
			return nil, err
		}
		return &pipelines[0], nil
	}
	return a.loadPipeline(orgID, pipeline.ID)
}

// defaultPipeline returns the org's default pipeline (or its oldest one),
// creating the default pipeline when the org has none.
func (a *App) defaultPipeline(orgID uuid.UUID) (*models.Pipeline, error) {
	pipelines, err := a.loadPipelines(orgID)
	if err != nil {
		return nil, err
	}
	if len(pipelines) == 0 {
		return a.createDefaultPipeline(orgID)
	}
	return &pipelines[0], nil
}

func clearDefaultPipeline(tx *gorm.DB, orgID uuid.UUID) error {
	return tx.Model(&models.Pipeline{}).
		Where("organization_id = ? AND is_default = ?", orgID, true).
		Update("is_default", false).Error
}

// validatePipelineRequest trims and validates a pipeline request in place and
// returns an error message, or "" when valid.
func validatePipelineRequest(req *PipelineRequest) string {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return "name is required"
	}
	if len(req.Name) > 100 {
		return "name must be at most 100 characters"
	}
	if len(req.Stages) == 0 {
		return "at least one stage is required"
	}
	if len(req.Stages) > 30 {
		return "a pipeline can have at most 30 stages"
	}
	seen := make(map[string]bool, len(req.Stages))
	for i := range req.Stages {
		s := &req.Stages[i]
		s.Name = strings.TrimSpace(s.Name)
		if s.Name == "" {
			return "stage name is required"
		}
		if len(s.Name) > 100 {
			return "stage name must be at most 100 characters"
		}
		key := strings.ToLower(s.Name)
		if seen[key] {
			return fmt.Sprintf("duplicate stage name %q", s.Name)
		}
		seen[key] = true
		if s.Kind == "" {
			s.Kind = models.StageKindOpen
		}
		if !models.IsValidStageKind(s.Kind) {
			return "invalid stage kind. Valid kinds: open, won, lost"
		}
		if s.Probability < 0 || s.Probability > 100 {
			return "stage probability must be between 0 and 100"
		}
		if !models.IsValidTagColor(s.Color) {
			return "invalid stage color. Valid colors: blue, red, green, yellow, purple, gray"
		}
	}
	return ""
}

func stageToResponse(s models.PipelineStage) PipelineStageResponse {
	return PipelineStageResponse{
		ID:          s.ID,
		Name:        s.Name,
		Color:       s.Color,
		Position:    s.Position,
		Kind:        s.Kind,
		Probability: s.Probability,
	}
}

func pipelineToResponse(p models.Pipeline) PipelineResponse {
	stages := make([]PipelineStageResponse, len(p.Stages))
	for i, s := range p.Stages {
		stages[i] = stageToResponse(s)
	}
	return PipelineResponse{
		ID:        p.ID,
		Name:      p.Name,
		IsDefault: p.IsDefault,
		Stages:    stages,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

func contactToBoardCard(c *models.Contact, shouldMask bool) BoardCard {
	tags := make([]string, 0, len(c.Tags))
	for _, t := range c.Tags {
		if s, ok := t.(string); ok {
			tags = append(tags, s)
		}
	}
	phone, name := c.PhoneNumber, c.ProfileName
	if shouldMask {
		phone = utils.MaskPhoneNumber(phone)
		name = utils.MaskIfPhoneNumber(name)
	}
	card := BoardCard{
		ID:                 c.ID,
		PhoneNumber:        phone,
		ProfileName:        name,
		Tags:               tags,
		AssignedUserID:     c.AssignedUserID,
		LastMessageAt:      c.LastMessageAt,
		LastMessagePreview: c.LastMessagePreview,
		PipelineID:         c.PipelineID,
		StageID:            c.StageID,
		StageEnteredAt:     c.StageEnteredAt,
		PipelinePosition:   c.PipelinePosition,
		ServiceWindowOpen:  c.LastInboundAt != nil && time.Since(*c.LastInboundAt) < 24*time.Hour,
		DealValue:          c.DealValue,
		CloseReason:        c.CloseReason,
	}
	if c.LastInboundAt != nil {
		card.AwaitingReply = c.LastMessageAt != nil && !c.LastInboundAt.Before(*c.LastMessageAt)
		expires := c.LastInboundAt.Add(24 * time.Hour)
		card.WindowExpiresAt = &expires
	}
	if c.AssignedUser != nil {
		card.AssignedUserName = c.AssignedUser.FullName
	}
	return card
}
