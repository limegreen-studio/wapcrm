package handlers

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/crypto"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// LeadSourcesSettingsKey is the Organization.Settings key for lead sources.
const LeadSourcesSettingsKey = "lead_sources"

// leadSources is the stored configuration. Secrets are encrypted at rest and
// never returned by the API. UpdatedByID is the user public leads are
// recorded under.
type leadSources struct {
	Token             string     `json:"token"`
	TeamID            *uuid.UUID `json:"team_id,omitempty"`
	UpdatedByID       uuid.UUID  `json:"updated_by_id"`
	FacebookPageToken string     `json:"facebook_page_token,omitempty"`
	FacebookAppSecret string     `json:"facebook_app_secret,omitempty"`
}

// LeadSourcesResponse is what admins see of the lead source settings.
type LeadSourcesResponse struct {
	Token         string     `json:"token"`
	TeamID        *uuid.UUID `json:"team_id,omitempty"`
	FacebookReady bool       `json:"facebook_ready"`
}

// UpdateLeadSourcesRequest changes lead source settings. Empty secrets are
// left unchanged.
type UpdateLeadSourcesRequest struct {
	TeamID            *uuid.UUID `json:"team_id"`
	FacebookPageToken string     `json:"facebook_page_token"`
	FacebookAppSecret string     `json:"facebook_app_secret"`
}

