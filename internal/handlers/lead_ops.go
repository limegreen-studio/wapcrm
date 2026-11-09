package handlers

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// maxBulkContacts caps one bulk request.
const maxBulkContacts = 200

// maxDealValue is below the numeric(14,2) column limit.
const maxDealValue = 999999999999.0

// jsonStringArray encodes one string as a JSON array for a jsonb @> match.
func jsonStringArray(s string) string {
	b, _ := json.Marshal([]string{s})
	return string(b)
}

// BulkContactsRequest applies the given changes to many contacts. Unset fields
// are left alone. Contacts the user cannot see are skipped.
type BulkContactsRequest struct {
	ContactIDs     []uuid.UUID `json:"contact_ids"`
	StageID        *uuid.UUID  `json:"stage_id"`
	AssignedUserID *uuid.UUID  `json:"assigned_user_id"`
	ClearAssignee  bool        `json:"clear_assignee"`
	AddTags        []string    `json:"add_tags"`
}

// BulkUpdateContacts moves, assigns or tags many contacts in one request.
// Assignment needs contacts:write, same as the single-contact assign.
func (a *App) BulkUpdateContacts(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req BulkContactsRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if len(req.ContactIDs) == 0 || len(req.ContactIDs) > maxBulkContacts {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			fmt.Sprintf("contact_ids must have between 1 and %d items", maxBulkContacts), nil, "")
	}
	if req.StageID == nil && req.AssignedUserID == nil && !req.ClearAssignee && len(req.AddTags) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "No changes requested", nil, "")
	}

	assigning := req.AssignedUserID != nil || req.ClearAssignee
	if assigning && !a.HasPermission(userID, models.ResourceContacts, models.ActionWrite, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You do not have permission to assign contacts", nil, "")
	}
	if req.AssignedUserID != nil && !a.isOrgMember(*req.AssignedUserID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Assignee not found", nil, "")
	}

	var stage *models.PipelineStage
	if req.StageID != nil {
		var s models.PipelineStage
		if err := a.DB.Where("id = ? AND organization_id = ?", *req.StageID, orgID).First(&s).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Stage not found", nil, "")
		}
		stage = &s
	}

	var contacts []models.Contact
	q := a.DB.Where("id IN ? AND organization_id = ?", req.ContactIDs, orgID)
	if err := a.scopeAssignedContact(q, userID, orgID).Find(&contacts).Error; err != nil {
		a.Log.Error("Failed to load contacts for bulk update", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update contacts", nil, "")
	}

	updated := 0
	for i := range contacts {
		contact := &contacts[i]
		// Read before Updates, which writes the new tags back onto the struct
		added := addedTags(contact.Tags, req.AddTags)
		updates := map[string]any{}
		if req.ClearAssignee {
			updates["assigned_user_id"] = nil
		} else if req.AssignedUserID != nil {
			updates["assigned_user_id"] = *req.AssignedUserID
		}
		if merged, changed := mergeTags(contact.Tags, req.AddTags); changed {
			updates["tags"] = merged
		}
		if len(updates) > 0 {
			if err := a.DB.Model(contact).Updates(updates).Error; err != nil {
				a.Log.Error("Bulk update failed", "error", err, "contact_id", contact.ID)
				continue
			}
		}
		if stage != nil && (contact.StageID == nil || *contact.StageID != stage.ID) {
			var from *models.PipelineStage
			if contact.StageID != nil {
				var s models.PipelineStage
				if a.DB.Where("id = ?", *contact.StageID).First(&s).Error == nil {
					from = &s
				}
			}
			if err := a.placeOnStage(contact.ID, orgID, stage); err != nil {
				a.Log.Error("Bulk stage move failed", "error", err, "contact_id", contact.ID)
				continue
			}
			contact.PipelineID, contact.StageID = &stage.PipelineID, &stage.ID
			a.recordStageChange(orgID, userID, contact, from, stage)
			a.fireAutomations(orgID, automationEvent{
				Trigger: models.AutomationTriggerStageChanged, ContactID: contact.ID,
				PipelineID: &stage.PipelineID, StageID: &stage.ID,
			})
		}
		for _, tag := range added {
			a.fireAutomations(orgID, automationEvent{
				Trigger: models.AutomationTriggerTagAdded, ContactID: contact.ID,
				PipelineID: contact.PipelineID, StageID: contact.StageID, Tag: tag,
			})
		}
		a.broadcastStageChanged(orgID, contact)
		updated++
	}

	return r.SendEnvelope(map[string]any{"updated": updated, "skipped": len(req.ContactIDs) - updated})
}

