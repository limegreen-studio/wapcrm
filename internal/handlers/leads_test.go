package handlers_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/handlers"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func putLeadFields(t *testing.T, app *handlers.App, orgID, userID, contactID uuid.UUID, fields map[string]string) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"fields": fields})
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", contactID.String())
	require.NoError(t, app.UpdateContactLeadFields(req))
	return testutil.GetResponseStatusCode(req)
}

func reloadContact(t *testing.T, app *handlers.App, id uuid.UUID) models.Contact {
	t.Helper()
	var c models.Contact
	require.NoError(t, app.DB.First(&c, "id = ?", id).Error)
	return c
}

func leadOf(c models.Contact) map[string]any {
	lead, _ := c.Metadata[models.LeadMetadataKey].(map[string]any)
	return lead
}

func TestApp_UpdateContactLeadFields_MergesAndKeepsOtherMetadata(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := chatRWRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
		"assigned_user_id": agent.ID,
		"metadata":         models.JSONB{"flow": map[string]any{"step": "x"}},
	}).Error)

	assert.Equal(t, fasthttp.StatusOK, putLeadFields(t, app, org.ID, agent.ID, contact.ID,
		map[string]string{"source": " Facebook ", "budget": "50L"}))
	assert.Equal(t, fasthttp.StatusOK, putLeadFields(t, app, org.ID, agent.ID, contact.ID,
		map[string]string{"city": "Pune", "budget": ""}))

	c := reloadContact(t, app, contact.ID)
	assert.Equal(t, map[string]any{"source": "Facebook", "city": "Pune"}, leadOf(c))
	assert.Equal(t, map[string]any{"step": "x"}, c.Metadata["flow"], "other metadata untouched")
}

func TestApp_UpdateContactLeadFields_Validation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := chatRWRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	mine := testutil.CreateTestContact(t, app.DB, org.ID)
	notMine := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(mine).Update("assigned_user_id", agent.ID).Error)

	assert.Equal(t, fasthttp.StatusBadRequest, putLeadFields(t, app, org.ID, agent.ID, mine.ID, map[string]string{"salary": "1"}))
	assert.Equal(t, fasthttp.StatusBadRequest, putLeadFields(t, app, org.ID, agent.ID, mine.ID, map[string]string{}))
	assert.Equal(t, fasthttp.StatusBadRequest, putLeadFields(t, app, org.ID, agent.ID, mine.ID, map[string]string{"city": strings.Repeat("x", 256)}))
	assert.Equal(t, fasthttp.StatusNotFound, putLeadFields(t, app, org.ID, agent.ID, notMine.ID, map[string]string{"city": "Pune"}))
}

func TestApp_UpdateContactLeadFields_ReplacesNonObjectLead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr", []string{"chat:read", "chat:write", "contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("metadata", models.JSONB{"lead": "legacy string"}).Error)

	assert.Equal(t, fasthttp.StatusOK, putLeadFields(t, app, org.ID, user.ID, contact.ID, map[string]string{"city": "Surat"}))
	assert.Equal(t, map[string]any{"city": "Surat"}, leadOf(reloadContact(t, app, contact.ID)))
}

func importContactsCSV(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, csv string, updateOnDup bool) map[string]any {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("table", "contacts"))
	if updateOnDup {
		require.NoError(t, w.WriteField("update_on_duplicate", "true"))
	}
	fw, err := w.CreateFormFile("file", "leads.csv")
	require.NoError(t, err)
	_, err = fw.Write([]byte(csv))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req := testutil.NewRequest(t)
	req.RequestCtx.Request.Header.SetMethod("POST")
	req.RequestCtx.Request.Header.SetContentType(w.FormDataContentType())
	req.RequestCtx.Request.SetBody(body.Bytes())
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.ImportData(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))

	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data
}