func (a *App) loadLeadSources(orgID uuid.UUID) (leadSources, error) {
	var org models.Organization
	var cfg leadSources
	if err := a.DB.Select("id", "settings").Where("id = ?", orgID).First(&org).Error; err != nil {
		return cfg, err
	}
	raw, ok := org.Settings[LeadSourcesSettingsKey]
	if !ok {
		return cfg, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(b, &cfg)
}

func newLeadCaptureToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (cfg leadSources) response() LeadSourcesResponse {
	return LeadSourcesResponse{
		Token:         cfg.Token,
		TeamID:        cfg.TeamID,
		FacebookReady: cfg.FacebookPageToken != "" && cfg.FacebookAppSecret != "",
	}
}

// GetLeadSources returns the settings, creating the capture token on first use.
func (a *App) GetLeadSources(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	cfg, err := a.loadLeadSources(orgID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load lead sources", nil, "")
	}
	if cfg.Token == "" {
		if cfg.Token, err = newLeadCaptureToken(); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create token", nil, "")
		}
		cfg.UpdatedByID = userID
		if err := a.setOrgSetting(orgID, LeadSourcesSettingsKey, cfg); err != nil {
			a.Log.Error("Failed to save lead sources", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save lead sources", nil, "")
		}
	}
	return r.SendEnvelope(cfg.response())
}

// UpdateLeadSources saves the team and Facebook credentials.
func (a *App) UpdateLeadSources(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req UpdateLeadSourcesRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.TeamID != nil {
		var count int64
		a.DB.Model(&models.Team{}).Where("id = ? AND organization_id = ?", *req.TeamID, orgID).Count(&count)
		if count == 0 {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "team_id not found", nil, "")
		}
	}
	cfg, err := a.loadLeadSources(orgID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load lead sources", nil, "")
	}
	cfg.TeamID = req.TeamID
	cfg.UpdatedByID = userID
	for _, field := range []struct {
		value  string
		target *string
	}{{req.FacebookPageToken, &cfg.FacebookPageToken}, {req.FacebookAppSecret, &cfg.FacebookAppSecret}} {
		if field.value == "" {
			continue
		}
		enc, err := crypto.Encrypt(field.value, a.Config.App.EncryptionKey)
		if err != nil {
			a.Log.Error("Failed to encrypt lead source secret", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save lead sources", nil, "")
		}
		*field.target = enc
	}
	if cfg.Token == "" {
		if cfg.Token, err = newLeadCaptureToken(); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create token", nil, "")
		}
	}
	if err := a.setOrgSetting(orgID, LeadSourcesSettingsKey, cfg); err != nil {
		a.Log.Error("Failed to save lead sources", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save lead sources", nil, "")
	}
	return r.SendEnvelope(cfg.response())
}

// RotateLeadCaptureToken replaces the token; old form snippets stop working.
func (a *App) RotateLeadCaptureToken(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	cfg, err := a.loadLeadSources(orgID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load lead sources", nil, "")
	}
	if cfg.Token, err = newLeadCaptureToken(); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create token", nil, "")
	}
	cfg.UpdatedByID = userID
	if err := a.setOrgSetting(orgID, LeadSourcesSettingsKey, cfg); err != nil {
		a.Log.Error("Failed to save lead sources", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save lead sources", nil, "")
	}
	return r.SendEnvelope(cfg.response())
}

// findOrgByCaptureToken resolves the {token} path param to an org and config.
func (a *App) findOrgByCaptureToken(token string) (uuid.UUID, leadSources, bool) {
	var cfg leadSources
	if len(token) != 48 {
		return uuid.Nil, cfg, false
	}
	var org models.Organization
	if err := a.DB.Select("id", "settings").
		Where("settings->'"+LeadSourcesSettingsKey+"'->>'token' = ?", token).
		First(&org).Error; err != nil {
		return uuid.Nil, cfg, false
	}
	cfg, err := a.loadLeadSources(org.ID)
	if err != nil || subtle.ConstantTimeCompare([]byte(cfg.Token), []byte(token)) != 1 {
		return uuid.Nil, cfg, false
	}
	return org.ID, cfg, true
}

// publicLeadFields reads a form or JSON body into a flat field map.
func publicLeadFields(r *fastglue.Request) (map[string]string, bool) {
	fields := map[string]string{}
	contentType := string(r.RequestCtx.Request.Header.ContentType())
	if strings.HasPrefix(contentType, "application/json") {
		var body map[string]any
		if err := json.Unmarshal(r.RequestCtx.PostBody(), &body); err != nil {
			return nil, false
		}
		for k, v := range body {
			switch val := v.(type) {
			case string:
				fields[k] = val
			case float64, bool:
				fields[k] = fmt.Sprint(val)
			}
		}
		return fields, true
	}
	r.RequestCtx.PostArgs().VisitAll(func(k, v []byte) {
		fields[string(k)] = string(v)
	})
	return fields, true
}

// PublicCaptureLead takes a lead from a website form or Google Forms script.
// The URL token identifies the org. Callers can only supply contact and lead
// fields: pipeline, stage and owner come from the org's settings. An existing
// contact keeps its stage and owner.
func (a *App) PublicCaptureLead(r *fastglue.Request) error {
	r.RequestCtx.Response.Header.Set("Access-Control-Allow-Origin", "*")
	r.RequestCtx.Response.Header.Del("Access-Control-Allow-Credentials")

	orgID, cfg, ok := a.findOrgByCaptureToken(r.RequestCtx.UserValue("token").(string))
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Not found", nil, "")
	}
	fields, ok := publicLeadFields(r)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid body", nil, "")
	}
	// Hidden field that only bots fill in. Answer as if it worked.
	if fields["company_website"] != "" {
		return r.SendEnvelope(map[string]any{"ok": true})
	}

	req := CaptureLeadRequest{
		PhoneNumber: fields["phone_number"],
		Name:        fields["name"],
		TeamID:      cfg.TeamID,
		Lead:        map[string]string{},
	}
	if req.PhoneNumber == "" {
		req.PhoneNumber = fields["phone"]
	}
	if note := strings.TrimSpace(fields["message"]); note != "" {
		if len(note) > 2000 {
			note = note[:2000]
		}
		req.Note = note
	}
	known := map[string]bool{}
	for _, f := range a.leadFields(orgID) {
		known[f.Key] = true
	}
	for k, v := range fields {
		if key, isLead := strings.CutPrefix(k, "lead_"); isLead && known[key] {
			req.Lead[key] = v
		}
	}
	if known["source"] && req.Lead["source"] == "" {
		req.Lead["source"] = "website"
	}

	resp, status, msg := a.captureLead(orgID, cfg.UpdatedByID, req)
	if resp == nil {
		return r.SendErrorEnvelope(status, msg, nil, "")
	}
	// Do not reveal whether the number was already known
	return r.SendEnvelope(map[string]any{"ok": true})
}

