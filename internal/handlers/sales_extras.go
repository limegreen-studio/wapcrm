package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/assignment"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	leadSLAMaxReassigns = 3
	leadSLABatch        = 100
	maxQuoteItems       = 50
)

var countryCodePattern = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)

func (a *App) loadSalesSettings(orgID uuid.UUID) models.SalesSettings {
	settings := models.SalesSettings{MonthlyTargets: map[string]float64{}}
	var org models.Organization
	if err := a.DB.Select("id", "settings").Where("id = ?", orgID).First(&org).Error; err != nil {
		return settings
	}
	raw, ok := org.Settings[models.SalesSettingsKey]
	if !ok {
		return settings
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return settings
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		return models.SalesSettings{MonthlyTargets: map[string]float64{}}
	}
	if settings.MonthlyTargets == nil {
		settings.MonthlyTargets = map[string]float64{}
	}
	return settings
}

// GetSalesSettings returns the organization's sales settings.
func (a *App) GetSalesSettings(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionRead)
	if err != nil {
		return nil
	}
	return r.SendEnvelope(a.loadSalesSettings(orgID))
}

// UpdateSalesSettings validates and saves the sales settings.
func (a *App) UpdateSalesSettings(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req models.SalesSettings
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	req.DefaultCountryCode = strings.TrimPrefix(strings.TrimSpace(req.DefaultCountryCode), "+")
	if req.DefaultCountryCode != "" && !countryCodePattern.MatchString(req.DefaultCountryCode) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "default_country_code must be 1 to 4 digits", nil, "")
	}
	req.CurrencySymbol = strings.TrimSpace(req.CurrencySymbol)
	if len(req.CurrencySymbol) > 8 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "currency_symbol is too long", nil, "")
	}
	for id, target := range req.MonthlyTargets {
		userID, err := uuid.Parse(id)
		if err != nil || !a.isOrgMember(userID, orgID) {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "monthly_targets has an unknown user", nil, "")
		}
		if math.IsNaN(target) || target < 0 || target > maxDealValue {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "monthly target is out of range", nil, "")
		}
	}

	previous := a.loadSalesSettings(orgID)
	if req.LeadSLA.Enabled {
		if req.LeadSLA.Minutes < 1 || req.LeadSLA.Minutes > 1440 {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "minutes must be between 1 and 1440", nil, "")
		}
		var teams int64
		if req.LeadSLA.TeamID != nil {
			a.DB.Model(&models.Team{}).
				Where("id = ? AND organization_id = ? AND assignment_strategy <> ?", *req.LeadSLA.TeamID, orgID, models.AssignmentStrategyManual).
				Count(&teams)
		}
		if teams == 0 {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "choose a team that assigns automatically", nil, "")
		}
		// Keep the start time while the rule stays on
		if previous.LeadSLA.Enabled && previous.LeadSLA.EnabledAt != nil {
			req.LeadSLA.EnabledAt = previous.LeadSLA.EnabledAt
		} else {
			now := time.Now()
			req.LeadSLA.EnabledAt = &now
		}
	} else {
		req.LeadSLA.EnabledAt = nil
	}

	if err := a.setOrgSetting(orgID, models.SalesSettingsKey, req); err != nil {
		a.Log.Error("Failed to save sales settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save settings", nil, "")
	}
	return r.SendEnvelope(req)
}

// normalizeOrgPhone keeps digits and adds the organization's country code to
// a national number. Numbers typed with "+" are already international. The
// code is applied only to 10-digit numbers (after dropping one trunk zero).
func (a *App) normalizeOrgPhone(orgID uuid.UUID, raw string) string {
	digits := normalizeLeadPhone(raw)
	if strings.HasPrefix(strings.TrimSpace(raw), "+") {
		return digits
	}
	code := a.loadSalesSettings(orgID).DefaultCountryCode
	if code == "" {
		return digits
	}
	national := strings.TrimPrefix(digits, "0")
	if len(national) == 10 {
		return code + national
	}
	return digits
}

// templateBlocked reports whether a business-started template must not go to
// this contact: they asked not to be contacted, or opted out of marketing.
func templateBlocked(contact *models.Contact, tpl *models.Template) bool {
	return contact.DoNotContact || (contact.MarketingOptOut && strings.EqualFold(tpl.Category, "MARKETING"))
}

