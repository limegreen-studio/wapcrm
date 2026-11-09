package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/handlers"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type pipelineListResp struct {
	Data struct {
		Pipelines []handlers.PipelineResponse `json:"pipelines"`
	} `json:"data"`
}

type boardResp struct {
	Data struct {
		Pipeline handlers.PipelineResponse `json:"pipeline"`
		Columns  []handlers.BoardColumn    `json:"columns"`
	} `json:"data"`
}

type cardResp struct {
	Data handlers.BoardCard `json:"data"`
}

// listPipelines calls ListPipelines as the given user and returns the pipelines.
func listPipelines(t *testing.T, app *handlers.App, orgID, userID uuid.UUID) []handlers.PipelineResponse {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.ListPipelines(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var resp pipelineListResp
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data.Pipelines
}

// moveContact calls UpdateContactStage and returns the status code.
func moveContact(t *testing.T, app *handlers.App, orgID, userID, contactID uuid.UUID, body map[string]any) (int, handlers.BoardCard) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", contactID.String())
	require.NoError(t, app.UpdateContactStage(req))
	var resp cardResp
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Data
}

func TestApp_ListPipelines_CreatesDefault(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := chatRWRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	pipelines := listPipelines(t, app, org.ID, user.ID)
	require.Len(t, pipelines, 1)
	assert.Equal(t, "Sales", pipelines[0].Name)
	assert.True(t, pipelines[0].IsDefault)
	require.Len(t, pipelines[0].Stages, len(models.DefaultPipelineStages()))
	assert.Equal(t, "New", pipelines[0].Stages[0].Name)
	assert.Equal(t, models.StageKindWon, pipelines[0].Stages[4].Kind)

	// Second call must not create another pipeline
	assert.Len(t, listPipelines(t, app, org.ID, user.ID), 1)
}