// addedTags returns the requested tags that were not already on the contact.
func addedTags(existing models.JSONBArray, requested []string) []string {
	have := make(map[string]bool, len(existing))
	for _, t := range existing {
		if s, ok := t.(string); ok {
			have[s] = true
		}
	}
	var added []string
	for _, t := range requested {
		t = strings.TrimSpace(t)
		if t != "" && !have[t] {
			have[t] = true
			added = append(added, t)
		}
	}
	return added
}

// ListLeadQueue returns open-stage leads nobody owns, oldest first, so agents
// can pick them up. Message previews stay hidden from users who cannot read
// all contacts until they claim the lead.
func (a *App) ListLeadQueue(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	pg := parsePagination(r)

	q := a.DB.Model(&models.Contact{}).
		Joins("JOIN pipeline_stages ps ON ps.id = contacts.stage_id AND ps.kind = ?", models.StageKindOpen).
		Where("contacts.organization_id = ? AND contacts.assigned_user_id IS NULL", orgID)

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		a.Log.Error("Failed to count lead queue", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load lead queue", nil, "")
	}
	var contacts []models.Contact
	if err := q.Order("contacts.created_at ASC").Offset(pg.Offset).Limit(pg.Limit).Find(&contacts).Error; err != nil {
		a.Log.Error("Failed to load lead queue", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load lead queue", nil, "")
	}

	shouldMask := a.ShouldMaskPhoneNumbers(orgID)
	canReadAll := a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID)
	cards := make([]BoardCard, len(contacts))
	for i := range contacts {
		cards[i] = contactToBoardCard(&contacts[i], shouldMask)
		if !canReadAll {
			cards[i].LastMessagePreview = ""
		}
	}
	return r.SendEnvelope(listEnvelope("leads", cards, total, pg))
}

// ClaimContact assigns an unowned lead to the caller. The conditional UPDATE
// means two agents claiming at once cannot both win.
func (a *App) ClaimContact(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}

	res := a.DB.Model(&models.Contact{}).
		Where("id = ? AND organization_id = ? AND assigned_user_id IS NULL", contactID, orgID).
		Update("assigned_user_id", userID)
	if res.Error != nil {
		a.Log.Error("Failed to claim contact", "error", res.Error)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to claim lead", nil, "")
	}
	if res.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "This lead has already been claimed", nil, "")
	}

	var contact models.Contact
	if err := a.DB.Preload("AssignedUser").Where("id = ?", contactID).First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load contact", nil, "")
	}
	a.addLeadNote(orgID, userID, contactID, "Lead claimed from the queue")
	a.broadcastStageChanged(orgID, &contact)
	return r.SendEnvelope(a.boardCard(&contact, orgID))
}

// addLeadNote writes a conversation note and shows it in open chats.
func (a *App) addLeadNote(orgID, userID, contactID uuid.UUID, content string) {
	note := models.ConversationNote{
		OrganizationID: orgID,
		ContactID:      contactID,
		CreatedByID:    userID,
		Content:        content,
	}
	if err := a.DB.Create(&note).Error; err != nil {
		a.Log.Error("Failed to create note", "error", err, "contact_id", contactID)
		return
	}
	if a.WSHub != nil {
		var user models.User
		a.DB.First(&user, "id = ?", userID)
		note.CreatedBy = &user
		a.WSHub.BroadcastToContact(orgID, contactID, websocket.WSMessage{
			Type:    websocket.TypeConversationNoteCreated,
			Payload: noteToResponse(note),
		})
	}
}