// DoNotContactRequest sets or clears a lead's do-not-contact flag.
type DoNotContactRequest struct {
	DoNotContact bool `json:"do_not_contact"`
}

// UpdateDoNotContact marks a lead so that no template reaches them.
func (a *App) UpdateDoNotContact(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}
	var req DoNotContactRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if !a.contactVisible(contactID, userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}
	if err := a.DB.Model(&models.Contact{}).Where("id = ?", contactID).Update("do_not_contact", req.DoNotContact).Error; err != nil {
		a.Log.Error("Failed to update do-not-contact", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update contact", nil, "")
	}
	note := "Marked do not contact"
	if !req.DoNotContact {
		note = "Do not contact removed"
	}
	a.addLeadNote(orgID, userID, contactID, note)
	return r.SendEnvelope(map[string]any{"do_not_contact": req.DoNotContact})
}

// StartNewDeal reopens a won or lost lead as a fresh deal. The closed deal
// stays in stage_history, so repeat customers count every sale.
func (a *App) StartNewDeal(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
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
	var contact models.Contact
	if err := a.DB.Where("id = ?", contactID).First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}
	var current models.PipelineStage
	if contact.StageID == nil || a.DB.Where("id = ?", *contact.StageID).First(&current).Error != nil || current.Kind == models.StageKindOpen {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Only a won or lost lead can start a new deal", nil, "")
	}
	var first models.PipelineStage
	if err := a.DB.Where("pipeline_id = ? AND kind = ?", current.PipelineID, models.StageKindOpen).Order("position ASC").First(&first).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "This pipeline has no open stage", nil, "")
	}
	if err := a.DB.Model(&models.Contact{}).Where("id = ?", contactID).Updates(map[string]any{
		"deal_value": 0, "close_reason": "", "deal_seq": contact.DealSeq + 1,
		"no_reply_handled_at": nil, "no_reply_step": 0,
	}).Error; err != nil {
		a.Log.Error("Failed to start new deal", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to start new deal", nil, "")
	}
	if err := a.placeOnStage(contactID, orgID, &first); err != nil {
		a.Log.Error("Failed to place new deal", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to start new deal", nil, "")
	}
	contact.DealSeq++
	contact.DealValue, contact.CloseReason = 0, ""
	// No "from" stage: reports count a new deal as a new lead
	a.writeStageHistory(orgID, userID, &contact, nil, &first)
	a.addLeadNote(orgID, userID, contactID, "New deal started at stage "+first.Name)

	var updated models.Contact
	if err := a.DB.Preload("AssignedUser").Where("id = ?", contactID).First(&updated).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load contact", nil, "")
	}
	a.broadcastStageChanged(orgID, &updated)
	a.fireAutomations(orgID, automationEvent{
		Trigger: models.AutomationTriggerStageChanged, ContactID: contactID,
		PipelineID: &first.PipelineID, StageID: &first.ID,
	})
	return r.SendEnvelope(a.boardCard(&updated, orgID))
}

// ProcessLeadSLA reassigns new leads whose owner has not answered in time.
// Each lead moves at most leadSLAMaxReassigns times.
func (a *App) ProcessLeadSLA(now time.Time) {
	var orgs []models.Organization
	if err := a.DB.Select("id", "settings").
		Where("settings->'" + models.SalesSettingsKey + "'->'lead_sla'->>'enabled' = 'true'").
		Find(&orgs).Error; err != nil {
		a.Log.Error("Failed to load lead SLA settings", "error", err)
		return
	}
	for _, org := range orgs {
		sla := a.loadSalesSettings(org.ID).LeadSLA
		if !sla.Enabled || sla.TeamID == nil || sla.EnabledAt == nil || sla.Minutes < 1 {
			continue
		}
		if a.outsideBusinessHours(org.ID) {
			continue
		}
		a.processLeadSLAForOrg(org.ID, sla, now)
	}
}

// outsideBusinessHours uses the chatbot's hours when the organization set them.
func (a *App) outsideBusinessHours(orgID uuid.UUID) bool {
	settings, err := a.getChatbotSettingsCached(orgID, "")
	if err != nil || settings == nil || !settings.BusinessHours.Enabled || len(settings.BusinessHours.Hours) == 0 {
		return false
	}
	return !a.isWithinBusinessHours(settings.BusinessHours.Hours)
}

