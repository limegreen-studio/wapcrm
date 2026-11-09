package handlers

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// UpdateLeadFieldsRequest sets lead fields on a contact. Only the given keys
// change; an empty string removes that field.
type UpdateLeadFieldsRequest struct {
	Fields map[string]string `json:"fields"`
}

// UpdateLeadFieldDefsRequest replaces the organization's lead field list.
type UpdateLeadFieldDefsRequest struct {
	Fields []models.LeadField `json:"fields"`
}

// ListLeadFields returns the organization's lead field definitions.
func (a *App) ListLeadFields(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceChat, models.ActionRead)
	if err != nil {
		return nil
	}
	return r.SendEnvelope(map[string]any{"fields": a.leadFields(orgID)})
}

// UpdateLeadFieldDefs replaces the organization's lead field list. Removing a
// field hides it; values already stored on contacts are kept, so adding the
// same key back shows them again.
func (a *App) UpdateLeadFieldDefs(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req UpdateLeadFieldDefsRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if err := models.ValidateLeadFields(req.Fields); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}

	if err := a.setOrgSetting(orgID, models.LeadFieldsSettingsKey, req.Fields); err != nil {
		a.Log.Error("Failed to save lead fields", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save lead fields", nil, "")
	}
	return r.SendEnvelope(map[string]any{"fields": a.leadFields(orgID)})
}

// leadFields returns the organization's lead fields (defaults when unset).
func (a *App) leadFields(orgID uuid.UUID) []models.LeadField {
	var org models.Organization
	if err := a.DB.Select("id", "settings").Where("id = ?", orgID).First(&org).Error; err != nil {
		return models.DefaultLeadFields()
	}
	return models.LeadFieldsFromSettings(org.Settings)
}

// setOrgSetting writes one key of Organization.Settings in a single statement,
// leaving other keys untouched.
func (a *App) setOrgSetting(orgID uuid.UUID, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return a.DB.Exec(`UPDATE organizations
		SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), ARRAY[?]::text[], ?::jsonb, true), updated_at = NOW()
		WHERE id = ?`, key, string(b), orgID).Error
}

// UpdateContactLeadFields merges lead fields into Contact.Metadata["lead"].
// It uses chat:write (not contacts:write) so agents can fill in the leads they
// work, scoped to contacts they can see.
func (a *App) UpdateContactLeadFields(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}

	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}

	var req UpdateLeadFieldsRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	set, remove, msg := normalizeLeadFields(req.Fields, a.leadFields(orgID))
	if msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}

	if !a.contactVisible(contactID, userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	if err := mergeLeadFields(a.DB, contactID, set, remove); err != nil {
		a.Log.Error("Failed to update lead fields", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update lead fields", nil, "")
	}

	var contact models.Contact
	if err := a.DB.Where("id = ?", contactID).First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load contact", nil, "")
	}

	if a.WSHub != nil {
		a.WSHub.BroadcastToContact(orgID, contactID, websocket.WSMessage{
			Type:    websocket.TypeContactLeadUpdated,
			Payload: map[string]any{"contact_id": contactID, "metadata": contact.Metadata},
		})
	}

	return r.SendEnvelope(map[string]any{
		"contact_id": contactID,
		"metadata":   contact.Metadata,
	})
}

// normalizeLeadFields trims values and splits them into keys to set and keys
// to remove. It returns an error message for unknown keys or long values.
func normalizeLeadFields(fields map[string]string, allowed []models.LeadField) (set map[string]string, remove []string, msg string) {
	if len(fields) == 0 {
		return nil, nil, "fields is required"
	}
	set = make(map[string]string, len(fields))
	for key, value := range fields {
		if !models.HasLeadField(allowed, key) {
			return nil, nil, "unknown lead field: " + key
		}
		value = strings.TrimSpace(value)
		if len(value) > models.LeadFieldMaxLength {
			return nil, nil, key + " must be at most 255 characters"
		}
		if value == "" {
			remove = append(remove, key)
		} else {
			set[key] = value
		}
	}
	return set, remove, ""
}

// mergeLeadFields updates metadata.lead in one statement so concurrent writes
// to other metadata keys (or other lead keys) are not lost.
func mergeLeadFields(db *gorm.DB, contactID uuid.UUID, set map[string]string, remove []string) error {
	if len(set) == 0 && len(remove) == 0 {
		return nil
	}
	setJSON, err := json.Marshal(set)
	if err != nil {
		return err
	}
	if remove == nil {
		remove = []string{}
	}
	removeJSON, err := json.Marshal(remove)
	if err != nil {
		return err
	}
	// jsonb - text[] removes keys; the array comes from a JSON list of strings.
	return db.Exec(`
		UPDATE contacts
		SET metadata = jsonb_set(
			COALESCE(metadata, '{}'::jsonb),
			'{`+models.LeadMetadataKey+`}',
			(CASE WHEN jsonb_typeof(metadata->'`+models.LeadMetadataKey+`') = 'object'
				THEN metadata->'`+models.LeadMetadataKey+`' ELSE '{}'::jsonb END || ?::jsonb)
				- ARRAY(SELECT jsonb_array_elements_text(?::jsonb)),
			true
		),
		updated_at = NOW()
		WHERE id = ?`, string(setJSON), string(removeJSON), contactID).Error
}