// ContactDealRequest sets a lead's deal value and close reason.
type ContactDealRequest struct {
	DealValue   *float64 `json:"deal_value"`
	CloseReason *string  `json:"close_reason"`
}

// UpdateContactDeal edits deal value and won/lost reason (chat:write, scoped).
func (a *App) UpdateContactDeal(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}
	var req ContactDealRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	updates := map[string]any{}
	if req.DealValue != nil {
		v := *req.DealValue
		if math.IsNaN(v) || v < 0 || v > maxDealValue {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "deal_value must be between 0 and 999999999999", nil, "")
		}
		updates["deal_value"] = math.Round(v*100) / 100
	}
	if req.CloseReason != nil {
		reason := strings.TrimSpace(*req.CloseReason)
		if len(reason) > 255 {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "close_reason must be at most 255 characters", nil, "")
		}
		updates["close_reason"] = reason
	}
	if len(updates) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "No fields to update", nil, "")
	}
	if !a.contactVisible(contactID, userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}
	if err := a.DB.Model(&models.Contact{}).Where("id = ?", contactID).Updates(updates).Error; err != nil {
		a.Log.Error("Failed to update deal", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update deal", nil, "")
	}
	var contact models.Contact
	if err := a.DB.Preload("AssignedUser").Where("id = ?", contactID).First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load contact", nil, "")
	}
	// A value entered after closing belongs to that closed deal
	if err := a.DB.Exec(`UPDATE stage_history SET deal_value = ?, close_reason = ?
		WHERE id = (SELECT id FROM stage_history
		            WHERE contact_id = ? AND deal_seq = ? AND to_stage_kind IN ('won', 'lost')
		            ORDER BY changed_at DESC LIMIT 1)`,
		contact.DealValue, contact.CloseReason, contact.ID, contact.DealSeq).Error; err != nil {
		a.Log.Error("Failed to update closed deal", "error", err, "contact_id", contact.ID)
	}
	a.broadcastStageChanged(orgID, &contact)
	return r.SendEnvelope(a.boardCard(&contact, orgID))
}

// DuplicateGroup is a set of contacts whose numbers end in the same 10 digits.
type DuplicateGroup struct {
	Key      string      `json:"key"`
	Contacts []BoardCard `json:"contacts"`
}

const duplicateKeyExpr = `RIGHT(REGEXP_REPLACE(phone_number, '[^0-9]', '', 'g'), 10)`