func (a *App) processLeadSLAForOrg(orgID uuid.UUID, sla models.LeadSLA, now time.Time) {
	// Spot new owners from any source (manual, round robin, claim) in one
	// place, restarting the clock and the reassign count.
	if err := a.DB.Exec(`
		UPDATE contacts SET sla_owner_id = assigned_user_id, sla_owner_since = ?, sla_reassign_count = 0
		WHERE organization_id = ? AND assigned_user_id IS NOT NULL AND deleted_at IS NULL
		  AND sla_owner_id IS DISTINCT FROM assigned_user_id
		  AND created_at >= ?`, now, orgID, *sla.EnabledAt).Error; err != nil {
		a.Log.Error("Failed to track lead owners", "error", err, "org_id", orgID)
		return
	}

	cutoff := now.Add(-time.Duration(sla.Minutes) * time.Minute)
	var contacts []models.Contact
	err := a.DB.Raw(`
		UPDATE contacts SET sla_owner_since = ?
		WHERE id IN (
			SELECT c.id FROM contacts c
			JOIN pipeline_stages ps ON ps.id = c.stage_id AND ps.kind = ?
			WHERE c.organization_id = ? AND c.deleted_at IS NULL
			  AND c.assigned_user_id IS NOT NULL
			  AND c.created_at >= ? AND c.sla_owner_since < ? AND c.sla_reassign_count < ?
			  AND NOT EXISTS (SELECT 1 FROM messages m
			                  WHERE m.contact_id = c.id AND m.direction = 'outgoing' AND m.sent_by_user_id IS NOT NULL)
			LIMIT ?
			FOR UPDATE OF c SKIP LOCKED
		)
		RETURNING *`, now, models.StageKindOpen, orgID, *sla.EnabledAt, cutoff, leadSLAMaxReassigns, leadSLABatch).Scan(&contacts).Error
	if err != nil {
		a.Log.Error("Failed to claim late leads", "error", err, "org_id", orgID)
		return
	}
	if a.Assigner == nil {
		return
	}
	for i := range contacts {
		contact := &contacts[i]
		previous := *contact.AssignedUserID
		owner := a.Assigner.AssignToTeam(*sla.TeamID, orgID, []uuid.UUID{previous}, assignment.LeadLoadCounter)
		if owner == nil || *owner == previous {
			continue
		}
		if err := a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).Updates(map[string]any{
			"assigned_user_id": *owner, "sla_owner_id": *owner, "sla_reassign_count": contact.SLAReassignCount + 1,
		}).Error; err != nil {
			a.Log.Error("Failed to reassign late lead", "error", err, "contact_id", contact.ID)
			continue
		}
		contact.AssignedUserID = owner
		a.addLeadNote(orgID, *owner, contact.ID, fmt.Sprintf("Reassigned: no response within %d minutes", sla.Minutes))
		a.broadcastStageChanged(orgID, contact)
		a.notifyUser(orgID, *owner, "New lead reassigned to you", contact.ProfileName, "/chat/"+contact.ID.String())
	}
}

// pushHosts are the browser push services the server may post to. The
// endpoint comes from the client, so an open URL would allow request forgery.
var pushHostSuffixes = []string{
	"fcm.googleapis.com", "push.apple.com", "push.services.mozilla.com", "notify.windows.com",
}

func validPushEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range pushHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// GetPushKey returns the public key browsers need in order to subscribe.
func (a *App) GetPushKey(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceChat, models.ActionRead); err != nil {
		return nil
	}
	return r.SendEnvelope(map[string]any{"public_key": a.Config.Push.VAPIDPublicKey})
}

