package handlers_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/assignment"
	"github.com/limegreen-studio/wapcrm/internal/handlers"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func leadOpsAdmin(t *testing.T, app *handlers.App, orgID uuid.UUID) *models.User {
	t.Helper()
	role := testutil.CreateTestRoleWithKeys(t, app.DB, orgID, "lead-admin-"+uuid.NewString()[:6],
		[]string{"chat:read", "chat:write", "contacts:read", "contacts:write", "settings.general:read", "settings.general:write"})
	return testutil.CreateTestUser(t, app.DB, orgID, testutil.WithRoleID(&role.ID))
}

func leadOpsAgent(t *testing.T, app *handlers.App, orgID uuid.UUID) *models.User {
	t.Helper()
	role := testutil.CreateTestRoleWithKeys(t, app.DB, orgID, "lead-agent-"+uuid.NewString()[:6], []string{"chat:read", "chat:write"})
	return testutil.CreateTestUser(t, app.DB, orgID, testutil.WithRoleID(&role.ID))
}

func onStage(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, stage uuid.UUID, opts ...testutil.ContactOption) *models.Contact {
	t.Helper()
	c := testutil.CreateTestContactWith(t, app.DB, orgID, opts...)
	status, _ := moveContact(t, app, orgID, userID, c.ID, map[string]any{"stage_id": stage})
	require.Equal(t, fasthttp.StatusOK, status)
	return c
}