// ListDuplicateContacts finds likely duplicates: numbers that differ only by
// country code or formatting. A person reviews each group before merging.
func (a *App) ListDuplicateContacts(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceContacts, models.ActionWrite)
	if err != nil {
		return nil
	}

	var keys []string
	if err := a.DB.Model(&models.Contact{}).
		Where("organization_id = ? AND LENGTH(REGEXP_REPLACE(phone_number, '[^0-9]', '', 'g')) >= 10", orgID).
		Group(duplicateKeyExpr).Having("COUNT(*) > 1").
		Order(duplicateKeyExpr).Limit(50).
		Pluck(duplicateKeyExpr, &keys).Error; err != nil {
		a.Log.Error("Failed to find duplicates", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to find duplicates", nil, "")
	}

	groups := make([]DuplicateGroup, 0, len(keys))
	shouldMask := a.ShouldMaskPhoneNumbers(orgID)
	for _, key := range keys {
		var contacts []models.Contact
		if err := a.DB.Preload("AssignedUser").
			Where("organization_id = ? AND "+duplicateKeyExpr+" = ?", orgID, key).
			Order("created_at ASC").Find(&contacts).Error; err != nil {
			a.Log.Error("Failed to load duplicates", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to find duplicates", nil, "")
		}
		group := DuplicateGroup{Key: key, Contacts: make([]BoardCard, len(contacts))}
		for i := range contacts {
			group.Contacts[i] = contactToBoardCard(&contacts[i], shouldMask)
		}
		groups = append(groups, group)
	}
	return r.SendEnvelope(map[string]any{"groups": groups})
}

// MergeContactsRequest folds DuplicateID into PrimaryID.
type MergeContactsRequest struct {
	PrimaryID   uuid.UUID `json:"primary_id"`
	DuplicateID uuid.UUID `json:"duplicate_id"`
}

// MergeContacts moves everything linked to the duplicate onto the primary and
// soft-deletes the duplicate. The primary keeps the longer number, because
// WhatsApp sends the full international form and must match the surviving
// contact. Tables are found by their contact_id column so new ones are covered.
func (a *App) MergeContacts(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceContacts, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req MergeContactsRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.PrimaryID == req.DuplicateID {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Choose two different contacts", nil, "")
	}

	var primary, duplicate models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", req.PrimaryID, orgID).First(&primary).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}
	if err := a.DB.Where("id = ? AND organization_id = ?", req.DuplicateID, orgID).First(&duplicate).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var tables []string
		if err := tx.Raw(`SELECT table_name FROM information_schema.columns
			WHERE column_name = 'contact_id' AND table_schema = current_schema()`).Scan(&tables).Error; err != nil {
			return err
		}
		for _, table := range tables {
			if err := tx.Exec(`UPDATE "`+table+`" SET contact_id = ? WHERE contact_id = ?`, primary.ID, duplicate.ID).Error; err != nil {
				return fmt.Errorf("move %s: %w", table, err)
			}
		}

		// Free the number first: phone is unique per org, including soft-deleted rows
		if err := tx.Model(&models.Contact{}).Where("id = ?", duplicate.ID).
			Update("phone_number", "merged:"+duplicate.ID.String()).Error; err != nil {
			return err
		}

		updates := map[string]any{}
		if len(normalizeLeadPhone(duplicate.PhoneNumber)) > len(normalizeLeadPhone(primary.PhoneNumber)) {
			updates["phone_number"] = duplicate.PhoneNumber
		}
		if primary.ProfileName == "" && duplicate.ProfileName != "" {
			updates["profile_name"] = duplicate.ProfileName
		}
		if primary.WhatsAppAccount == "" && duplicate.WhatsAppAccount != "" {
			updates["whats_app_account"] = duplicate.WhatsAppAccount
		}
		if primary.AssignedUserID == nil && duplicate.AssignedUserID != nil {
			updates["assigned_user_id"] = *duplicate.AssignedUserID
		}
		if primary.BSUID == "" && duplicate.BSUID != "" {
			updates["bs_uid"] = duplicate.BSUID
		}
		if primary.DealValue == 0 && duplicate.DealValue != 0 {
			updates["deal_value"] = duplicate.DealValue
		}
		if primary.CloseReason == "" && duplicate.CloseReason != "" {
			updates["close_reason"] = duplicate.CloseReason
		}
		if primary.StageID == nil && duplicate.StageID != nil {
			updates["pipeline_id"] = duplicate.PipelineID
			updates["stage_id"] = duplicate.StageID
			updates["stage_entered_at"] = duplicate.StageEnteredAt
			updates["pipeline_position"] = duplicate.PipelinePosition
		}
		if duplicate.LastMessageAt != nil && (primary.LastMessageAt == nil || duplicate.LastMessageAt.After(*primary.LastMessageAt)) {
			updates["last_message_at"] = duplicate.LastMessageAt
			updates["last_message_preview"] = duplicate.LastMessagePreview
		}
		if duplicate.LastInboundAt != nil && (primary.LastInboundAt == nil || duplicate.LastInboundAt.After(*primary.LastInboundAt)) {
			updates["last_inbound_at"] = duplicate.LastInboundAt
		}
		tags := make([]string, 0, len(duplicate.Tags))
		for _, t := range duplicate.Tags {
			if s, ok := t.(string); ok {
				tags = append(tags, s)
			}
		}
		if merged, changed := mergeTags(primary.Tags, tags); changed {
			updates["tags"] = merged
		}
		if metadata, changed := mergeContactMetadata(primary.Metadata, duplicate.Metadata); changed {
			updates["metadata"] = metadata
		}
		if len(updates) > 0 {
			if err := tx.Model(&models.Contact{}).Where("id = ?", primary.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&models.Contact{}, "id = ?", duplicate.ID).Error
	})
	if err != nil {
		a.Log.Error("Failed to merge contacts", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to merge contacts", nil, "")
	}

	a.addLeadNote(orgID, userID, primary.ID, "Merged duplicate contact "+duplicate.PhoneNumber)
	var merged models.Contact
	if err := a.DB.Preload("AssignedUser").Where("id = ?", primary.ID).First(&merged).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load contact", nil, "")
	}
	a.broadcastStageChanged(orgID, &merged)
	return r.SendEnvelope(a.boardCard(&merged, orgID))
}