// PushSubscribeRequest is what the browser's PushManager returns.
type PushSubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// SubscribePush stores this device for the current user.
func (a *App) SubscribePush(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionRead)
	if err != nil {
		return nil
	}
	var req PushSubscribeRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if !validPushEndpoint(req.Endpoint) || req.Keys.P256DH == "" || req.Keys.Auth == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid push subscription", nil, "")
	}
	// The endpoint identifies the device; a new login on it takes over
	if err := a.DB.Unscoped().Where("endpoint = ?", req.Endpoint).Delete(&models.PushSubscription{}).Error; err != nil {
		a.Log.Error("Failed to replace push subscription", "error", err)
	}
	sub := models.PushSubscription{OrganizationID: orgID, UserID: userID, Endpoint: req.Endpoint, P256DH: req.Keys.P256DH, Auth: req.Keys.Auth}
	if err := a.DB.Create(&sub).Error; err != nil {
		a.Log.Error("Failed to save push subscription", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save subscription", nil, "")
	}
	return r.SendEnvelope(map[string]any{"subscribed": true})
}

// notifyUser pushes to every device of a user. It is a no-op without VAPID keys.
func (a *App) notifyUser(orgID, userID uuid.UUID, title, body, link string) {
	if a.Config == nil || a.Config.Push.VAPIDPrivateKey == "" {
		return
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		var subs []models.PushSubscription
		if err := a.DB.Where("organization_id = ? AND user_id = ?", orgID, userID).Find(&subs).Error; err != nil {
			a.Log.Error("Failed to load push subscriptions", "error", err)
			return
		}
		payload, err := json.Marshal(map[string]string{"title": title, "body": body, "url": link})
		if err != nil {
			return
		}
		for i := range subs {
			sub := &subs[i]
			resp, err := webpush.SendNotification(payload, &webpush.Subscription{
				Endpoint: sub.Endpoint,
				Keys:     webpush.Keys{P256dh: sub.P256DH, Auth: sub.Auth},
			}, &webpush.Options{
				HTTPClient:      a.HTTPClient,
				Subscriber:      a.Config.Push.Subject,
				VAPIDPublicKey:  a.Config.Push.VAPIDPublicKey,
				VAPIDPrivateKey: a.Config.Push.VAPIDPrivateKey,
				TTL:             3600,
			})
			if err != nil {
				a.Log.Error("Failed to send push", "error", err, "user_id", userID)
				continue
			}
			_ = resp.Body.Close()
			// The browser unsubscribed or expired
			if resp.StatusCode == 404 || resp.StatusCode == 410 {
				a.DB.Unscoped().Delete(sub)
			}
		}
	}()
}

// TimelineEntry is one line of a lead's activity.
type TimelineEntry struct {
	Kind string    `json:"kind"` // note, stage, task, call, quote
	At   time.Time `json:"at"`
	Text string    `json:"text"`
	By   string    `json:"by,omitempty"`
}

const timelineLimit = 200

// GetContactTimeline merges a lead's notes, stage moves, follow-ups, calls and
// quotes, newest first.
func (a *App) GetContactTimeline(r *fastglue.Request) error {
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
	entries := []TimelineEntry{}

	var notes []models.ConversationNote
	a.DB.Preload("CreatedBy").Where("contact_id = ?", contactID).Order("created_at DESC").Limit(timelineLimit).Find(&notes)
	for i := range notes {
		e := TimelineEntry{Kind: "note", At: notes[i].CreatedAt, Text: notes[i].Content}
		if notes[i].CreatedBy != nil {
			e.By = notes[i].CreatedBy.FullName
		}
		entries = append(entries, e)
	}

	var tasks []models.Task
	a.DB.Preload("Assignee").Where("contact_id = ?", contactID).Order("created_at DESC").Limit(timelineLimit).Find(&tasks)
	for i := range tasks {
		by := ""
		if tasks[i].Assignee != nil {
			by = tasks[i].Assignee.FullName
		}
		entries = append(entries, TimelineEntry{Kind: "task", At: tasks[i].CreatedAt, Text: "Follow-up created: " + tasks[i].Title, By: by})
		if tasks[i].CompletedAt != nil {
			entries = append(entries, TimelineEntry{Kind: "task", At: *tasks[i].CompletedAt, Text: "Follow-up done: " + tasks[i].Title, By: by})
		}
	}

	var calls []models.CallLog
	a.DB.Where("contact_id = ?", contactID).Order("created_at DESC").Limit(timelineLimit).Find(&calls)
	for i := range calls {
		entries = append(entries, TimelineEntry{
			Kind: "call", At: calls[i].CreatedAt,
			Text: fmt.Sprintf("WhatsApp call (%s, %s, %ds)", calls[i].Direction, calls[i].Status, calls[i].Duration),
		})
	}

	var quotes []models.Quote
	a.DB.Where("contact_id = ?", contactID).Order("created_at DESC").Limit(timelineLimit).Find(&quotes)
	for i := range quotes {
		entries = append(entries, TimelineEntry{
			Kind: "quote", At: quotes[i].CreatedAt,
			Text: fmt.Sprintf("Quote %q: %.2f (%s)", quotes[i].Title, quotes[i].Total, quotes[i].Status),
		})
	}

	// Stage moves are already written as notes, which carry the same text
	var closed []models.StageHistory
	a.DB.Where("contact_id = ? AND to_stage_kind IN ?", contactID, []string{models.StageKindWon, models.StageKindLost}).
		Order("changed_at DESC").Limit(timelineLimit).Find(&closed)
	for i := range closed {
		text := fmt.Sprintf("Deal %s: %.2f", closed[i].ToStageKind, closed[i].DealValue)
		if closed[i].CloseReason != "" {
			text += " (" + closed[i].CloseReason + ")"
		}
		entries = append(entries, TimelineEntry{Kind: "stage", At: closed[i].ChangedAt, Text: text})
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })
	if len(entries) > timelineLimit {
		entries = entries[:timelineLimit]
	}
	return r.SendEnvelope(map[string]any{"entries": entries})
}