func TestApp_ImportContacts_LeadFieldsAndStage(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "imp",
		[]string{"contacts:import", "contacts:export", "chat:read", "chat:write", "contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	listPipelines(t, app, org.ID, user.ID) // creates the default "Sales" pipeline

	csv := "Phone Number,Name,Lead Source,lead_budget,Lead City,Stage\n" +
		"+919800000001,Kiran,Website,40L,Ahmedabad,qualified\n" +
		"919800000002,Meera,Referral,,Vadodara,\n" +
		"919800000003,Bad Stage,,,,Nope\n"
	res := importContactsCSV(t, app, org.ID, user.ID, csv, false)
	assert.EqualValues(t, 2, res["created"])
	assert.EqualValues(t, 1, res["errors"])

	var kiran models.Contact
	require.NoError(t, app.DB.Where("organization_id = ? AND phone_number = ?", org.ID, "919800000001").First(&kiran).Error)
	assert.Equal(t, map[string]any{"source": "Website", "budget": "40L", "city": "Ahmedabad"}, leadOf(kiran))
	require.NotNil(t, kiran.StageID)
	var stage models.PipelineStage
	require.NoError(t, app.DB.First(&stage, "id = ?", *kiran.StageID).Error)
	assert.Equal(t, "Qualified", stage.Name)
	assert.NotNil(t, kiran.StageEnteredAt)

	var meera models.Contact
	require.NoError(t, app.DB.Where("organization_id = ? AND phone_number = ?", org.ID, "919800000002").First(&meera).Error)
	assert.Equal(t, map[string]any{"source": "Referral", "city": "Vadodara"}, leadOf(meera))
	assert.Nil(t, meera.StageID)

	// Re-import with update: lead-only columns still update the contact
	res = importContactsCSV(t, app, org.ID, user.ID, "phone_number,lead_status\n919800000002,Hot\n", true)
	assert.EqualValues(t, 1, res["updated"])
	require.NoError(t, app.DB.First(&meera, "id = ?", meera.ID).Error)
	assert.Equal(t, "Hot", leadOf(meera)["status"])
	assert.Equal(t, "Referral", leadOf(meera)["source"])

	// Export includes the virtual columns
	body, _ := json.Marshal(map[string]any{
		"table":   "contacts",
		"columns": []string{"phone_number", "stage", "lead_source", "lead_status"},
	})
	req := testutil.NewRequest(t)
	req.RequestCtx.Request.Header.SetContentType("application/json")
	req.RequestCtx.Request.Header.SetMethod("POST")
	req.RequestCtx.Request.SetBody(body)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.ExportData(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
	out := string(testutil.GetResponseBody(req))
	assert.Contains(t, out, "Phone Number,Stage,Lead Source,Lead Status")
	assert.Contains(t, out, "919800000001,Qualified,Website,")
	assert.Contains(t, out, "919800000002,,Referral,Hot")
}

func TestApp_LeadFieldDefs_CustomPerOrg(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "adm",
		[]string{"settings.general:write", "chat:read", "chat:write", "contacts:read", "contacts:import", "contacts:export"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	agentRole := chatRWRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	put := func(userID uuid.UUID, fields any) int {
		req := testutil.NewJSONRequest(t, map[string]any{"fields": fields})
		testutil.SetAuthContext(req, org.ID, userID)
		require.NoError(t, app.UpdateLeadFieldDefs(req))
		return testutil.GetResponseStatusCode(req)
	}

	assert.Equal(t, fasthttp.StatusForbidden, put(agent.ID, []map[string]any{{"key": "x", "label": "X"}}))
	assert.Equal(t, fasthttp.StatusBadRequest, put(user.ID, []map[string]any{{"key": "Bad Key", "label": "X"}}))
	assert.Equal(t, fasthttp.StatusBadRequest, put(user.ID, []map[string]any{{"key": "a"}, {"key": "a"}}))
	assert.Equal(t, fasthttp.StatusBadRequest, put(user.ID, []map[string]any{}))
	require.Equal(t, fasthttp.StatusOK, put(user.ID, []map[string]any{
		{"key": "source", "label": "Source", "options": []string{"Ads", " ", "Referral"}},
		{"key": "property_type", "label": "Property Type"},
	}))

	// Settings key written without clobbering other settings
	require.NoError(t, app.DB.Exec(`UPDATE organizations SET settings = settings || '{"timezone":"Asia/Kolkata"}' WHERE id = ?`, org.ID).Error)
	require.Equal(t, fasthttp.StatusOK, put(user.ID, []map[string]any{
		{"key": "source", "label": "Source", "options": []string{"Ads", "Referral"}},
		{"key": "property_type", "label": "Property Type"},
	}))
	var o models.Organization
	require.NoError(t, app.DB.First(&o, "id = ?", org.ID).Error)
	assert.Equal(t, "Asia/Kolkata", o.Settings["timezone"])

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.ListLeadFields(req))
	var list struct {
		Data struct {
			Fields []models.LeadField `json:"fields"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &list))
	require.Len(t, list.Data.Fields, 2)
	assert.Equal(t, []string{"Ads", "Referral"}, list.Data.Fields[0].Options)

	// Only the org's keys are accepted now
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	assert.Equal(t, fasthttp.StatusBadRequest, putLeadFields(t, app, org.ID, user.ID, contact.ID, map[string]string{"budget": "1"}))
	assert.Equal(t, fasthttp.StatusOK, putLeadFields(t, app, org.ID, user.ID, contact.ID, map[string]string{"property_type": "Villa"}))

	// CSV columns follow the org's fields (by key and by "Lead <Label>" header)
	res := importContactsCSV(t, app, org.ID, user.ID, "phone_number,Lead Property Type,lead_budget\n919811100001,Plot,9L\n", false)
	assert.EqualValues(t, 1, res["created"])
	var c models.Contact
	require.NoError(t, app.DB.Where("organization_id = ? AND phone_number = ?", org.ID, "919811100001").First(&c).Error)
	assert.Equal(t, map[string]any{"property_type": "Plot"}, leadOf(c), "lead_budget is not an org field and is ignored")

	body, _ := json.Marshal(map[string]any{"table": "contacts", "columns": []string{"phone_number", "lead_property_type"}})
	ex := testutil.NewRequest(t)
	ex.RequestCtx.Request.Header.SetContentType("application/json")
	ex.RequestCtx.Request.Header.SetMethod("POST")
	ex.RequestCtx.Request.SetBody(body)
	testutil.SetAuthContext(ex, org.ID, user.ID)
	require.NoError(t, app.ExportData(ex))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(ex), string(testutil.GetResponseBody(ex)))
	out := string(testutil.GetResponseBody(ex))
	assert.Contains(t, out, "Phone Number,Lead Property Type")
	assert.Contains(t, out, "919811100001,Plot")
}