func TestApp_CreatePipeline_RequiresSettingsWrite(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := chatRWRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	req := testutil.NewJSONRequest(t, map[string]any{
		"name":   "Partners",
		"stages": []map[string]any{{"name": "Intro"}},
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreatePipeline(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestApp_CreatePipeline_Validation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "settings", []string{"settings.general:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	cases := []map[string]any{
		{"name": "", "stages": []map[string]any{{"name": "A"}}},
		{"name": "P", "stages": []map[string]any{}},
		{"name": "P", "stages": []map[string]any{{"name": "A"}, {"name": "a"}}},
		{"name": "P", "stages": []map[string]any{{"name": "A", "kind": "maybe"}}},
		{"name": "P", "stages": []map[string]any{{"name": "A", "color": "pink"}}},
	}
	for _, body := range cases {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, org.ID, user.ID)
		require.NoError(t, app.CreatePipeline(req))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req), "body: %v", body)
	}

	req := testutil.NewJSONRequest(t, map[string]any{
		"name":       "Partners",
		"is_default": true,
		"stages":     []map[string]any{{"name": "Intro", "color": "blue"}, {"name": "Signed", "kind": "won"}},
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreatePipeline(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Data handlers.PipelineResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, "Partners", resp.Data.Name)
	require.Len(t, resp.Data.Stages, 2)
	assert.Equal(t, "Signed", resp.Data.Stages[1].Name)
	assert.Equal(t, 1, resp.Data.Stages[1].Position)
	assert.Equal(t, models.StageKindOpen, resp.Data.Stages[0].Kind)
}

func TestApp_UpdatePipeline_StageInUseConflict(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin-ish",
		[]string{"chat:read", "chat:write", "contacts:read", "settings.general:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	p := listPipelines(t, app, org.ID, user.ID)[0]
	status, _ := moveContact(t, app, org.ID, user.ID, contact.ID, map[string]any{"stage_id": p.Stages[0].ID})
	require.Equal(t, fasthttp.StatusOK, status)

	// Drop the first stage (which holds the contact): must be refused
	stages := []map[string]any{}
	for _, s := range p.Stages[1:] {
		stages = append(stages, map[string]any{"id": s.ID, "name": s.Name, "color": s.Color, "kind": s.Kind})
	}
	req := testutil.NewJSONRequest(t, map[string]any{"name": p.Name, "is_default": true, "stages": stages})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.UpdatePipeline(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))

	// Rename + reorder + add a stage: allowed
	stages = []map[string]any{
		{"id": p.Stages[1].ID, "name": "Reached", "color": "purple"},
		{"id": p.Stages[0].ID, "name": "Fresh", "color": "blue"},
		{"name": "Demo booked", "color": "yellow"},
	}
	req = testutil.NewJSONRequest(t, map[string]any{"name": "Main", "is_default": true, "stages": stages})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.UpdatePipeline(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Data handlers.PipelineResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, "Main", resp.Data.Name)
	require.Len(t, resp.Data.Stages, 3)
	assert.Equal(t, []string{"Reached", "Fresh", "Demo booked"},
		[]string{resp.Data.Stages[0].Name, resp.Data.Stages[1].Name, resp.Data.Stages[2].Name})
}

func TestApp_UpdateContactStage_MoveWritesNoteAndBoard(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent-plus",
		[]string{"chat:read", "chat:write", "contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	c1 := testutil.CreateTestContact(t, app.DB, org.ID)
	c2 := testutil.CreateTestContact(t, app.DB, org.ID)

	p := listPipelines(t, app, org.ID, user.ID)[0]
	newStage, contacted := p.Stages[0], p.Stages[1]

	status, card := moveContact(t, app, org.ID, user.ID, c1.ID, map[string]any{"stage_id": newStage.ID})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, newStage.ID, *card.StageID)
	assert.Equal(t, p.ID, *card.PipelineID)
	assert.NotNil(t, card.StageEnteredAt)
	assert.Equal(t, float64(1024), card.PipelinePosition)

	// Second contact appends after the first
	status, card = moveContact(t, app, org.ID, user.ID, c2.ID, map[string]any{"stage_id": newStage.ID})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, float64(2048), card.PipelinePosition)

	// Reorder inside the same stage: no new note
	status, _ = moveContact(t, app, org.ID, user.ID, c2.ID, map[string]any{"stage_id": newStage.ID, "position": 512})
	require.Equal(t, fasthttp.StatusOK, status)

	// Move to another stage
	status, card = moveContact(t, app, org.ID, user.ID, c1.ID, map[string]any{"stage_id": contacted.ID})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, contacted.ID, *card.StageID)

	var notes []models.ConversationNote
	require.NoError(t, app.DB.Where("contact_id = ?", c1.ID).Order("created_at ASC").Find(&notes).Error)
	require.Len(t, notes, 2)
	assert.Contains(t, notes[0].Content, `Added to pipeline "Sales" at stage "New"`)
	assert.Equal(t, "Stage changed: New → Contacted", notes[1].Content)

	var c2Notes int64
	app.DB.Model(&models.ConversationNote{}).Where("contact_id = ?", c2.ID).Count(&c2Notes)
	assert.Equal(t, int64(1), c2Notes, "reorder in same stage must not add a note")

	// Board reflects positions and totals
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.GetPipelineBoard(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var board boardResp
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &board))
	require.Len(t, board.Data.Columns, len(p.Stages))
	assert.Equal(t, int64(1), board.Data.Columns[0].Total)
	require.Len(t, board.Data.Columns[0].Cards, 1)
	assert.Equal(t, c2.ID, board.Data.Columns[0].Cards[0].ID)
	assert.Equal(t, int64(1), board.Data.Columns[1].Total)
	assert.Equal(t, c1.ID, board.Data.Columns[1].Cards[0].ID)
	assert.Empty(t, board.Data.Columns[2].Cards)

	// Remove from pipeline
	status, card = moveContact(t, app, org.ID, user.ID, c1.ID, map[string]any{"stage_id": nil})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Nil(t, card.StageID)
	assert.Nil(t, card.PipelineID)
}

func TestApp_UpdateContactStage_AgentScopedToAssigned(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := chatRWRole(t, app.DB, org.ID) // no contacts:read
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	mine := testutil.CreateTestContact(t, app.DB, org.ID)
	other := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(mine).Update("assigned_user_id", agent.ID).Error)

	p := listPipelines(t, app, org.ID, agent.ID)[0]

	status, _ := moveContact(t, app, org.ID, agent.ID, other.ID, map[string]any{"stage_id": p.Stages[0].ID})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	status, _ = moveContact(t, app, org.ID, agent.ID, mine.ID, map[string]any{"stage_id": p.Stages[0].ID})
	assert.Equal(t, fasthttp.StatusOK, status)
}