// QuoteRequest creates or edits a quote.
type QuoteRequest struct {
	Title      string             `json:"title"`
	Items      []models.QuoteItem `json:"items"`
	TaxPercent float64            `json:"tax_percent"`
	Notes      string             `json:"notes"`
	PaymentURL string             `json:"payment_url"`
}

// QuoteResponse is the API form of a quote.
type QuoteResponse struct {
	ID         uuid.UUID          `json:"id"`
	ContactID  uuid.UUID          `json:"contact_id"`
	Title      string             `json:"title"`
	Items      []models.QuoteItem `json:"items"`
	TaxPercent float64            `json:"tax_percent"`
	Total      float64            `json:"total"`
	Notes      string             `json:"notes"`
	PaymentURL string             `json:"payment_url"`
	Status     string             `json:"status"`
	Link       string             `json:"link"`
	CreatedAt  time.Time          `json:"created_at"`
}

// quoteBaseURL builds the public origin from the proxy headers.
func (a *App) quoteBaseURL(r *fastglue.Request) string {
	scheme := string(r.RequestCtx.Request.Header.Peek("X-Forwarded-Proto"))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	return scheme + "://" + string(r.RequestCtx.Host()) + strings.TrimRight(a.Config.Server.BasePath, "/")
}

func quoteItems(raw models.JSONBArray) []models.QuoteItem {
	items := []models.QuoteItem{}
	b, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(b, &items) != nil {
		return []models.QuoteItem{}
	}
	return items
}

func (a *App) quoteToResponse(r *fastglue.Request, q *models.Quote) QuoteResponse {
	return QuoteResponse{
		ID: q.ID, ContactID: q.ContactID, Title: q.Title, Items: quoteItems(q.Items),
		TaxPercent: q.TaxPercent, Total: q.Total, Notes: q.Notes, PaymentURL: q.PaymentURL, Status: q.Status,
		Link: a.quoteBaseURL(r) + "/api/public/quotes/" + q.Token, CreatedAt: q.CreatedAt,
	}
}

// validateQuote trims the request and returns its total, or an error message.
func validateQuote(req *QuoteRequest) (float64, string) {
	req.Title = strings.TrimSpace(req.Title)
	req.PaymentURL = strings.TrimSpace(req.PaymentURL)
	if req.Title == "" || len(req.Title) > 255 {
		return 0, "title is required and must be at most 255 characters"
	}
	if len(req.Items) == 0 || len(req.Items) > maxQuoteItems {
		return 0, fmt.Sprintf("a quote needs between 1 and %d items", maxQuoteItems)
	}
	if req.TaxPercent < 0 || req.TaxPercent > 100 {
		return 0, "tax_percent must be between 0 and 100"
	}
	// Shown as a button on a public page, so only payment-safe schemes
	if req.PaymentURL != "" {
		u, err := url.Parse(req.PaymentURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "upi") {
			return 0, "payment_url must start with https:// or upi://"
		}
	}
	subtotal := 0.0
	for i := range req.Items {
		item := &req.Items[i]
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" || len(item.Name) > 255 {
			return 0, "each item needs a name of at most 255 characters"
		}
		if item.Quantity <= 0 || item.Quantity > 1e6 || item.Price < 0 || item.Price > maxDealValue {
			return 0, "item quantity or price is out of range"
		}
		subtotal += item.Quantity * item.Price
	}
	total := math.Round(subtotal*(100+req.TaxPercent)) / 100
	if total > maxDealValue {
		return 0, "quote total is too large"
	}
	return total, ""
}

