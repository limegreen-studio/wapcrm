package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/handlers"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/pkg/whatsapp"
	"github.com/limegreen-studio/wapcrm/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// mockWA records request bodies sent to the WhatsApp API.
type mockWA struct {
	mu     sync.Mutex
	bodies []string
}

func (m *mockWA) sent() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.bodies...)
}

func newNoReplyApp(t *testing.T) (*handlers.App, *mockWA) {
	t.Helper()
	wa := &mockWA{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		wa.mu.Lock()
		wa.bodies = append(wa.bodies, string(b))
		wa.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]string{{"id": "wamid.mock_" + uuid.NewString()[:8]}},
		})
	}))
	t.Cleanup(srv.Close)
	app := newTestApp(t, withWhatsApp(whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)))
	return app, wa
}

func putNoReplyRule(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, body map[string]any) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.UpdateNoReplyRule(req))
	return testutil.GetResponseStatusCode(req)
}

func TestApp_UpdateNoReplyRule_Validation(t *testing.T) {
	app, _ := newNoReplyApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "adm", []string{"settings.general:write", "settings.general:read"})
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name) // body "Hello {{1}}"

	assert.Equal(t, fasthttp.StatusBadRequest, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{"enabled": true, "hours": 0, "create_task": true}))
	assert.Equal(t, fasthttp.StatusBadRequest, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{"enabled": true, "hours": 24}))
	assert.Equal(t, fasthttp.StatusBadRequest, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{"enabled": true, "hours": 24, "template_id": tpl.ID}),
		"missing {{1}} param")
	assert.Equal(t, fasthttp.StatusBadRequest, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{"enabled": true, "hours": 24, "template_id": uuid.New(), "template_params": map[string]string{"1": "x"}}))

	require.NoError(t, app.DB.Model(tpl).Update("header_type", "IMAGE").Error)
	assert.Equal(t, fasthttp.StatusBadRequest, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{"enabled": true, "hours": 24, "template_id": tpl.ID, "template_params": map[string]string{"1": "x"}}))
	require.NoError(t, app.DB.Model(tpl).Update("header_type", "").Error)

	require.Equal(t, fasthttp.StatusOK, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{
		"enabled": true, "hours": 24, "template_id": tpl.ID, "template_params": map[string]string{"1": "{{name}}"},
	}))
	// Disabled rule may be saved without actions
	require.Equal(t, fasthttp.StatusOK, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{"enabled": false}))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, admin.ID)
	require.NoError(t, app.GetNoReplyRule(req))
	var resp struct {
		Data models.NoReplyRule `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.False(t, resp.Data.Enabled)
	assert.Equal(t, admin.ID, resp.Data.UpdatedByID)
}

func TestApp_ProcessNoReplyLeads(t *testing.T) {
	app, wa := newNoReplyApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "adm",
		[]string{"settings.general:write", "chat:read", "chat:write", "contacts:read"})
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	owner := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)

	p := listPipelines(t, app, org.ID, admin.ID)[0]
	won := p.Stages[4].ID
	now := time.Now()
	ago := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }

	mk := func(name string, stage uuid.UUID, lastMsg, lastIn *time.Time, assigned *uuid.UUID) *models.Contact {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		status, _ := moveContact(t, app, org.ID, admin.ID, c.ID, map[string]any{"stage_id": stage})
		require.Equal(t, fasthttp.StatusOK, status)
		require.NoError(t, app.DB.Model(c).Updates(map[string]any{
			"profile_name": name, "last_message_at": lastMsg, "last_inbound_at": lastIn,
			"assigned_user_id": assigned, "whats_app_account": account.Name,
		}).Error)
		return c
	}
	t30, t2, t40 := ago(30), ago(2), ago(40)
	silent := mk("Silent", p.Stages[0].ID, &t30, &t40, &owner.ID) // our msg 30h ago, they wrote 40h ago -> act
	recent := mk("Recent", p.Stages[0].ID, &t2, &t40, nil)        // our msg only 2h ago -> wait
	replied := mk("Replied", p.Stages[0].ID, &t30, &t30, nil)     // their message is the last -> skip
	closed := mk("Closed", won, &t30, &t40, nil)                  // won stage -> skip
	neverWrote := mk("Never", p.Stages[1].ID, &t30, nil, nil)     // no inbound ever, our msg 30h ago -> act

	require.Equal(t, fasthttp.StatusOK, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{
		"enabled": true, "hours": 24, "create_task": true, "task_title": "Chase lead",
		"template_id": tpl.ID, "template_params": map[string]string{"1": "{{name}}"},
	}))

	app.ProcessNoReplyLeads(now)

	acted := func(c *models.Contact) bool {
		var n int64
		app.DB.Model(&models.Task{}).Where("contact_id = ? AND title = ?", c.ID, "Chase lead").Count(&n)
		return n > 0
	}
	assert.True(t, acted(silent))
	assert.True(t, acted(neverWrote))
	assert.False(t, acted(recent))
	assert.False(t, acted(replied))
	assert.False(t, acted(closed))

	var task models.Task
	require.NoError(t, app.DB.Where("contact_id = ?", silent.ID).First(&task).Error)
	require.NotNil(t, task.AssigneeID)
	assert.Equal(t, owner.ID, *task.AssigneeID, "task goes to the lead owner")
	assert.Equal(t, owner.ID, task.CreatedByID)

	var note models.ConversationNote
	require.NoError(t, app.DB.Where("contact_id = ? AND content LIKE ?", neverWrote.ID, "No reply for 24h%").First(&note).Error)
	assert.Contains(t, note.Content, "created a follow-up")
	assert.Contains(t, note.Content, "sent template")
	assert.Equal(t, admin.ID, note.CreatedByID, "unowned lead: acts as the admin who saved the rule")

	// Template went out with the contact name filled in
	testutil.AssertEventually(t, func() bool {
		hits := 0
		for _, b := range wa.sent() {
			if strings.Contains(b, "Silent") || strings.Contains(b, "Never") {
				hits++
			}
		}
		return hits == 2
	}, 3*time.Second, "two template sends with names")

	// Runs once per silence: a second tick does nothing new
	app.ProcessNoReplyLeads(now.Add(time.Hour))
	var tasks int64
	app.DB.Model(&models.Task{}).Where("contact_id IN ?", []uuid.UUID{silent.ID, neverWrote.ID}).Count(&tasks)
	assert.Equal(t, int64(2), tasks)

	// After the customer replies and we answer, a new silence can trigger again
	t25, t26 := ago(25), ago(26)
	require.NoError(t, app.DB.Model(silent).Updates(map[string]any{"last_inbound_at": t26, "last_message_at": t25}).Error)
	require.NoError(t, app.DB.Model(silent).Update("no_reply_handled_at", ago(27)).Error)
	app.ProcessNoReplyLeads(now.Add(2 * time.Hour))
	app.DB.Model(&models.Task{}).Where("contact_id = ?", silent.ID).Count(&tasks)
	assert.Equal(t, int64(2), tasks)
}

func testNoReply(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, body map[string]any) (int, string) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.TestNoReplyRule(req))
	return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
}

func TestApp_TestNoReplyRule_SendsNow(t *testing.T) {
	app, wa := newNoReplyApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "adm",
		[]string{"settings.general:write", "chat:read", "contacts:read"})
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Updates(map[string]any{"profile_name": "Meera", "whats_app_account": account.Name}).Error)

	// No template given and none saved
	status, _ := testNoReply(t, app, org.ID, admin.ID, map[string]any{"contact_id": contact.ID})
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	// Uses the saved rule's template
	require.Equal(t, fasthttp.StatusOK, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{
		"enabled": false, "template_id": tpl.ID, "template_params": map[string]string{"1": "{{name}}"},
	}))
	status, body := testNoReply(t, app, org.ID, admin.ID, map[string]any{"contact_id": contact.ID})
	require.Equal(t, fasthttp.StatusOK, status, body)
	assert.Contains(t, body, "wamid.mock_")
	require.NotEmpty(t, wa.sent())
	assert.Contains(t, wa.sent()[len(wa.sent())-1], "Meera")

	// Test send does not mark the lead as handled
	assert.Nil(t, reloadContact(t, app, contact.ID).NoReplyHandledAt)
}

func TestApp_TestNoReplyRule_ReportsMetaRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "Template name does not exist in the translation", "code": 132001},
		})
	}))
	t.Cleanup(srv.Close)
	app := newTestApp(t, withWhatsApp(whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)))
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "adm",
		[]string{"settings.general:write", "chat:read", "chat:write", "contacts:read"})
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("whats_app_account", account.Name).Error)

	status, body := testNoReply(t, app, org.ID, admin.ID, map[string]any{
		"contact_id": contact.ID, "template_id": tpl.ID, "template_params": map[string]string{"1": "x"},
	})
	assert.Equal(t, fasthttp.StatusBadGateway, status)
	assert.Contains(t, body, "132001")

	// The background rule records the failure in the conversation note
	p := listPipelines(t, app, org.ID, admin.ID)[0]
	s, _ := moveContact(t, app, org.ID, admin.ID, contact.ID, map[string]any{"stage_id": p.Stages[0].ID})
	require.Equal(t, fasthttp.StatusOK, s)
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, app.DB.Model(contact).Updates(map[string]any{"last_message_at": old, "no_reply_handled_at": nil}).Error)
	require.Equal(t, fasthttp.StatusOK, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{
		"enabled": true, "hours": 24, "template_id": tpl.ID, "template_params": map[string]string{"1": "x"},
	}))
	app.ProcessNoReplyLeads(time.Now())
	var note models.ConversationNote
	require.NoError(t, app.DB.Where("contact_id = ? AND content LIKE ?", contact.ID, "No reply for 24h%").First(&note).Error)
	assert.Contains(t, note.Content, "could not send template")
}