// FacebookLeadVerify answers Meta's webhook handshake. The verify token is the
// capture token already in the callback URL.
func (a *App) FacebookLeadVerify(r *fastglue.Request) error {
	token := r.RequestCtx.UserValue("token").(string)
	args := r.RequestCtx.QueryArgs()
	if _, _, ok := a.findOrgByCaptureToken(token); !ok ||
		string(args.Peek("hub.mode")) != "subscribe" || string(args.Peek("hub.verify_token")) != token {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Verification failed", nil, "")
	}
	r.RequestCtx.SetStatusCode(fasthttp.StatusOK)
	r.RequestCtx.SetBodyString(string(args.Peek("hub.challenge")))
	return nil
}

type facebookLeadEvent struct {
	Entry []struct {
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				LeadgenID string `json:"leadgen_id"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// FacebookLeadWebhook receives leadgen events, fetches each lead from the
// Graph API and captures it. The signature check is mandatory here.
func (a *App) FacebookLeadWebhook(r *fastglue.Request) error {
	orgID, cfg, ok := a.findOrgByCaptureToken(r.RequestCtx.UserValue("token").(string))
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Not found", nil, "")
	}
	if cfg.FacebookPageToken == "" || cfg.FacebookAppSecret == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Facebook Lead Ads is not configured", nil, "")
	}
	secret, err := crypto.Decrypt(cfg.FacebookAppSecret, a.Config.App.EncryptionKey)
	if err != nil {
		a.Log.Error("Failed to decrypt Facebook app secret", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Configuration error", nil, "")
	}
	body := r.RequestCtx.PostBody()
	if !verifyWebhookSignature(body, r.RequestCtx.Request.Header.Peek("X-Hub-Signature-256"), []byte(secret)) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Invalid signature", nil, "")
	}
	pageToken, err := crypto.Decrypt(cfg.FacebookPageToken, a.Config.App.EncryptionKey)
	if err != nil {
		a.Log.Error("Failed to decrypt Facebook page token", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Configuration error", nil, "")
	}

	var event facebookLeadEvent
	if err := json.Unmarshal(bytes.Clone(body), &event); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid body", nil, "")
	}
	for _, entry := range event.Entry {
		for _, change := range entry.Changes {
			if change.Field != "leadgen" || change.Value.LeadgenID == "" {
				continue
			}
			if err := a.captureFacebookLead(orgID, cfg, pageToken, change.Value.LeadgenID); err != nil {
				a.Log.Error("Failed to capture Facebook lead", "error", err, "leadgen_id", change.Value.LeadgenID)
			}
		}
	}
	return r.SendEnvelope(map[string]any{"ok": true})
}

// captureFacebookLead fetches one lead and runs it through the capture path.
func (a *App) captureFacebookLead(orgID uuid.UUID, cfg leadSources, pageToken, leadgenID string) error {
	endpoint := fmt.Sprintf("%s/%s/%s?fields=field_data",
		strings.TrimRight(a.Config.WhatsApp.BaseURL, "/"), a.Config.WhatsApp.APIVersion, url.PathEscape(leadgenID))
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+pageToken)
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graph API returned %d", resp.StatusCode)
	}
	var lead struct {
		FieldData []struct {
			Name   string   `json:"name"`
			Values []string `json:"values"`
		} `json:"field_data"`
	}
	if err := json.Unmarshal(raw, &lead); err != nil {
		return err
	}

	capture := CaptureLeadRequest{TeamID: cfg.TeamID, Lead: map[string]string{}}
	known := map[string]bool{}
	for _, f := range a.leadFields(orgID) {
		known[f.Key] = true
	}
	for _, f := range lead.FieldData {
		if len(f.Values) == 0 {
			continue
		}
		switch f.Name {
		case "phone_number", "phone":
			capture.PhoneNumber = f.Values[0]
		case "full_name", "name":
			capture.Name = f.Values[0]
		default:
			if known[f.Name] {
				capture.Lead[f.Name] = f.Values[0]
			}
		}
	}
	if known["source"] {
		capture.Lead["source"] = "facebook"
	}
	if res, _, msg := a.captureLead(orgID, cfg.UpdatedByID, capture); res == nil {
		return fmt.Errorf("capture rejected: %s", msg)
	}
	return nil
}