func encodeQuoteItems(items []models.QuoteItem) (models.JSONBArray, error) {
	b, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	var out models.JSONBArray
	return out, json.Unmarshal(b, &out)
}

// ListContactQuotes returns a lead's quotes.
func (a *App) ListContactQuotes(r *fastglue.Request) error {
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
	var quotes []models.Quote
	if err := a.DB.Where("organization_id = ? AND contact_id = ?", orgID, contactID).Order("created_at DESC").Find(&quotes).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load quotes", nil, "")
	}
	out := make([]QuoteResponse, len(quotes))
	for i := range quotes {
		out[i] = a.quoteToResponse(r, &quotes[i])
	}
	return r.SendEnvelope(map[string]any{"quotes": out})
}

// CreateQuote adds a draft quote to a lead.
func (a *App) CreateQuote(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
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
	var req QuoteRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	total, msg := validateQuote(&req)
	if msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	items, err := encodeQuoteItems(req.Items)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid items", nil, "")
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create quote", nil, "")
	}
	quote := models.Quote{
		OrganizationID: orgID, ContactID: contactID, Title: req.Title, Items: items, TaxPercent: req.TaxPercent,
		Total: total, Notes: req.Notes, PaymentURL: req.PaymentURL, Status: models.QuoteStatusDraft,
		Token: hex.EncodeToString(tokenBytes), CreatedByID: userID,
	}
	if err := a.DB.Create(&quote).Error; err != nil {
		a.Log.Error("Failed to create quote", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create quote", nil, "")
	}
	return r.SendEnvelope(a.quoteToResponse(r, &quote))
}

// findVisibleQuote loads the {id} quote if the user can see its lead.
func (a *App) findVisibleQuote(r *fastglue.Request, orgID, userID uuid.UUID) (*models.Quote, error) {
	id, err := parsePathUUID(r, "id", "quote")
	if err != nil {
		return nil, err
	}
	var quote models.Quote
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&quote).Error; err != nil || !a.contactVisible(quote.ContactID, userID, orgID) {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Quote not found", nil, "")
		return nil, errEnvelopeSent
	}
	return &quote, nil
}

// UpdateQuoteStatus records acceptance or payment by hand.
func (a *App) UpdateQuoteStatus(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	quote, err := a.findVisibleQuote(r, orgID, userID)
	if err != nil {
		return nil
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Status != models.QuoteStatusAccepted && req.Status != models.QuoteStatusPaid {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "status must be accepted or paid", nil, "")
	}
	if err := a.DB.Model(quote).Update("status", req.Status).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update quote", nil, "")
	}
	a.addLeadNote(orgID, userID, quote.ContactID, fmt.Sprintf("Quote %q marked %s", quote.Title, req.Status))
	return r.SendEnvelope(a.quoteToResponse(r, quote))
}

// DeleteQuote removes a quote; its public link stops working.
func (a *App) DeleteQuote(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	quote, err := a.findVisibleQuote(r, orgID, userID)
	if err != nil {
		return nil
	}
	if err := a.DB.Delete(quote).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete quote", nil, "")
	}
	return r.SendEnvelope(map[string]any{"message": "Quote deleted"})
}