// mergeContactMetadata fills keys missing from primary, including inside the
// lead namespace. Values already on the primary win.
func mergeContactMetadata(primary, duplicate models.JSONB) (models.JSONB, bool) {
	out := models.JSONB{}
	for k, v := range primary {
		out[k] = v
	}
	changed := false
	for k, v := range duplicate {
		existing, ok := out[k]
		if !ok {
			out[k] = v
			changed = true
			continue
		}
		if k != models.LeadMetadataKey {
			continue
		}
		have, ok1 := existing.(map[string]any)
		add, ok2 := v.(map[string]any)
		if !ok1 || !ok2 {
			continue
		}
		lead := make(map[string]any, len(have)+len(add))
		for lk, lv := range have {
			lead[lk] = lv
		}
		for lk, lv := range add {
			if _, exists := lead[lk]; !exists {
				lead[lk] = lv
				changed = true
			}
		}
		out[k] = lead
	}
	return out, changed
}

// StageHistoryResponse is one row of a contact's stage history.
type StageHistoryResponse struct {
	FromStageName string    `json:"from_stage_name"`
	ToStageName   string    `json:"to_stage_name"`
	ChangedByName string    `json:"changed_by_name"`
	ChangedAt     time.Time `json:"changed_at"`
}

// GetContactStageHistory returns a contact's stage moves, newest first.
func (a *App) GetContactStageHistory(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionRead)
	if err != nil {
		return nil
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}
	if !a.contactVisible(contactID, userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}
	rows := []StageHistoryResponse{}
	if err := a.DB.Table("stage_history sh").
		Select("sh.from_stage_name, sh.to_stage_name, sh.changed_at, COALESCE(u.full_name, '') AS changed_by_name").
		Joins("LEFT JOIN users u ON u.id = sh.changed_by_id").
		Where("sh.organization_id = ? AND sh.contact_id = ?", orgID, contactID).
		Order("sh.changed_at DESC").Limit(200).Scan(&rows).Error; err != nil {
		a.Log.Error("Failed to load stage history", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load stage history", nil, "")
	}
	return r.SendEnvelope(map[string]any{"history": rows})
}

// ReportPeriod is one time bucket of a pipeline report.
type ReportPeriod struct {
	Period time.Time `json:"period"`
	Leads  int64     `json:"leads"`
	Won    int64     `json:"won"`
	Lost   int64     `json:"lost"`
}

// ReportGroup is one agent's or source's row in a pipeline report.
type ReportGroup struct {
	Key      string  `json:"key"`
	Name     string  `json:"name"`
	Leads    int64   `json:"leads"`
	Won      int64   `json:"won"`
	Lost     int64   `json:"lost"`
	WonValue float64 `json:"won_value"`
}

// ReportStageTime is the average time leads spent on one stage.
type ReportStageTime struct {
	Stage      string  `json:"stage"`
	AvgSeconds float64 `json:"avg_seconds"`
	Moves      int64   `json:"moves"`
}

// PipelineReport is the response of GET /api/pipelines/{id}/report.
type PipelineReport struct {
	From       time.Time         `json:"from"`
	To         time.Time         `json:"to"`
	Group      string            `json:"group"`
	ByPeriod   []ReportPeriod    `json:"by_period"`
	ByAgent    []ReportGroup     `json:"by_agent"`
	BySource   []ReportGroup     `json:"by_source"`
	StageTimes []ReportStageTime `json:"stage_times"`
}