func TestApp_UpdateContactStage_CrossOrgStageRejected(t *testing.T) {
	app := newTestApp(t)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	roleA := testutil.CreateTestRoleWithKeys(t, app.DB, orgA.ID, "a", []string{"chat:read", "chat:write", "contacts:read"})
	roleB := chatRWRole(t, app.DB, orgB.ID)
	userA := testutil.CreateTestUser(t, app.DB, orgA.ID, testutil.WithRoleID(&roleA.ID))
	userB := testutil.CreateTestUser(t, app.DB, orgB.ID, testutil.WithRoleID(&roleB.ID))
	contactA := testutil.CreateTestContact(t, app.DB, orgA.ID)

	pB := listPipelines(t, app, orgB.ID, userB.ID)[0]

	status, _ := moveContact(t, app, orgA.ID, userA.ID, contactA.ID, map[string]any{"stage_id": pB.Stages[0].ID})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

func TestApp_DeletePipeline_ClearsContacts(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin-ish",
		[]string{"chat:read", "chat:write", "contacts:read", "settings.general:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	p := listPipelines(t, app, org.ID, user.ID)[0]
	status, _ := moveContact(t, app, org.ID, user.ID, contact.ID, map[string]any{"stage_id": p.Stages[2].ID})
	require.Equal(t, fasthttp.StatusOK, status)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.DeletePipeline(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var reloaded models.Contact
	require.NoError(t, app.DB.First(&reloaded, "id = ?", contact.ID).Error)
	assert.Nil(t, reloaded.PipelineID)
	assert.Nil(t, reloaded.StageID)
}

func TestApp_GetPipelineStats(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr",
		[]string{"chat:read", "chat:write", "contacts:read", "contacts:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	p := listPipelines(t, app, org.ID, user.ID)[0] // New, Contacted, Qualified, Negotiation, Won, Lost
	leads := []struct {
		phone, source, stage string
	}{
		{"701", "Website", "New"},
		{"702", "Website", "Won"},
		{"703", "Facebook", "Won"},
		{"704", "Facebook", "Lost"},
		{"705", "", "Contacted"},
	}
	for _, l := range leads {
		body := map[string]any{"phone_number": l.phone, "stage": l.stage}
		if l.source != "" {
			body["lead"] = map[string]string{"source": l.source}
		}
		status, _ := captureLead(t, app, org.ID, user.ID, body)
		require.Equal(t, fasthttp.StatusOK, status)
	}

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.GetPipelineStats(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))

	var resp struct {
		Data handlers.PipelineStats `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	s := resp.Data
	assert.Equal(t, int64(5), s.Total)
	assert.Equal(t, int64(2), s.Won)
	assert.Equal(t, int64(1), s.Lost)
	assert.Equal(t, int64(2), s.Open)
	require.NotNil(t, s.WinRate)
	assert.InDelta(t, 2.0/3.0, *s.WinRate, 0.0001)
	require.Len(t, s.ByStage, 6)
	assert.Equal(t, int64(1), s.ByStage[0].Count)
	assert.Equal(t, int64(2), s.ByStage[4].Count)
	assert.Equal(t, int64(0), s.StaleOpenLeads, "new leads without messages are not stale yet")

	bySource := map[string]handlers.SourceCount{}
	for _, sc := range s.BySource {
		bySource[sc.Source] = sc
	}
	assert.Equal(t, int64(2), bySource["Website"].Count)
	assert.Equal(t, int64(1), bySource["Website"].Won)
	assert.Equal(t, int64(2), bySource["Facebook"].Count)
	assert.Equal(t, int64(1), bySource[""].Count)
}

func TestApp_GetPipelineBoard_StagePaging(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr", []string{"chat:read", "chat:write", "contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	p := listPipelines(t, app, org.ID, user.ID)[0]

	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		status, _ := moveContact(t, app, org.ID, user.ID, c.ID, map[string]any{"stage_id": p.Stages[0].ID})
		require.Equal(t, fasthttp.StatusOK, status)
		ids = append(ids, c.ID)
	}

	board := func(params map[string]string) (int, boardResp) {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", p.ID.String())
		for k, v := range params {
			testutil.SetQueryParam(req, k, v)
		}
		require.NoError(t, app.GetPipelineBoard(req))
		var b boardResp
		_ = json.Unmarshal(testutil.GetResponseBody(req), &b)
		return testutil.GetResponseStatusCode(req), b
	}

	_, b := board(map[string]string{"limit": "2"})
	require.Len(t, b.Data.Columns, len(p.Stages))
	assert.Equal(t, int64(5), b.Data.Columns[0].Total)
	require.Len(t, b.Data.Columns[0].Cards, 2)

	_, b = board(map[string]string{"limit": "2", "stage_id": p.Stages[0].ID.String(), "offset": "2"})
	require.Len(t, b.Data.Columns, 1, "only the requested column")
	require.Len(t, b.Data.Columns[0].Cards, 2)
	assert.Equal(t, ids[2], b.Data.Columns[0].Cards[0].ID)
	assert.Equal(t, ids[3], b.Data.Columns[0].Cards[1].ID)

	status, _ := board(map[string]string{"stage_id": uuid.NewString()})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}