// SendQuote sends the quote link as a WhatsApp text. WhatsApp only allows free
// text inside the 24-hour window, so outside it the agent must copy the link.
func (a *App) SendQuote(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	quote, err := a.findVisibleQuote(r, orgID, userID)
	if err != nil {
		return nil
	}
	var contact models.Contact
	if err := a.DB.Where("id = ?", quote.ContactID).First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}
	if contact.LastInboundAt == nil || time.Since(*contact.LastInboundAt) >= 24*time.Hour {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "The 24-hour window is closed. Copy the link or send a template first.", nil, "")
	}
	account, err := a.resolveWhatsAppAccount(orgID, contact.WhatsAppAccount)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "No WhatsApp account available", nil, "")
	}
	symbol := a.loadSalesSettings(orgID).CurrencySymbol
	text := fmt.Sprintf("%s\nTotal: %s%.2f\n%s/api/public/quotes/%s", quote.Title, symbol, quote.Total, a.quoteBaseURL(r), quote.Token)
	opts := DefaultSendOptions()
	opts.Async = false
	sent, err := a.SendOutgoingMessage(r.RequestCtx, OutgoingMessageRequest{
		Account: account, Contact: &contact, Type: models.MessageTypeText, Content: text,
	}, opts)
	if err != nil {
		a.Log.Error("Failed to send quote", "error", err, "quote_id", quote.ID)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Failed to send quote", nil, "")
	}
	if sent != nil {
		a.DB.Model(&models.Message{}).Where("id = ?", sent.ID).Update("sent_by_user_id", userID)
	}
	now := time.Now()
	updates := map[string]any{"sent_at": now}
	if quote.Status == models.QuoteStatusDraft {
		updates["status"] = models.QuoteStatusSent
	}
	if err := a.DB.Model(quote).Updates(updates).Error; err != nil {
		a.Log.Error("Failed to mark quote sent", "error", err)
	}
	return r.SendEnvelope(a.quoteToResponse(r, quote))
}

