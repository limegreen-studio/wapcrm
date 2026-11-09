package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/handlers"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type taskResp struct {
	Data handlers.TaskResponse `json:"data"`
}

type taskListResp struct {
	Data struct {
		Tasks []handlers.TaskResponse `json:"tasks"`
		Total int                     `json:"total"`
	} `json:"data"`
}

func createTask(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, body map[string]any) (int, handlers.TaskResponse) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.CreateTask(req))
	var resp taskResp
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Data
}

func listTasks(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, params map[string]string) (int, taskListResp) {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	for k, v := range params {
		testutil.SetQueryParam(req, k, v)
	}
	require.NoError(t, app.ListTasks(req))
	var resp taskListResp
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp
}

func updateTask(t *testing.T, app *handlers.App, orgID, userID, taskID uuid.UUID, body map[string]any) (int, handlers.TaskResponse) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", taskID.String())
	require.NoError(t, app.UpdateTask(req))
	var resp taskResp
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Data
}

func TestApp_CreateTask_DefaultsAndValidation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr", []string{"chat:read", "chat:write", "contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	status, _ := createTask(t, app, org.ID, user.ID, map[string]any{"contact_id": contact.ID, "title": "  "})
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	status, _ = createTask(t, app, org.ID, user.ID, map[string]any{"contact_id": uuid.New(), "title": "Call"})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	status, _ = createTask(t, app, org.ID, user.ID, map[string]any{"contact_id": contact.ID, "title": "Call", "assignee_id": uuid.New()})
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	due := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	status, task := createTask(t, app, org.ID, user.ID, map[string]any{
		"contact_id": contact.ID, "title": " Call back ", "due_at": due,
	})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "Call back", task.Title)
	require.NotNil(t, task.AssigneeID)
	assert.Equal(t, user.ID, *task.AssigneeID, "assignee defaults to creator")
	assert.Equal(t, contact.ID, task.ContactID)
	require.NotNil(t, task.DueAt)
	assert.True(t, due.Equal(*task.DueAt))
	assert.False(t, task.Overdue)
	assert.False(t, task.Done)
}

func TestApp_ListTasks_FiltersAndOrder(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr", []string{"chat:read", "chat:write", "contacts:read"})
	me := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	now := time.Now()
	_, overdue := createTask(t, app, org.ID, me.ID, map[string]any{"contact_id": contact.ID, "title": "overdue", "due_at": now.Add(-time.Hour)})
	_, later := createTask(t, app, org.ID, me.ID, map[string]any{"contact_id": contact.ID, "title": "later", "due_at": now.Add(48 * time.Hour)})
	_, noDue := createTask(t, app, org.ID, me.ID, map[string]any{"contact_id": contact.ID, "title": "no due"})
	_, theirs := createTask(t, app, org.ID, me.ID, map[string]any{"contact_id": contact.ID, "title": "theirs", "assignee_id": other.ID, "due_at": now.Add(time.Hour)})
	_, done := createTask(t, app, org.ID, me.ID, map[string]any{"contact_id": contact.ID, "title": "done"})
	status, _ := updateTask(t, app, org.ID, me.ID, done.ID, map[string]any{"done": true})
	require.Equal(t, fasthttp.StatusOK, status)

	// Default: open tasks, overdue first, no-due last
	status, resp := listTasks(t, app, org.ID, me.ID, nil)
	require.Equal(t, fasthttp.StatusOK, status)
	require.Equal(t, 4, resp.Data.Total)
	titles := []string{}
	for _, task := range resp.Data.Tasks {
		titles = append(titles, task.Title)
	}
	assert.Equal(t, []string{"overdue", "theirs", "later", "no due"}, titles)
	assert.True(t, resp.Data.Tasks[0].Overdue)

	_, resp = listTasks(t, app, org.ID, me.ID, map[string]string{"assignee_id": "me"})
	assert.Equal(t, 3, resp.Data.Total)

	_, resp = listTasks(t, app, org.ID, me.ID, map[string]string{"assignee_id": other.ID.String()})
	require.Equal(t, 1, resp.Data.Total)
	assert.Equal(t, theirs.ID, resp.Data.Tasks[0].ID)

	// Overdue = due_before now
	_, resp = listTasks(t, app, org.ID, me.ID, map[string]string{"due_before": now.Format(time.RFC3339)})
	require.Equal(t, 1, resp.Data.Total)
	assert.Equal(t, overdue.ID, resp.Data.Tasks[0].ID)

	// Upcoming window
	_, resp = listTasks(t, app, org.ID, me.ID, map[string]string{
		"due_after":  now.Format(time.RFC3339),
		"due_before": now.Add(72 * time.Hour).Format(time.RFC3339),
	})
	assert.Equal(t, 2, resp.Data.Total)

	_, resp = listTasks(t, app, org.ID, me.ID, map[string]string{"status": "done"})
	require.Equal(t, 1, resp.Data.Total)
	assert.True(t, resp.Data.Tasks[0].Done)

	status, _ = listTasks(t, app, org.ID, me.ID, map[string]string{"due_before": "tomorrow"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	_ = later
	_ = noDue
}

func TestApp_Tasks_AgentScoping(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	mgrRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr", []string{"chat:read", "chat:write", "contacts:read"})
	agentRole := chatRWRole(t, app.DB, org.ID)
	mgr := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&mgrRole.ID))
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	mine := testutil.CreateTestContact(t, app.DB, org.ID)
	hidden := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(mine).Update("assigned_user_id", agent.ID).Error)

	_, onMine := createTask(t, app, org.ID, mgr.ID, map[string]any{"contact_id": mine.ID, "title": "on my contact", "assignee_id": mgr.ID})
	_, assignedToAgent := createTask(t, app, org.ID, mgr.ID, map[string]any{"contact_id": hidden.ID, "title": "assigned to me", "assignee_id": agent.ID})
	_, invisible := createTask(t, app, org.ID, mgr.ID, map[string]any{"contact_id": hidden.ID, "title": "invisible"})

	_, resp := listTasks(t, app, org.ID, agent.ID, nil)
	ids := map[uuid.UUID]bool{}
	for _, task := range resp.Data.Tasks {
		ids[task.ID] = true
	}
	assert.True(t, ids[onMine.ID])
	assert.True(t, ids[assignedToAgent.ID])
	assert.False(t, ids[invisible.ID])

	status, _ := updateTask(t, app, org.ID, agent.ID, invisible.ID, map[string]any{"done": true})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	status, _ = createTask(t, app, org.ID, agent.ID, map[string]any{"contact_id": hidden.ID, "title": "nope"})
	assert.Equal(t, fasthttp.StatusNotFound, status)
}

