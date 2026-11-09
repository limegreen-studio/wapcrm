package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/assignment"
	"github.com/limegreen-studio/wapcrm/internal/handlers"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func captureLead(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, body map[string]any) (int, handlers.CaptureLeadResponse) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.CaptureLead(req))
	var resp struct {
		Data handlers.CaptureLeadResponse `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Data
}

func leadCaptureUser(t *testing.T, app *handlers.App, orgID uuid.UUID) *models.User {
	t.Helper()
	role := testutil.CreateTestRoleWithKeys(t, app.DB, orgID, "api", []string{"contacts:write", "contacts:read", "chat:read", "chat:write"})
	return testutil.CreateTestUser(t, app.DB, orgID, testutil.WithRoleID(&role.ID))
}

func TestApp_CaptureLead_CreateThenUpsert(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := leadCaptureUser(t, app, org.ID)

	status, res := captureLead(t, app, org.ID, user.ID, map[string]any{
		"phone_number": "+91 98000-11111",
		"name":         "Website Lead",
		"lead":         map[string]string{"source": "Website", "budget": "60L"},
		"tags":         []string{"web"},
	})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.True(t, res.Created)
	assert.Equal(t, "New", res.StageName, "new leads land on the first stage of the default pipeline")
	require.NotNil(t, res.StageID)

	c := reloadContact(t, app, res.ContactID)
	assert.Equal(t, "919800011111", c.PhoneNumber)
	assert.Equal(t, "Website Lead", c.ProfileName)
	assert.Equal(t, map[string]any{"source": "Website", "budget": "60L"}, leadOf(c))

	var notes []models.ConversationNote
	require.NoError(t, app.DB.Where("contact_id = ?", c.ID).Order("created_at").Find(&notes).Error)
	require.Len(t, notes, 2)
	assert.Contains(t, notes[0].Content, "Added to pipeline")
	assert.Equal(t, "Lead captured via API (source: Website)", notes[1].Content)

	// Same phone again: update, no duplicate, stage kept, tags merged
	status, res2 := captureLead(t, app, org.ID, user.ID, map[string]any{
		"phone_number": "919800011111",
		"name":         "Other Name",
		"lead":         map[string]string{"city": "Rajkot"},
		"tags":         []string{"web", "ads"},
	})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.False(t, res2.Created)
	assert.Equal(t, res.ContactID, res2.ContactID)
	assert.Equal(t, *res.StageID, *res2.StageID)

	c = reloadContact(t, app, res.ContactID)
	assert.Equal(t, "Website Lead", c.ProfileName, "existing name is not overwritten")
	assert.Equal(t, models.JSONBArray{"web", "ads"}, c.Tags)
	assert.Equal(t, map[string]any{"source": "Website", "budget": "60L", "city": "Rajkot"}, leadOf(c))

	var count int64
	app.DB.Model(&models.Contact{}).Where("organization_id = ?", org.ID).Count(&count)
	assert.Equal(t, int64(1), count)

	// Explicit stage moves an existing lead
	status, res3 := captureLead(t, app, org.ID, user.ID, map[string]any{"phone_number": "919800011111", "stage": "qualified"})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "Qualified", res3.StageName)
}

func TestApp_CaptureLead_Validation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := leadCaptureUser(t, app, org.ID)

	cases := []map[string]any{
		{"phone_number": "abc"},
		{"phone_number": "123", "stage": "Nope"},
		{"phone_number": "123", "lead": map[string]string{"salary": "x"}},
		{"phone_number": "123", "team_id": uuid.New()},
		{"phone_number": "123", "assigned_user_id": uuid.New()},
		{"phone_number": "123", "follow_up_in_minutes": -5},
		{"phone_number": "123", "pipeline_id": uuid.New()},
	}
	for _, body := range cases {
		status, _ := captureLead(t, app, org.ID, user.ID, body)
		assert.Equal(t, fasthttp.StatusBadRequest, status, "body: %v", body)
	}

	var count int64
	app.DB.Model(&models.Contact{}).Where("organization_id = ?", org.ID).Count(&count)
	assert.Zero(t, count, "rejected requests must not create contacts")

	// chat-only role cannot capture leads
	agentRole := chatRWRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	status, _ := captureLead(t, app, org.ID, agent.ID, map[string]any{"phone_number": "123"})
	assert.Equal(t, fasthttp.StatusForbidden, status)
}

func TestApp_CaptureLead_RoundRobinAndFollowUp(t *testing.T) {
	app := newTestApp(t)
	app.Assigner = assignment.New(app.DB, app.Redis, app.Log)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := leadCaptureUser(t, app, org.ID)
	a1 := testutil.CreateTestUser(t, app.DB, org.ID)
	a2 := testutil.CreateTestUser(t, app.DB, org.ID)

	team := models.Team{
		BaseModel:          models.BaseModel{ID: uuid.New()},
		OrganizationID:     org.ID,
		Name:               "Sales " + uuid.NewString()[:8],
		AssignmentStrategy: models.AssignmentStrategyRoundRobin,
		IsActive:           true,
	}
	require.NoError(t, app.DB.Create(&team).Error)
	for _, u := range []*models.User{a1, a2} {
		require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: team.ID, UserID: u.ID, Role: models.TeamRoleAgent}).Error)
	}

	owners := map[uuid.UUID]int{}
	var firstContact uuid.UUID
	for i, phone := range []string{"911", "912", "913", "914"} {
		status, res := captureLead(t, app, org.ID, user.ID, map[string]any{
			"phone_number":         phone,
			"team_id":              team.ID,
			"follow_up_in_minutes": 15,
		})
		require.Equal(t, fasthttp.StatusOK, status)
		require.NotNil(t, res.AssignedUserID, "lead %d should be assigned", i)
		require.NotNil(t, res.TaskID)
		owners[*res.AssignedUserID]++
		if i == 0 {
			firstContact = res.ContactID
		}

		var task models.Task
		require.NoError(t, app.DB.First(&task, "id = ?", *res.TaskID).Error)
		assert.Equal(t, "Contact new lead", task.Title)
		require.NotNil(t, task.AssigneeID)
		assert.Equal(t, *res.AssignedUserID, *task.AssigneeID, "follow-up goes to the lead owner")
	}
	assert.Equal(t, 2, owners[a1.ID])
	assert.Equal(t, 2, owners[a2.ID])

	// Re-capturing an assigned lead keeps its owner
	before := reloadContact(t, app, firstContact).AssignedUserID
	_, res := captureLead(t, app, org.ID, user.ID, map[string]any{"phone_number": "911", "team_id": team.ID})
	require.NotNil(t, res.AssignedUserID)
	assert.Equal(t, *before, *res.AssignedUserID)
}

func TestApp_CaptureLead_ConcurrentSamePhone(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := leadCaptureUser(t, app, org.ID)
	listPipelines(t, app, org.ID, user.ID) // create the default pipeline up front
	// Production creates this index in database.CreateIndexes; the test schema does not.
	if err := app.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_org_phone ON contacts(organization_id, phone_number)`).Error; err != nil {
		t.Skipf("cannot create unique phone index in test DB: %v", err)
	}

	const n = 6
	statuses := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() {
			req := testutil.NewJSONRequest(t, map[string]any{"phone_number": "915550001"})
			testutil.SetAuthContext(req, org.ID, user.ID)
			_ = app.CaptureLead(req)
			statuses <- testutil.GetResponseStatusCode(req)
		}()
	}
	for i := 0; i < n; i++ {
		assert.Equal(t, fasthttp.StatusOK, <-statuses)
	}

	var count int64
	app.DB.Model(&models.Contact{}).Where("organization_id = ? AND phone_number = ?", org.ID, "915550001").Count(&count)
	assert.Equal(t, int64(1), count)
}