var quotePageTemplate = template.Must(template.New("quote").Funcs(template.FuncMap{
	"money": func(v float64) string { return fmt.Sprintf("%.2f", v) },
	"line":  func(q, p float64) string { return fmt.Sprintf("%.2f", q*p) },
}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title>
<style>body{font:16px system-ui,sans-serif;max-width:640px;margin:0 auto;padding:24px 16px;color:#111}
table{width:100%;border-collapse:collapse;margin:16px 0}th,td{padding:8px 4px;border-bottom:1px solid #ddd;text-align:left}
td.n,th.n{text-align:right}.total{font-size:20px;font-weight:600;text-align:right}
.actions{display:flex;flex-direction:column;gap:12px;margin-top:24px}
.btn{display:block;padding:14px;border-radius:8px;text-align:center;text-decoration:none;font:inherit;border:1px solid #16a34a;cursor:pointer;background:#fff;color:#16a34a;width:100%;box-sizing:border-box}
.btn.pay{background:#16a34a;color:#fff}.muted{color:#666;white-space:pre-line}</style></head><body>
<h1>{{.Title}}</h1>
<table><thead><tr><th>Item</th><th class="n">Qty</th><th class="n">Price</th><th class="n">Amount</th></tr></thead><tbody>
{{range .Items}}<tr><td>{{.Name}}</td><td class="n">{{.Quantity}}</td><td class="n">{{$.Currency}}{{money .Price}}</td><td class="n">{{$.Currency}}{{line .Quantity .Price}}</td></tr>{{end}}
</tbody></table>
{{if .TaxPercent}}<p class="muted">Includes tax at {{.TaxPercent}}%</p>{{end}}
<p class="total">Total: {{.Currency}}{{money .Total}}</p>
{{if .Notes}}<p class="muted">{{.Notes}}</p>{{end}}
{{if .Accepted}}<p><strong>Thank you. This quote is accepted.</strong></p>{{end}}
<div class="actions">
{{if .PaymentURL}}<a class="btn pay" href="{{.PaymentURL}}" rel="noopener noreferrer">Pay now</a>{{end}}
{{if not .Accepted}}<form method="post" action="{{.AcceptPath}}"><button class="btn" type="submit">Accept quote</button></form>{{end}}
</div></body></html>`))

func quotePaymentURL(raw string) template.URL {
	// Validated to https or upi when saved; html/template would strip upi
	return template.URL(raw) // #nosec G203
}

func (a *App) findQuoteByToken(token string) (*models.Quote, bool) {
	if len(token) != 48 {
		return nil, false
	}
	var quote models.Quote
	if err := a.DB.Where("token = ?", token).First(&quote).Error; err != nil {
		return nil, false
	}
	return &quote, true
}

// PublicQuotePage renders the quote a lead opens from their link.
func (a *App) PublicQuotePage(r *fastglue.Request) error {
	token := r.RequestCtx.UserValue("token").(string)
	quote, ok := a.findQuoteByToken(token)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Not found", nil, "")
	}
	var out strings.Builder
	err := quotePageTemplate.Execute(&out, map[string]any{
		"Title": quote.Title, "Items": quoteItems(quote.Items), "TaxPercent": quote.TaxPercent, "Total": quote.Total,
		"Notes": quote.Notes, "PaymentURL": quotePaymentURL(quote.PaymentURL),
		"Currency":   a.loadSalesSettings(quote.OrganizationID).CurrencySymbol,
		"Accepted":   quote.Status == models.QuoteStatusAccepted || quote.Status == models.QuoteStatusPaid,
		"AcceptPath": strings.TrimRight(a.Config.Server.BasePath, "/") + "/api/public/quotes/" + token + "/accept",
	})
	if err != nil {
		a.Log.Error("Failed to render quote", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to render quote", nil, "")
	}
	r.RequestCtx.Response.Header.Set("Referrer-Policy", "no-referrer")
	r.RequestCtx.SetContentType("text/html; charset=utf-8")
	r.RequestCtx.SetBodyString(out.String())
	return nil
}

// AcceptPublicQuote records the lead's acceptance and sets the deal value.
func (a *App) AcceptPublicQuote(r *fastglue.Request) error {
	token := r.RequestCtx.UserValue("token").(string)
	quote, ok := a.findQuoteByToken(token)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Not found", nil, "")
	}
	res := a.DB.Model(&models.Quote{}).
		Where("id = ? AND status IN ?", quote.ID, []string{models.QuoteStatusDraft, models.QuoteStatusSent}).
		Update("status", models.QuoteStatusAccepted)
	if res.Error == nil && res.RowsAffected == 1 {
		a.DB.Model(&models.Contact{}).Where("id = ?", quote.ContactID).Update("deal_value", quote.Total)
		a.addLeadNote(quote.OrganizationID, quote.CreatedByID, quote.ContactID, fmt.Sprintf("Customer accepted quote %q", quote.Title))
		var contact models.Contact
		if a.DB.Where("id = ?", quote.ContactID).First(&contact).Error == nil {
			a.broadcastStageChanged(quote.OrganizationID, &contact)
			if contact.AssignedUserID != nil {
				a.notifyUser(quote.OrganizationID, *contact.AssignedUserID, "Quote accepted", quote.Title, "/chat/"+contact.ID.String())
			}
		}
	}
	r.RequestCtx.Redirect(strings.TrimRight(a.Config.Server.BasePath, "/")+"/api/public/quotes/"+token, fasthttp.StatusSeeOther)
	return nil
}

// managedUserIDs returns the members of teams the user manages in this org.
func (a *App) managedUserIDs(userID, orgID uuid.UUID) []uuid.UUID {
	var ids []uuid.UUID
	a.DB.Model(&models.TeamMember{}).Where("team_id IN (?)",
		a.DB.Model(&models.TeamMember{}).
			Joins("JOIN teams ON teams.id = team_members.team_id AND teams.organization_id = ?", orgID).
			Select("team_members.team_id").
			Where("team_members.user_id = ? AND team_members.role = ?", userID, models.TeamRoleManager)).
		Distinct().Pluck("user_id", &ids)
	return ids
}

// applyDefaultCountryCode adds the organization's code to a number typed
// without one. Other formatting is left alone for existing callers.
func (a *App) applyDefaultCountryCode(orgID uuid.UUID, phone string) string {
	return withCountryCode(a.loadSalesSettings(orgID).DefaultCountryCode, phone)
}

func withCountryCode(code, phone string) string {
	if code == "" || strings.HasPrefix(strings.TrimSpace(phone), "+") {
		return phone
	}
	national := strings.TrimPrefix(normalizeLeadPhone(phone), "0")
	if len(national) != 10 {
		return phone
	}
	return code + national
}