func call(t *testing.T, h func(*fastglue.Request) error, orgID, userID uuid.UUID, body any, path map[string]string) (int, []byte) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	for k, v := range path {
		testutil.SetPathParam(req, k, v)
	}
	require.NoError(t, h(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

func TestApp_BulkUpdateContacts(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	agent := leadOpsAgent(t, app, org.ID)
	p := listPipelines(t, app, org.ID, admin.ID)[0]

	mine := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	other := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	require.NoError(t, app.DB.Model(mine).Update("assigned_user_id", agent.ID).Error)

	status, body := call(t, app.BulkUpdateContacts, org.ID, agent.ID, map[string]any{
		"contact_ids": []uuid.UUID{mine.ID, other.ID}, "stage_id": p.Stages[1].ID, "add_tags": []string{"hot"},
	}, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Contains(t, string(body), `"updated":1`, "agent's bulk skips contacts it cannot see")

	var got models.Contact
	require.NoError(t, app.DB.First(&got, "id = ?", mine.ID).Error)
	assert.Equal(t, p.Stages[1].ID, *got.StageID)
	assert.Contains(t, got.Tags, any("hot"))
	var untouched models.Contact
	require.NoError(t, app.DB.First(&untouched, "id = ?", other.ID).Error)
	assert.Equal(t, p.Stages[0].ID, *untouched.StageID, "unseen contact stays put")

	var history int64
	app.DB.Model(&models.StageHistory{}).Where("contact_id = ? AND to_stage_id = ?", mine.ID, p.Stages[1].ID).Count(&history)
	assert.EqualValues(t, 1, history)

	status, _ = call(t, app.BulkUpdateContacts, org.ID, agent.ID, map[string]any{
		"contact_ids": []uuid.UUID{mine.ID}, "assigned_user_id": admin.ID,
	}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, status, "assigning needs contacts:write")

	status, _ = call(t, app.BulkUpdateContacts, org.ID, admin.ID, map[string]any{"contact_ids": []uuid.UUID{}, "add_tags": []string{"x"}}, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

func TestApp_ClaimContact_OneWinner(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	a1 := leadOpsAgent(t, app, org.ID)
	a2 := leadOpsAgent(t, app, org.ID)
	p := listPipelines(t, app, org.ID, admin.ID)[0]
	lead := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	require.NoError(t, app.DB.Model(lead).Update("last_message_preview", "secret text").Error)

	status, body := call(t, app.ListLeadQueue, org.ID, a1.ID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Contains(t, string(body), lead.ID.String())
	assert.NotContains(t, string(body), "secret text", "preview hidden until claimed")

	var wg sync.WaitGroup
	results := make([]int, 2)
	for i, agent := range []*models.User{a1, a2} {
		wg.Add(1)
		go func(i int, id uuid.UUID) {
			defer wg.Done()
			results[i], _ = call(t, app.ClaimContact, org.ID, id, nil, map[string]string{"id": lead.ID.String()})
		}(i, agent.ID)
	}
	wg.Wait()
	assert.ElementsMatch(t, []int{fasthttp.StatusOK, fasthttp.StatusConflict}, results)

	_, body = call(t, app.ListLeadQueue, org.ID, a1.ID, nil, nil)
	assert.NotContains(t, string(body), lead.ID.String(), "claimed lead leaves the queue")
}

func TestApp_Deal_BoardFiltersAndValue(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	p := listPipelines(t, app, org.ID, admin.ID)[0]
	c1 := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	c2 := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)

	id := map[string]string{"id": c1.ID.String()}
	status, _ := call(t, app.UpdateContactDeal, org.ID, admin.ID, map[string]any{"deal_value": 1500.5, "close_reason": "Budget"}, id)
	require.Equal(t, fasthttp.StatusOK, status)
	status, _ = call(t, app.UpdateContactDeal, org.ID, admin.ID, map[string]any{"deal_value": -1}, id)
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	status, _ = call(t, app.UpdateContactDeal, org.ID, admin.ID, map[string]any{"deal_value": 250}, map[string]string{"id": c2.ID.String()})
	require.Equal(t, fasthttp.StatusOK, status)

	require.NoError(t, app.DB.Model(c1).Updates(map[string]any{
		"tags": models.JSONBArray{"vip"}, "metadata": models.JSONB{"lead": map[string]any{"source": "Facebook Ads"}},
	}).Error)

	board := func(params map[string]string) handlers.BoardColumn {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, admin.ID)
		testutil.SetPathParam(req, "id", p.ID.String())
		for k, v := range params {
			testutil.SetQueryParam(req, k, v)
		}
		require.NoError(t, app.GetPipelineBoard(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
		var resp boardResp
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		return resp.Data.Columns[0]
	}
	assert.InDelta(t, 1750.5, board(nil).Value, 0.001)
	assert.EqualValues(t, 1, board(map[string]string{"tag": "vip"}).Total)
	assert.EqualValues(t, 1, board(map[string]string{"lead_key": "source", "lead_value": "facebook"}).Total)
	assert.EqualValues(t, 0, board(map[string]string{"created_before": time.Now().Add(-time.Hour).Format(time.RFC3339)}).Total)
}

func TestApp_MergeContacts(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	suffix := time.Now().Format("0405") + "12"
	short := "98765" + suffix[:5]
	primary := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithPhoneNumber(short))
	dup := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithPhoneNumber("91"+short))
	task := models.Task{OrganizationID: org.ID, ContactID: dup.ID, CreatedByID: admin.ID, Title: "Moved"}
	require.NoError(t, app.DB.Create(&task).Error)
	require.NoError(t, app.DB.Model(dup).Updates(map[string]any{"tags": models.JSONBArray{"from-dup"}, "deal_value": 42}).Error)

	status, body := call(t, app.ListDuplicateContacts, org.ID, admin.ID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Contains(t, string(body), dup.ID.String())

	status, body = call(t, app.MergeContacts, org.ID, admin.ID, map[string]any{"primary_id": primary.ID, "duplicate_id": dup.ID}, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(body))

	var got models.Contact
	require.NoError(t, app.DB.First(&got, "id = ?", primary.ID).Error)
	assert.Equal(t, "91"+short, got.PhoneNumber, "keeps the international form WhatsApp sends")
	assert.Contains(t, got.Tags, any("from-dup"))
	assert.InDelta(t, 42, got.DealValue, 0.001)

	var moved models.Task
	require.NoError(t, app.DB.First(&moved, "id = ?", task.ID).Error)
	assert.Equal(t, primary.ID, moved.ContactID)
	assert.Error(t, app.DB.First(&models.Contact{}, "id = ?", dup.ID).Error, "duplicate is soft-deleted")
}

func TestApp_PipelineReport_FromHistory(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	p := listPipelines(t, app, org.ID, admin.ID)[0]
	won := p.Stages[4].ID
	c := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	require.NoError(t, app.DB.Model(c).Updates(map[string]any{
		"deal_value": 900, "metadata": models.JSONB{"lead": map[string]any{"source": "=cmd"}},
	}).Error)
	status, _ := moveContact(t, app, org.ID, admin.ID, c.ID, map[string]any{"stage_id": won})
	require.Equal(t, fasthttp.StatusOK, status)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, admin.ID)
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.GetPipelineReport(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
	var resp struct {
		Data handlers.PipelineReport `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	require.Len(t, resp.Data.ByPeriod, 1)
	assert.EqualValues(t, 1, resp.Data.ByPeriod[0].Leads)
	assert.EqualValues(t, 1, resp.Data.ByPeriod[0].Won)
	require.NotEmpty(t, resp.Data.BySource)
	assert.InDelta(t, 900, resp.Data.BySource[0].WonValue, 0.001)
	require.Len(t, resp.Data.StageTimes, 1)
	assert.Equal(t, p.Stages[0].Name, resp.Data.StageTimes[0].Stage)

	csvReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(csvReq, org.ID, admin.ID)
	testutil.SetPathParam(csvReq, "id", p.ID.String())
	testutil.SetQueryParam(csvReq, "format", "csv")
	require.NoError(t, app.GetPipelineReport(csvReq))
	assert.Contains(t, string(testutil.GetResponseBody(csvReq)), "'=cmd", "formula cells are neutralised")
}

func TestApp_LogCallOutcome(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := leadOpsAgent(t, app, org.ID)
	c := testutil.CreateTestContact(t, app.DB, org.ID)
	id := map[string]string{"id": c.ID.String()}

	status, _ := call(t, app.LogCallOutcome, org.ID, agent.ID, map[string]any{"outcome": "connected"}, id)
	assert.Equal(t, fasthttp.StatusNotFound, status, "agent cannot log on a lead it cannot see")

	require.NoError(t, app.DB.Model(c).Update("assigned_user_id", agent.ID).Error)
	status, _ = call(t, app.LogCallOutcome, org.ID, agent.ID, map[string]any{"outcome": "nope"}, id)
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	due := time.Now().Add(time.Hour)
	status, body := call(t, app.LogCallOutcome, org.ID, agent.ID, map[string]any{"outcome": "callback", "notes": "After 5pm", "follow_up_at": due}, id)
	require.Equal(t, fasthttp.StatusOK, status, string(body))

	var note models.ConversationNote
	require.NoError(t, app.DB.Where("contact_id = ?", c.ID).First(&note).Error)
	assert.Contains(t, note.Content, "Asked to call back")
	assert.Contains(t, note.Content, "After 5pm")
	var tasks int64
	app.DB.Model(&models.Task{}).Where("contact_id = ? AND assignee_id = ?", c.ID, agent.ID).Count(&tasks)
	assert.EqualValues(t, 1, tasks)
}

func leadSourceToken(t *testing.T, app *handlers.App, orgID, userID uuid.UUID) string {
	t.Helper()
	status, body := call(t, app.GetLeadSources, orgID, userID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	var resp struct {
		Data handlers.LeadSourcesResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Len(t, resp.Data.Token, 48)
	return resp.Data.Token
}

func publicLead(t *testing.T, app *handlers.App, token string, body map[string]any) (int, string) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetPathParam(req, "token", token)
	require.NoError(t, app.PublicCaptureLead(req))
	return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
}

func TestApp_PublicCaptureLead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	token := leadSourceToken(t, app, org.ID, admin.ID)

	status, _ := publicLead(t, app, strings.Repeat("0", 48), map[string]any{"phone": "919000000001"})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	phone := "9190" + time.Now().Format("05040") + "11"
	status, _ = publicLead(t, app, token, map[string]any{"phone": phone, "company_website": "http://spam"})
	assert.Equal(t, fasthttp.StatusOK, status)
	var count int64
	app.DB.Model(&models.Contact{}).Where("organization_id = ? AND phone_number = ?", org.ID, phone).Count(&count)
	assert.Zero(t, count, "honeypot submissions are dropped")

	status, body := publicLead(t, app, token, map[string]any{"phone": "+" + phone, "name": "Web Lead", "lead_city": "Pune"})
	require.Equal(t, fasthttp.StatusOK, status, body)
	var c models.Contact
	require.NoError(t, app.DB.Where("organization_id = ? AND phone_number = ?", org.ID, phone).First(&c).Error)
	assert.NotNil(t, c.StageID)
	lead := c.Metadata["lead"].(map[string]any)
	assert.Equal(t, "Pune", lead["city"])
	assert.Equal(t, "website", lead["source"])
	assert.NotContains(t, body, c.ID.String(), "public response does not expose contact ids")

	status, _ = publicLead(t, app, token, map[string]any{"name": "No phone"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

func TestApp_FacebookLeadWebhook(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer page-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"field_data": []map[string]any{
			{"name": "full_name", "values": []string{"FB Lead"}},
			{"name": "phone_number", "values": []string{"+919111222333"}},
			{"name": "city", "values": []string{"Chennai"}},
		}})
	}))
	t.Cleanup(graph.Close)

	app := newTestApp(t)
	app.Config.WhatsApp.BaseURL = graph.URL
	app.Config.WhatsApp.APIVersion = "v24.0"
	app.HTTPClient = graph.Client()
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	token := leadSourceToken(t, app, org.ID, admin.ID)

	status, body := call(t, app.UpdateLeadSources, org.ID, admin.ID, map[string]any{"facebook_page_token": "page-tok", "facebook_app_secret": "shh"}, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Contains(t, string(body), `"facebook_ready":true`)
	assert.NotContains(t, string(body), "page-tok", "secrets are never returned")

	event := map[string]any{"entry": []map[string]any{{"changes": []map[string]any{{"field": "leadgen", "value": map[string]any{"leadgen_id": "L1"}}}}}}
	raw, _ := json.Marshal(event)

	send := func(signature string) int {
		req := testutil.NewJSONRequest(t, event)
		testutil.SetPathParam(req, "token", token)
		req.RequestCtx.Request.Header.Set("X-Hub-Signature-256", signature)
		require.NoError(t, app.FacebookLeadWebhook(req))
		return testutil.GetResponseStatusCode(req)
	}
	assert.Equal(t, fasthttp.StatusForbidden, send("sha256=00"))

	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write(raw)
	require.Equal(t, fasthttp.StatusOK, send("sha256="+hex.EncodeToString(mac.Sum(nil))))

	var c models.Contact
	require.NoError(t, app.DB.Where("organization_id = ? AND phone_number = ?", org.ID, "919111222333").First(&c).Error)
	assert.Equal(t, "FB Lead", c.ProfileName)
	assert.Equal(t, "facebook", c.Metadata["lead"].(map[string]any)["source"])
}

func TestApp_NoReply_StepsAndPipelineRule(t *testing.T) {
	app, _ := newNoReplyApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	first := listPipelines(t, app, org.ID, admin.ID)[0]

	status, body := call(t, app.CreatePipeline, org.ID, admin.ID, map[string]any{
		"name": "Second", "stages": []map[string]any{{"name": "Open", "color": "blue", "kind": "open"}},
	}, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	var created struct {
		Data handlers.PipelineResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &created))

	now := time.Now()
	lastMsg := now.Add(-30 * time.Hour)
	mk := func(stage uuid.UUID) *models.Contact {
		c := onStage(t, app, org.ID, admin.ID, stage)
		require.NoError(t, app.DB.Model(c).Updates(map[string]any{"last_message_at": lastMsg}).Error)
		return c
	}
	onFirst, onSecond := mk(first.Stages[0].ID), mk(created.Data.Stages[0].ID)

	require.Equal(t, fasthttp.StatusOK, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{
		"enabled": true, "hours": 24, "create_task": true, "task_title": "Org step",
		"steps": []map[string]any{{"hours": 48, "create_task": true, "task_title": "Second step"}},
	}))
	req := testutil.NewJSONRequest(t, map[string]any{"enabled": false, "hours": 24, "create_task": true})
	testutil.SetAuthContext(req, org.ID, admin.ID)
	testutil.SetQueryParam(req, "pipeline_id", created.Data.ID.String())
	require.NoError(t, app.UpdateNoReplyRule(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	titles := func(c *models.Contact) []string {
		var out []string
		app.DB.Model(&models.Task{}).Where("contact_id = ?", c.ID).Order("created_at").Pluck("title", &out)
		return out
	}

	app.ProcessNoReplyLeads(now)
	assert.Equal(t, []string{"Org step"}, titles(onFirst))
	assert.Empty(t, titles(onSecond), "a disabled pipeline rule overrides the org rule")

	app.ProcessNoReplyLeads(now.Add(47 * time.Hour))
	assert.Equal(t, []string{"Org step"}, titles(onFirst), "second step waits for its own delay")

	app.ProcessNoReplyLeads(now.Add(49 * time.Hour))
	assert.Equal(t, []string{"Org step", "Second step"}, titles(onFirst))

	app.ProcessNoReplyLeads(now.Add(200 * time.Hour))
	assert.Len(t, titles(onFirst), 2, "sequence ends after the last step")
}

func TestApp_DefaultPipeline_ConcurrentFirstLeads(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, _ := captureLead(t, app, org.ID, admin.ID, map[string]any{"phone_number": "9198000000" + strconv.Itoa(10+i)})
			assert.Equal(t, fasthttp.StatusOK, status)
		}(i)
	}
	wg.Wait()

	var count int64
	app.DB.Model(&models.Pipeline{}).Where("organization_id = ?", org.ID).Count(&count)
	assert.EqualValues(t, 1, count)
}

func createAutomation(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, body map[string]any) handlers.AutomationRuleResponse {
	t.Helper()
	status, raw := call(t, app.CreateAutomationRule, orgID, userID, body, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(raw))
	var resp struct {
		Data handlers.AutomationRuleResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	return resp.Data
}

func tagsOf(t *testing.T, app *handlers.App, id uuid.UUID) models.JSONBArray {
	t.Helper()
	var c models.Contact
	require.NoError(t, app.DB.First(&c, "id = ?", id).Error)
	return c.Tags
}

func TestApp_Automation_Validation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	agent := leadOpsAgent(t, app, org.ID)

	good := map[string]any{"name": "R", "trigger": "tag_added", "actions": []map[string]any{{"type": "add_tag", "tag": "x"}}}
	status, _ := call(t, app.CreateAutomationRule, org.ID, agent.ID, good, nil)
	assert.Equal(t, fasthttp.StatusForbidden, status)

	for name, body := range map[string]map[string]any{
		"no name":      {"trigger": "tag_added", "actions": []map[string]any{{"type": "add_tag", "tag": "x"}}},
		"bad trigger":  {"name": "R", "trigger": "nope", "actions": []map[string]any{{"type": "add_tag", "tag": "x"}}},
		"no actions":   {"name": "R", "trigger": "tag_added"},
		"bad action":   {"name": "R", "trigger": "tag_added", "actions": []map[string]any{{"type": "delete_all"}}},
		"bad delay":    {"name": "R", "trigger": "tag_added", "actions": []map[string]any{{"type": "add_tag", "tag": "x", "delay_hours": 9999}}},
		"foreign user": {"name": "R", "trigger": "tag_added", "actions": []map[string]any{{"type": "assign_user", "user_id": uuid.New()}}},
	} {
		status, _ := call(t, app.CreateAutomationRule, org.ID, admin.ID, body, nil)
		assert.Equal(t, fasthttp.StatusBadRequest, status, name)
	}
}

func TestApp_Automation_RunsAndDoesNotLoop(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	p := listPipelines(t, app, org.ID, admin.ID)[0]
	contact := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)

	// Each rule's action would trigger the other if actions fired rules
	createAutomation(t, app, org.ID, admin.ID, map[string]any{
		"name": "A", "enabled": true, "trigger": "tag_added", "tag": "a",
		"actions": []map[string]any{{"type": "add_tag", "tag": "b"}, {"type": "create_task", "task_title": "From rule"}, {"type": "assign_user", "user_id": admin.ID}},
	})
	createAutomation(t, app, org.ID, admin.ID, map[string]any{
		"name": "B", "enabled": true, "trigger": "tag_added", "tag": "b",
		"actions": []map[string]any{{"type": "add_tag", "tag": "loop"}},
	})
	createAutomation(t, app, org.ID, admin.ID, map[string]any{
		"name": "Off", "trigger": "tag_added", "tag": "a",
		"actions": []map[string]any{{"type": "add_tag", "tag": "disabled-rule"}},
	})

	status, body := call(t, app.BulkUpdateContacts, org.ID, admin.ID, map[string]any{"contact_ids": []uuid.UUID{contact.ID}, "add_tags": []string{"a"}}, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(body))

	require.Eventually(t, func() bool {
		var c models.Contact
		app.DB.First(&c, "id = ?", contact.ID)
		return c.AssignedUserID != nil
	}, 5*time.Second, 50*time.Millisecond)

	tags := tagsOf(t, app, contact.ID)
	assert.Contains(t, tags, any("b"))
	assert.NotContains(t, tags, any("loop"), "changes made by actions do not fire rules")
	assert.NotContains(t, tags, any("disabled-rule"))
	var tasks int64
	app.DB.Model(&models.Task{}).Where("contact_id = ? AND title = ?", contact.ID, "From rule").Count(&tasks)
	assert.EqualValues(t, 1, tasks)
}

func TestApp_Automation_DripStopsOnReplyOrStageLeave(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	p := listPipelines(t, app, org.ID, admin.ID)[0]
	target := p.Stages[1].ID

	rule := createAutomation(t, app, org.ID, admin.ID, map[string]any{
		"name": "Drip", "enabled": true, "trigger": "stage_changed", "stage_id": target,
		"actions": []map[string]any{{"type": "add_tag", "tag": "drip", "delay_hours": 24}},
	})

	mk := func() *models.Contact {
		c := onStage(t, app, org.ID, admin.ID, target)
		require.Eventually(t, func() bool {
			var n int64
			app.DB.Model(&models.AutomationJob{}).Where("contact_id = ?", c.ID).Count(&n)
			return n == 1
		}, 5*time.Second, 50*time.Millisecond)
		return c
	}
	stays, replied, moved := mk(), mk(), mk()

	inbound := time.Now().Add(time.Minute)
	require.NoError(t, app.DB.Model(replied).Update("last_inbound_at", inbound).Error)
	status, _ := moveContact(t, app, org.ID, admin.ID, moved.ID, map[string]any{"stage_id": p.Stages[0].ID})
	require.Equal(t, fasthttp.StatusOK, status)

	app.ProcessAutomationJobs(time.Now().Add(23 * time.Hour))
	assert.NotContains(t, tagsOf(t, app, stays.ID), any("drip"), "waits for its delay")

	app.ProcessAutomationJobs(time.Now().Add(25 * time.Hour))
	assert.Contains(t, tagsOf(t, app, stays.ID), any("drip"))
	assert.NotContains(t, tagsOf(t, app, replied.ID), any("drip"), "a reply stops the drip")
	assert.NotContains(t, tagsOf(t, app, moved.ID), any("drip"), "leaving the stage stops the drip")

	var open int64
	app.DB.Model(&models.AutomationJob{}).Where("rule_id = ? AND processed_at IS NULL", rule.ID).Count(&open)
	assert.Zero(t, open, "each step is claimed once")
}

func TestApp_Automation_TemplateSentOncePerWindow(t *testing.T) {
	app, wa := newNoReplyApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	p := listPipelines(t, app, org.ID, admin.ID)[0]
	c := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	require.NoError(t, app.DB.Model(c).Update("whats_app_account", account.Name).Error)

	createAutomation(t, app, org.ID, admin.ID, map[string]any{
		"name": "Send", "enabled": true, "trigger": "stage_changed", "stage_id": p.Stages[1].ID,
		"actions": []map[string]any{{"type": "send_template", "template_id": tpl.ID, "template_params": map[string]string{"1": "{{name}}"}}},
	})

	for _, stage := range []uuid.UUID{p.Stages[1].ID, p.Stages[0].ID, p.Stages[1].ID} {
		status, _ := moveContact(t, app, org.ID, admin.ID, c.ID, map[string]any{"stage_id": stage})
		require.Equal(t, fasthttp.StatusOK, status)
		time.Sleep(150 * time.Millisecond)
	}
	require.Eventually(t, func() bool { return len(wa.sent()) >= 1 }, 5*time.Second, 50*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, wa.sent(), 1, "same rule, lead and template send once per window")
}

func TestApp_BulkSendTemplate(t *testing.T) {
	app, wa := newNoReplyApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	agent := leadOpsAgent(t, app, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	require.NoError(t, app.DB.Model(tpl).Update("category", "MARKETING").Error)

	ok := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	optedOut := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	require.NoError(t, app.DB.Model(optedOut).Update("marketing_opt_out", true).Error)

	body := map[string]any{"contact_ids": []uuid.UUID{ok.ID, optedOut.ID}, "template_id": tpl.ID, "template_params": map[string]string{"1": "{{name}}"}}
	status, _ := call(t, app.BulkSendTemplate, org.ID, agent.ID, body, nil)
	assert.Equal(t, fasthttp.StatusForbidden, status, "agents cannot spend on templates")

	status, raw := call(t, app.BulkSendTemplate, org.ID, admin.ID, body, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(raw))
	assert.Contains(t, string(raw), `"queued":1`)
	assert.Contains(t, string(raw), `"skipped":1`)
	require.Eventually(t, func() bool { return len(wa.sent()) == 1 }, 5*time.Second, 50*time.Millisecond)
}

func TestApp_TeamReport(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	agent := leadOpsAgent(t, app, org.ID)
	p := listPipelines(t, app, org.ID, admin.ID)[0]
	lead := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	now := time.Now()
	in := now.Add(-10 * time.Minute)
	require.NoError(t, app.DB.Model(lead).Updates(map[string]any{
		"assigned_user_id": agent.ID, "last_inbound_at": in, "last_message_at": in,
	}).Error)
	require.NoError(t, app.DB.Create(&models.Message{OrganizationID: org.ID, WhatsAppAccount: "a", ContactID: lead.ID, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, BaseModel: models.BaseModel{CreatedAt: now.Add(-10 * time.Minute)}}).Error)
	require.NoError(t, app.DB.Create(&models.Message{OrganizationID: org.ID, WhatsAppAccount: "a", ContactID: lead.ID, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, SentByUserID: &agent.ID, BaseModel: models.BaseModel{CreatedAt: now.Add(-8 * time.Minute)}}).Error)
	due := now.Add(-time.Hour)
	require.NoError(t, app.DB.Create(&models.Task{OrganizationID: org.ID, ContactID: lead.ID, AssigneeID: &agent.ID, CreatedByID: admin.ID, Title: "Late", DueAt: &due}).Error)

	status, _ := call(t, app.GetTeamReport, org.ID, agent.ID, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, status, "agents cannot see the whole team")

	status, raw := call(t, app.GetTeamReport, org.ID, admin.ID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(raw))
	var resp struct {
		Data handlers.TeamReport `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	var row *handlers.TeamReportRow
	for i := range resp.Data.Agents {
		if resp.Data.Agents[i].UserID == agent.ID {
			row = &resp.Data.Agents[i]
		}
	}
	require.NotNil(t, row)
	assert.EqualValues(t, 1, row.OpenLeads)
	assert.EqualValues(t, 1, row.AwaitingReply)
	assert.EqualValues(t, 1, row.OverdueTasks)
	assert.EqualValues(t, 1, row.MessagesSent)
	assert.EqualValues(t, 0, row.Untouched, "the agent has written to this lead")
	require.NotNil(t, row.AvgResponseSeconds)
	assert.InDelta(t, 120, *row.AvgResponseSeconds, 1)
}

func createSalesTeam(t *testing.T, app *handlers.App, orgID uuid.UUID) *models.Team {
	t.Helper()
	team := models.Team{OrganizationID: orgID, Name: "sales-" + uuid.NewString()[:8], AssignmentStrategy: models.AssignmentStrategyRoundRobin, IsActive: true}
	require.NoError(t, app.DB.Create(&team).Error)
	return &team
}

func setSalesSettings(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, body map[string]any) {
	t.Helper()
	status, raw := call(t, app.UpdateSalesSettings, orgID, userID, body, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(raw))
}

func TestApp_DefaultCountryCode(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	setSalesSettings(t, app, org.ID, admin.ID, map[string]any{"default_country_code": "+91"})

	cases := map[string]string{
		"9000012345":   "919000012345",
		"09840121225":  "919840121225",
		"+14155550100": "14155550100",
		"919840121226": "919840121226",
		"98401 212-27": "919840121227",
	}
	for input, want := range cases {
		status, resp := captureLead(t, app, org.ID, admin.ID, map[string]any{"phone_number": input})
		require.Equal(t, fasthttp.StatusOK, status, input)
		var c models.Contact
		require.NoError(t, app.DB.First(&c, "id = ?", resp.ContactID).Error)
		assert.Equal(t, want, c.PhoneNumber, input)
	}

	status, raw := call(t, app.UpdateSalesSettings, org.ID, admin.ID, map[string]any{"default_country_code": "abc"}, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, status, string(raw))
}

func TestApp_OptOut_BlocksEveryAutomatedSend(t *testing.T) {
	app, wa := newNoReplyApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	require.NoError(t, app.DB.Model(tpl).Update("category", "MARKETING").Error)
	p := listPipelines(t, app, org.ID, admin.ID)[0]

	past := time.Now().Add(-30 * time.Hour)
	mk := func(updates map[string]any) *models.Contact {
		c := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
		updates["last_message_at"] = past
		updates["whats_app_account"] = account.Name
		require.NoError(t, app.DB.Model(c).Updates(updates).Error)
		return c
	}
	optedOut := mk(map[string]any{"marketing_opt_out": true})
	dnc := mk(map[string]any{})
	status, _ := call(t, app.UpdateDoNotContact, org.ID, admin.ID, map[string]any{"do_not_contact": true}, map[string]string{"id": dnc.ID.String()})
	require.Equal(t, fasthttp.StatusOK, status)

	require.Equal(t, fasthttp.StatusOK, putNoReplyRule(t, app, org.ID, admin.ID, map[string]any{
		"enabled": true, "hours": 24, "template_id": tpl.ID, "template_params": map[string]string{"1": "x"},
	}))
	app.ProcessNoReplyLeads(time.Now())
	assert.Empty(t, wa.sent(), "no-reply rule honours opt-out and do-not-contact")

	var note models.ConversationNote
	require.NoError(t, app.DB.Where("contact_id = ? AND content LIKE ?", optedOut.ID, "%skipped template%").First(&note).Error)

	status, raw := call(t, app.TestNoReplyRule, org.ID, admin.ID, map[string]any{"contact_id": dnc.ID, "template_id": tpl.ID, "template_params": map[string]string{"1": "x"}}, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, status, string(raw))
}

func TestApp_RepeatDeals_CountedSeparately(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	p := listPipelines(t, app, org.ID, admin.ID)[0]
	won := p.Stages[4].ID
	c := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	id := map[string]string{"id": c.ID.String()}

	status, _ := call(t, app.StartNewDeal, org.ID, admin.ID, nil, id)
	assert.Equal(t, fasthttp.StatusConflict, status, "an open lead has no finished deal")

	status, _ = call(t, app.UpdateContactDeal, org.ID, admin.ID, map[string]any{"deal_value": 1000}, id)
	require.Equal(t, fasthttp.StatusOK, status)
	status, _ = moveContact(t, app, org.ID, admin.ID, c.ID, map[string]any{"stage_id": won})
	require.Equal(t, fasthttp.StatusOK, status)
	// Corrected after closing: belongs to the closed deal
	status, _ = call(t, app.UpdateContactDeal, org.ID, admin.ID, map[string]any{"deal_value": 1500}, id)
	require.Equal(t, fasthttp.StatusOK, status)

	status, raw := call(t, app.StartNewDeal, org.ID, admin.ID, nil, id)
	require.Equal(t, fasthttp.StatusOK, status, string(raw))
	var fresh models.Contact
	require.NoError(t, app.DB.First(&fresh, "id = ?", c.ID).Error)
	assert.Zero(t, fresh.DealValue)
	assert.Equal(t, p.Stages[0].ID, *fresh.StageID)

	status, _ = call(t, app.UpdateContactDeal, org.ID, admin.ID, map[string]any{"deal_value": 700}, id)
	require.Equal(t, fasthttp.StatusOK, status)
	status, _ = moveContact(t, app, org.ID, admin.ID, c.ID, map[string]any{"stage_id": won})
	require.Equal(t, fasthttp.StatusOK, status)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, admin.ID)
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.GetPipelineReport(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
	var resp struct {
		Data handlers.PipelineReport `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	require.NotEmpty(t, resp.Data.BySource)
	assert.EqualValues(t, 2, resp.Data.BySource[0].Won, "both sales count")
	assert.InDelta(t, 2200, resp.Data.BySource[0].WonValue, 0.001)
}

func TestApp_LeadSLA_Reassigns(t *testing.T) {
	app := newTestApp(t)
	app.Assigner = assignment.New(app.DB, app.Redis, app.Log)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	a1 := leadOpsAgent(t, app, org.ID)
	a2 := leadOpsAgent(t, app, org.ID)
	team := createSalesTeam(t, app, org.ID)
	for _, u := range []*models.User{a1, a2} {
		require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: team.ID, UserID: u.ID}).Error)
	}
	p := listPipelines(t, app, org.ID, admin.ID)[0]

	before := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	require.NoError(t, app.DB.Model(before).Updates(map[string]any{"assigned_user_id": a1.ID, "created_at": time.Now().Add(-48 * time.Hour)}).Error)

	setSalesSettings(t, app, org.ID, admin.ID, map[string]any{"lead_sla": map[string]any{"enabled": true, "minutes": 15, "team_id": team.ID}})

	late := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	answered := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	for _, c := range []*models.Contact{late, answered} {
		require.NoError(t, app.DB.Model(c).Update("assigned_user_id", a1.ID).Error)
	}
	require.NoError(t, app.DB.Create(&models.Message{OrganizationID: org.ID, WhatsAppAccount: "a", ContactID: answered.ID, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, SentByUserID: &a1.ID}).Error)

	now := time.Now()
	app.ProcessLeadSLA(now) // starts each owner's clock
	app.ProcessLeadSLA(now.Add(14 * time.Minute))
	owner := func(c *models.Contact) uuid.UUID {
		var got models.Contact
		require.NoError(t, app.DB.First(&got, "id = ?", c.ID).Error)
		return *got.AssignedUserID
	}
	assert.Equal(t, a1.ID, owner(late), "still inside the window")

	app.ProcessLeadSLA(now.Add(16 * time.Minute))
	assert.Equal(t, a2.ID, owner(late), "late lead moves to another agent")
	assert.Equal(t, a1.ID, owner(answered), "answered lead stays")
	assert.Equal(t, a1.ID, owner(before), "leads from before the rule are left alone")
}

type pushRecorder struct {
	mu   sync.Mutex
	urls []string
}

func (p *pushRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.urls = append(p.urls, r.URL.String())
	p.mu.Unlock()
	return &http.Response{StatusCode: 201, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
}

func TestApp_Push_SubscribeAndSend(t *testing.T) {
	priv, pub, err := webpush.GenerateVAPIDKeys()
	require.NoError(t, err)
	rec := &pushRecorder{}
	app := newTestApp(t)
	app.HTTPClient = &http.Client{Transport: rec}
	app.Config.Push.VAPIDPublicKey, app.Config.Push.VAPIDPrivateKey, app.Config.Push.Subject = pub, priv, "mailto:a@example.com"
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := leadOpsAgent(t, app, org.ID)

	keys := map[string]string{"p256dh": "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM", "auth": "tBHItJI5svbpez7KI4CCXg"}
	status, _ := call(t, app.SubscribePush, org.ID, agent.ID, map[string]any{"endpoint": "http://169.254.169.254/latest", "keys": keys}, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "only known push services are allowed")

	endpoint := "https://fcm.googleapis.com/fcm/send/" + uuid.NewString()
	status, raw := call(t, app.SubscribePush, org.ID, agent.ID, map[string]any{"endpoint": endpoint, "keys": keys}, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(raw))

	c := testutil.CreateTestContact(t, app.DB, org.ID)
	due := time.Now().Add(-time.Minute)
	task := models.Task{OrganizationID: org.ID, ContactID: c.ID, AssigneeID: &agent.ID, CreatedByID: agent.ID, Title: "Ring", DueAt: &due}
	require.NoError(t, app.DB.Create(&task).Error)
	app.ProcessDueTaskReminders(time.Now())
	require.Eventually(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.urls) == 1 && rec.urls[0] == endpoint
	}, 5*time.Second, 50*time.Millisecond)
}

func TestApp_TeamManager_SeesTeamOnly(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	manager := leadOpsAgent(t, app, org.ID)
	member := leadOpsAgent(t, app, org.ID)
	outsider := leadOpsAgent(t, app, org.ID)
	team := createSalesTeam(t, app, org.ID)
	require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: team.ID, UserID: manager.ID, Role: models.TeamRoleManager}).Error)
	require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: team.ID, UserID: member.ID, Role: models.TeamRoleAgent}).Error)
	p := listPipelines(t, app, org.ID, admin.ID)[0]

	mine := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	other := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	require.NoError(t, app.DB.Model(mine).Update("assigned_user_id", member.ID).Error)
	require.NoError(t, app.DB.Model(other).Update("assigned_user_id", outsider.ID).Error)

	can := func(userID uuid.UUID, c *models.Contact) int {
		status, _ := call(t, app.GetContactTimeline, org.ID, userID, nil, map[string]string{"id": c.ID.String()})
		return status
	}
	assert.Equal(t, fasthttp.StatusOK, can(manager.ID, mine), "manager sees a member's lead")
	assert.Equal(t, fasthttp.StatusNotFound, can(manager.ID, other), "but not another team's")
	assert.Equal(t, fasthttp.StatusNotFound, can(member.ID, other), "plain agents stay scoped")

	status, raw := call(t, app.GetTeamReport, org.ID, manager.ID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(raw))
	assert.Contains(t, string(raw), member.ID.String())
	assert.NotContains(t, string(raw), outsider.ID.String())

	status, _ = call(t, app.GetTeamReport, org.ID, outsider.ID, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, status)
}

func TestApp_TargetsAndForecast(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := leadOpsAdmin(t, app, org.ID)
	p := listPipelines(t, app, org.ID, admin.ID)[0]
	assert.Equal(t, 40, p.Stages[2].Probability)
	setSalesSettings(t, app, org.ID, admin.ID, map[string]any{"monthly_targets": map[string]float64{admin.ID.String(): 5000}})

	c := onStage(t, app, org.ID, admin.ID, p.Stages[2].ID)
	require.NoError(t, app.DB.Model(c).Updates(map[string]any{"deal_value": 1000, "assigned_user_id": admin.ID}).Error)
	wonLead := onStage(t, app, org.ID, admin.ID, p.Stages[0].ID)
	require.NoError(t, app.DB.Model(wonLead).Updates(map[string]any{"deal_value": 800, "assigned_user_id": admin.ID}).Error)
	status, _ := moveContact(t, app, org.ID, admin.ID, wonLead.ID, map[string]any{"stage_id": p.Stages[4].ID})
	require.Equal(t, fasthttp.StatusOK, status)

	status, raw := call(t, app.GetTeamReport, org.ID, admin.ID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, status, string(raw))
	var resp struct {
		Data handlers.TeamReport `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	assert.InDelta(t, 1000, resp.Data.OpenValue, 0.001)
	assert.InDelta(t, 400, resp.Data.Forecast, 0.001)
	var row handlers.TeamReportRow
	for _, a := range resp.Data.Agents {
		if a.UserID == admin.ID {
			row = a
		}
	}
	assert.InDelta(t, 5000, row.MonthlyTarget, 0.001)
	assert.InDelta(t, 800, row.WonValueMonth, 0.001)
}

func TestApp_Quotes(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := leadOpsAgent(t, app, org.ID)
	c := testutil.CreateTestContact(t, app.DB, org.ID)
	id := map[string]string{"id": c.ID.String()}
	items := []map[string]any{{"name": "<script>x</script>", "quantity": 2, "price": 100}}

	status, _ := call(t, app.CreateQuote, org.ID, agent.ID, map[string]any{"title": "Q", "items": items}, id)
	assert.Equal(t, fasthttp.StatusNotFound, status, "agent cannot quote a lead it cannot see")

	require.NoError(t, app.DB.Model(c).Update("assigned_user_id", agent.ID).Error)
	status, _ = call(t, app.CreateQuote, org.ID, agent.ID, map[string]any{"title": "Q", "items": items, "payment_url": "javascript:alert(1)"}, id)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "payment links must be https or upi")

	status, raw := call(t, app.CreateQuote, org.ID, agent.ID, map[string]any{"title": "Q", "items": items, "tax_percent": 18, "payment_url": "upi://pay?pa=x@y"}, id)
	require.Equal(t, fasthttp.StatusOK, status, string(raw))
	var resp struct {
		Data handlers.QuoteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	assert.InDelta(t, 236, resp.Data.Total, 0.001)
	token := resp.Data.Link[strings.LastIndex(resp.Data.Link, "/")+1:]

	send := map[string]string{"id": resp.Data.ID.String()}
	status, _ = call(t, app.SendQuote, org.ID, agent.ID, nil, send)
	assert.Equal(t, fasthttp.StatusConflict, status, "free text needs an open 24-hour window")

	page := testutil.NewGETRequest(t)
	testutil.SetPathParam(page, "token", token)
	require.NoError(t, app.PublicQuotePage(page))
	html := string(testutil.GetResponseBody(page))
	assert.NotContains(t, html, "<script>x", "item names are escaped")
	assert.Contains(t, html, "upi://pay")

	accept := testutil.NewJSONRequest(t, nil)
	testutil.SetPathParam(accept, "token", token)
	require.NoError(t, app.AcceptPublicQuote(accept))
	var got models.Contact
	require.NoError(t, app.DB.First(&got, "id = ?", c.ID).Error)
	assert.InDelta(t, 236, got.DealValue, 0.001, "accepting sets the deal value")

	status, raw = call(t, app.GetContactTimeline, org.ID, agent.ID, nil, id)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Contains(t, string(raw), `"kind":"quote"`)
	assert.Contains(t, string(raw), "accepted quote")
}