// GetPipelineReport reports conversion from stage history, scoped like the
// board. Query params: from, to (RFC3339; default last 30 days), group
// (day, week, month), format=csv.
func (a *App) GetPipelineReport(r *fastglue.Request) error {
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

	args := r.RequestCtx.QueryArgs()
	to := time.Now()
	from := to.AddDate(0, 0, -30)
	for param, target := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := string(args.Peek(param)); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid "+param+", expected RFC3339", nil, "")
			}
			*target = t
		}
	}
	group := string(args.Peek("group"))
	if group == "" {
		group = "day"
	}
	if group != "day" && group != "week" && group != "month" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "group must be day, week or month", nil, "")
	}

	visible := a.scopeAssignedContact(
		a.DB.Model(&models.Contact{}).Select("id").Where("organization_id = ?", orgID), userID, orgID)

	report := PipelineReport{From: from, To: to, Group: group,
		ByPeriod: []ReportPeriod{}, ByAgent: []ReportGroup{}, BySource: []ReportGroup{}, StageTimes: []ReportStageTime{}}

	// A deal counts by where it ended up: the last move of each deal. Moving a
	// won lead back and forth does not count twice, repeat deals each count.
	const finalMoves = `LEFT JOIN (
		SELECT DISTINCT ON (contact_id, deal_seq) id, to_stage_kind, deal_value
		FROM stage_history WHERE organization_id = ? AND pipeline_id = ?
		ORDER BY contact_id, deal_seq, changed_at DESC
	) f ON f.id = sh.id`

	// group is one of three fixed words, checked above
	err = a.DB.Table("stage_history sh").
		Joins(finalMoves, orgID, pipeline.ID).
		Select(`DATE_TRUNC('`+group+`', sh.changed_at) AS period,
			COUNT(*) FILTER (WHERE sh.from_stage_id IS NULL AND sh.to_stage_id IS NOT NULL) AS leads,
			COUNT(*) FILTER (WHERE f.to_stage_kind = 'won') AS won,
			COUNT(*) FILTER (WHERE f.to_stage_kind = 'lost') AS lost`).
		Where("sh.organization_id = ? AND sh.pipeline_id = ? AND sh.changed_at >= ? AND sh.changed_at < ?", orgID, pipeline.ID, from, to).
		Where("sh.contact_id IN (?)", visible).
		Group("period").Order("period").Scan(&report.ByPeriod).Error
	if err != nil {
		a.Log.Error("Failed to load report", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load report", nil, "")
	}

	// Closed in the period, by the lead's current owner and source
	closedGroup := func(keyExpr, nameExpr string, out *[]ReportGroup) error {
		return a.DB.Table("stage_history sh").
			Joins(finalMoves, orgID, pipeline.ID).
			Select(keyExpr+` AS key, `+nameExpr+` AS name,
				COUNT(DISTINCT sh.contact_id) AS leads,
				COUNT(*) FILTER (WHERE f.to_stage_kind = 'won') AS won,
				COUNT(*) FILTER (WHERE f.to_stage_kind = 'lost') AS lost,
				COALESCE(SUM(f.deal_value) FILTER (WHERE f.to_stage_kind = 'won'), 0) AS won_value`).
			Joins("JOIN contacts c ON c.id = sh.contact_id").
			Joins("LEFT JOIN users u ON u.id = c.assigned_user_id").
			Where("sh.organization_id = ? AND sh.pipeline_id = ? AND sh.changed_at >= ? AND sh.changed_at < ?", orgID, pipeline.ID, from, to).
			Where("sh.contact_id IN (?)", visible).
			Group(keyExpr + ", " + nameExpr).Order("won_value DESC, won DESC").Limit(100).Scan(out).Error
	}
	if err := closedGroup("COALESCE(c.assigned_user_id::text, '')", "COALESCE(u.full_name, '')", &report.ByAgent); err != nil {
		a.Log.Error("Failed to load agent report", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load report", nil, "")
	}
	sourceExpr := "COALESCE(c.metadata->'" + models.LeadMetadataKey + "'->>'source', '')"
	if err := closedGroup(sourceExpr, sourceExpr, &report.BySource); err != nil {
		a.Log.Error("Failed to load source report", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load report", nil, "")
	}

	err = a.DB.Raw(`
		SELECT stage, AVG(EXTRACT(EPOCH FROM next_at - changed_at)) AS avg_seconds, COUNT(*) AS moves
		FROM (
			SELECT sh.to_stage_name AS stage, sh.changed_at,
			       LEAD(sh.changed_at) OVER (PARTITION BY sh.contact_id ORDER BY sh.changed_at) AS next_at
			FROM stage_history sh
			WHERE sh.organization_id = ? AND sh.pipeline_id = ? AND sh.contact_id IN (?)
		) moves
		WHERE next_at IS NOT NULL AND stage <> '' AND next_at >= ? AND next_at < ?
		GROUP BY stage ORDER BY stage`, orgID, pipeline.ID, visible, from, to).Scan(&report.StageTimes).Error
	if err != nil {
		a.Log.Error("Failed to load stage times", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load report", nil, "")
	}

	if string(args.Peek("format")) == "csv" {
		return a.sendReportCSV(r, pipeline.Name, report)
	}
	return r.SendEnvelope(report)
}

// csvSafe stops spreadsheets from running a cell as a formula.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (a *App) sendReportCSV(r *fastglue.Request, pipelineName string, report PipelineReport) error {
	var out strings.Builder
	w := csv.NewWriter(&out)
	_ = w.Write([]string{"section", "name", "leads", "won", "lost", "won_value"})
	count := func(n int64) string { return strconv.FormatInt(n, 10) }
	for _, p := range report.ByPeriod {
		_ = w.Write([]string{report.Group, p.Period.Format("2006-01-02"), count(p.Leads), count(p.Won), count(p.Lost), ""})
	}
	groups := func(section string, rows []ReportGroup) {
		for _, g := range rows {
			_ = w.Write([]string{section, csvSafe(g.Name), count(g.Leads), count(g.Won), count(g.Lost), strconv.FormatFloat(g.WonValue, 'f', 2, 64)})
		}
	}
	groups("agent", report.ByAgent)
	groups("source", report.BySource)
	w.Flush()

	r.RequestCtx.SetContentType("text/csv; charset=utf-8")
	name := strings.Map(func(c rune) rune {
		if c == '"' || c == '\\' || c < 32 {
			return -1
		}
		return c
	}, pipelineName)
	r.RequestCtx.Response.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-report.csv"`, name))
	r.RequestCtx.SetBodyString(out.String())
	return nil
}

// CallOutcomeRequest records how a call with a lead went.
type CallOutcomeRequest struct {
	Outcome    string     `json:"outcome"`
	Notes      string     `json:"notes"`
	FollowUpAt *time.Time `json:"follow_up_at"`
}

var callOutcomeLabels = map[string]string{
	"connected":    "Connected",
	"no_answer":    "No answer",
	"busy":         "Busy",
	"callback":     "Asked to call back",
	"wrong_number": "Wrong number",
}

// LogCallOutcome writes a call outcome as a note, plus a follow-up when asked.
func (a *App) LogCallOutcome(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}
	var req CallOutcomeRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	label, ok := callOutcomeLabels[req.Outcome]
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "outcome must be connected, no_answer, busy, callback or wrong_number", nil, "")
	}
	if !a.contactVisible(contactID, userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	content := "Call: " + label
	if notes := strings.TrimSpace(req.Notes); notes != "" {
		content += "\n" + notes
	}
	a.addLeadNote(orgID, userID, contactID, content)

	var taskID *uuid.UUID
	if req.FollowUpAt != nil {
		task := models.Task{
			OrganizationID: orgID,
			ContactID:      contactID,
			AssigneeID:     &userID,
			CreatedByID:    userID,
			Title:          "Call back: " + strings.ToLower(label),
			DueAt:          req.FollowUpAt,
		}
		if err := a.DB.Create(&task).Error; err != nil {
			a.Log.Error("Failed to create call follow-up", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create follow-up", nil, "")
		}
		taskID = &task.ID
		a.broadcastTaskChanged(orgID, TaskResponse{ID: task.ID, ContactID: contactID}, false)
	}
	return r.SendEnvelope(map[string]any{"task_id": taskID})
}