func TestApp_UpdateTask_DoneReopenAndDueReset(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr", []string{"chat:read", "chat:write", "contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	_, task := createTask(t, app, org.ID, user.ID, map[string]any{"contact_id": contact.ID, "title": "Call", "due_at": time.Now().Add(-time.Minute)})
	app.ProcessDueTaskReminders(time.Now())

	var stored models.Task
	require.NoError(t, app.DB.First(&stored, "id = ?", task.ID).Error)
	require.NotNil(t, stored.ReminderSentAt)

	newDue := time.Now().Add(time.Hour)
	status, updated := updateTask(t, app, org.ID, user.ID, task.ID, map[string]any{"due_at": newDue, "title": "Call again"})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "Call again", updated.Title)
	var rearmed models.Task
	require.NoError(t, app.DB.First(&rearmed, "id = ?", task.ID).Error)
	assert.Nil(t, rearmed.ReminderSentAt, "new due time re-arms the reminder")

	status, updated = updateTask(t, app, org.ID, user.ID, task.ID, map[string]any{"done": true})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.True(t, updated.Done)
	require.NotNil(t, updated.CompletedAt)

	status, updated = updateTask(t, app, org.ID, user.ID, task.ID, map[string]any{"done": false})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.False(t, updated.Done)

	status, updated = updateTask(t, app, org.ID, user.ID, task.ID, map[string]any{"clear_due_at": true, "clear_assignee": true})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Nil(t, updated.DueAt)
	assert.Nil(t, updated.AssigneeID)

	status, _ = updateTask(t, app, org.ID, user.ID, task.ID, map[string]any{})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

func TestApp_ProcessDueTaskReminders_FiresOnce(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr", []string{"chat:read", "chat:write", "contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	_, due := createTask(t, app, org.ID, user.ID, map[string]any{"contact_id": contact.ID, "title": "due", "due_at": time.Now().Add(-time.Minute)})
	_, future := createTask(t, app, org.ID, user.ID, map[string]any{"contact_id": contact.ID, "title": "future", "due_at": time.Now().Add(time.Hour)})
	_, doneTask := createTask(t, app, org.ID, user.ID, map[string]any{"contact_id": contact.ID, "title": "done", "due_at": time.Now().Add(-time.Hour)})
	updateTask(t, app, org.ID, user.ID, doneTask.ID, map[string]any{"done": true})

	app.ProcessDueTaskReminders(time.Now())

	var first models.Task
	require.NoError(t, app.DB.First(&first, "id = ?", due.ID).Error)
	require.NotNil(t, first.ReminderSentAt)

	// Second run must not touch an already-reminded task
	app.ProcessDueTaskReminders(time.Now().Add(time.Minute))
	var second models.Task
	require.NoError(t, app.DB.First(&second, "id = ?", due.ID).Error)
	assert.True(t, first.ReminderSentAt.Equal(*second.ReminderSentAt))

	var f, d models.Task
	require.NoError(t, app.DB.First(&f, "id = ?", future.ID).Error)
	require.NoError(t, app.DB.First(&d, "id = ?", doneTask.ID).Error)
	assert.Nil(t, f.ReminderSentAt)
	assert.Nil(t, d.ReminderSentAt)
}

func TestApp_PipelineBoard_IncludesTaskSummary(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr", []string{"chat:read", "chat:write", "contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	p := listPipelines(t, app, org.ID, user.ID)[0]
	status, _ := moveContact(t, app, org.ID, user.ID, contact.ID, map[string]any{"stage_id": p.Stages[0].ID})
	require.Equal(t, fasthttp.StatusOK, status)

	soon := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	createTask(t, app, org.ID, user.ID, map[string]any{"contact_id": contact.ID, "title": "a", "due_at": soon})
	createTask(t, app, org.ID, user.ID, map[string]any{"contact_id": contact.ID, "title": "b", "due_at": soon.Add(time.Hour)})

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.GetPipelineBoard(req))
	var board boardResp
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &board))
	card := board.Data.Columns[0].Cards[0]
	assert.Equal(t, 2, card.OpenTasks)
	require.NotNil(t, card.NextTaskDueAt)
	assert.True(t, soon.Equal(*card.NextTaskDueAt))

	// The move response (also the WS payload) keeps the summary
	status, moved := moveContact(t, app, org.ID, user.ID, contact.ID, map[string]any{"stage_id": p.Stages[1].ID})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, 2, moved.OpenTasks)
	require.NotNil(t, moved.NextTaskDueAt)
}
