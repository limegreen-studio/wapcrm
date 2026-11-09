package handlers

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/limegreen-studio/wapcrm/internal/utils"
	"github.com/limegreen-studio/wapcrm/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// Tasks use chat permissions, like pipelines: chat:read to list, chat:write to
// create/update/delete. Users without contacts:read only see tasks assigned to
// them or tasks on contacts they can see.

// CreateTaskRequest is the request body for creating a task.
// AssigneeID defaults to the creating user.
type CreateTaskRequest struct {
	ContactID  uuid.UUID  `json:"contact_id"`
	Title      string     `json:"title"`
	Notes      string     `json:"notes"`
	DueAt      *time.Time `json:"due_at"`
	AssigneeID *uuid.UUID `json:"assignee_id"`
}

// UpdateTaskRequest is the request body for updating a task. Nil fields are
// left unchanged; ClearDueAt / ClearAssignee unset those fields.
type UpdateTaskRequest struct {
	Title         *string    `json:"title"`
	Notes         *string    `json:"notes"`
	DueAt         *time.Time `json:"due_at"`
	ClearDueAt    bool       `json:"clear_due_at"`
	AssigneeID    *uuid.UUID `json:"assignee_id"`
	ClearAssignee bool       `json:"clear_assignee"`
	Done          *bool      `json:"done"`
}

// TaskResponse is the API response for a task.
type TaskResponse struct {
	ID            uuid.UUID  `json:"id"`
	ContactID     uuid.UUID  `json:"contact_id"`
	ContactName   string     `json:"contact_name"`
	ContactPhone  string     `json:"contact_phone"`
	AssigneeID    *uuid.UUID `json:"assignee_id,omitempty"`
	AssigneeName  string     `json:"assignee_name,omitempty"`
	CreatedByID   uuid.UUID  `json:"created_by_id"`
	CreatedByName string     `json:"created_by_name,omitempty"`
	Title         string     `json:"title"`
	Notes         string     `json:"notes"`
	DueAt         *time.Time `json:"due_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Done          bool       `json:"done"`
	Overdue       bool       `json:"overdue"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// TaskDueEventData is the webhook payload for task.due.
type TaskDueEventData struct {
	TaskID       string  `json:"task_id"`
	Title        string  `json:"title"`
	DueAt        string  `json:"due_at"`
	ContactID    string  `json:"contact_id"`
	ContactPhone string  `json:"contact_phone"`
	ContactName  string  `json:"contact_name"`
	AssigneeID   *string `json:"assignee_id"`
}

// ListTasks returns tasks visible to the user.
//
// Query params:
//   - status: open (default), done, all
//   - assignee_id: "me", "unassigned" or a user uuid
//   - contact_id: only tasks on this contact
//   - due_before, due_after: RFC3339 bounds on due_at (client computes "today"
//     in its own timezone); due_before alone also matches overdue tasks
//   - page, limit
func (a *App) ListTasks(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionRead)
	if err != nil {
		return nil
	}

	args := r.RequestCtx.QueryArgs()
	pg := parsePagination(r)

	q := a.scopeVisibleTasks(a.DB.Model(&models.Task{}).Where("tasks.organization_id = ?", orgID), userID, orgID)

	status := string(args.Peek("status"))
	switch status {
	case "", "open":
		q = q.Where("tasks.completed_at IS NULL")
	case "done":
		q = q.Where("tasks.completed_at IS NOT NULL")
	case "all":
	default:
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "status must be open, done or all", nil, "")
	}

	switch assignee := string(args.Peek("assignee_id")); assignee {
	case "":
	case "me":
		q = q.Where("tasks.assignee_id = ?", userID)
	case "unassigned":
		q = q.Where("tasks.assignee_id IS NULL")
	default:
		id, err := uuid.Parse(assignee)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid assignee_id", nil, "")
		}
		q = q.Where("tasks.assignee_id = ?", id)
	}

	if c := string(args.Peek("contact_id")); c != "" {
		id, err := uuid.Parse(c)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid contact_id", nil, "")
		}
		q = q.Where("tasks.contact_id = ?", id)
	}

	for param, op := range map[string]string{"due_before": "<", "due_after": ">="} {
		v := string(args.Peek(param))
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid "+param+", expected RFC3339", nil, "")
		}
		q = q.Where("tasks.due_at "+op+" ?", t)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		a.Log.Error("Failed to count tasks", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list tasks", nil, "")
	}

	if status == "done" {
		q = q.Order("tasks.completed_at DESC")
	} else {
		q = q.Order("tasks.completed_at IS NOT NULL, tasks.due_at ASC NULLS LAST, tasks.created_at ASC")
	}

	var tasks []models.Task
	if err := q.Preload("Contact").Preload("Assignee").Preload("CreatedBy").
		Offset(pg.Offset).Limit(pg.Limit).Find(&tasks).Error; err != nil {
		a.Log.Error("Failed to list tasks", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list tasks", nil, "")
	}

	shouldMask := a.ShouldMaskPhoneNumbers(orgID)
	result := make([]TaskResponse, len(tasks))
	for i := range tasks {
		result[i] = taskToResponse(&tasks[i], shouldMask)
	}

	return r.SendEnvelope(listEnvelope("tasks", result, total, pg))
}

// CreateTask creates a follow-up task on a contact the user can see.
func (a *App) CreateTask(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req CreateTaskRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "title is required", nil, "")
	}
	if len(req.Title) > 255 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "title must be at most 255 characters", nil, "")
	}

	if !a.contactVisible(req.ContactID, userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	assigneeID := &userID
	if req.AssigneeID != nil {
		if !a.isOrgMember(*req.AssigneeID, orgID) {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Assignee not found", nil, "")
		}
		assigneeID = req.AssigneeID
	}

	task := models.Task{
		OrganizationID: orgID,
		ContactID:      req.ContactID,
		AssigneeID:     assigneeID,
		CreatedByID:    userID,
		Title:          req.Title,
		Notes:          req.Notes,
		DueAt:          req.DueAt,
	}
	if err := a.DB.Create(&task).Error; err != nil {
		a.Log.Error("Failed to create task", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create task", nil, "")
	}

	resp, err := a.loadTaskResponse(task.ID, orgID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load task", nil, "")
	}
	a.broadcastTaskChanged(orgID, resp, false)
	return r.SendEnvelope(resp)
}

// UpdateTask edits, completes or reopens a task.
func (a *App) UpdateTask(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}

	task, err := a.findVisibleTask(r, userID, orgID)
	if err != nil {
		return nil
	}

	var req UpdateTaskRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	updates := map[string]any{}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "title is required", nil, "")
		}
		if len(title) > 255 {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "title must be at most 255 characters", nil, "")
		}
		updates["title"] = title
	}
	if req.Notes != nil {
		updates["notes"] = *req.Notes
	}
	if req.ClearDueAt {
		updates["due_at"] = nil
		updates["reminder_sent_at"] = nil
	} else if req.DueAt != nil {
		updates["due_at"] = *req.DueAt
		// A new due time gets a new reminder
		updates["reminder_sent_at"] = nil
	}
	if req.ClearAssignee {
		updates["assignee_id"] = nil
	} else if req.AssigneeID != nil {
		if !a.isOrgMember(*req.AssigneeID, orgID) {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Assignee not found", nil, "")
		}
		updates["assignee_id"] = *req.AssigneeID
	}
	if req.Done != nil {
		if *req.Done && task.CompletedAt == nil {
			updates["completed_at"] = time.Now()
		} else if !*req.Done && task.CompletedAt != nil {
			updates["completed_at"] = nil
		}
	}

	if len(updates) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "No fields to update", nil, "")
	}

	if err := a.DB.Model(task).Updates(updates).Error; err != nil {
		a.Log.Error("Failed to update task", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update task", nil, "")
	}

	resp, err := a.loadTaskResponse(task.ID, orgID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load task", nil, "")
	}
	a.broadcastTaskChanged(orgID, resp, false)
	return r.SendEnvelope(resp)
}

// DeleteTask soft-deletes a task.
func (a *App) DeleteTask(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}

	task, err := a.findVisibleTask(r, userID, orgID)
	if err != nil {
		return nil
	}

	if err := a.DB.Delete(task).Error; err != nil {
		a.Log.Error("Failed to delete task", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete task", nil, "")
	}

	a.broadcastTaskChanged(orgID, TaskResponse{ID: task.ID, ContactID: task.ContactID, AssigneeID: task.AssigneeID}, true)
	return r.SendEnvelope(map[string]any{"message": "Task deleted"})
}

// ProcessDueTaskReminders fires one reminder per task whose due time has
// passed: a WebSocket "task_due" to the assignee and the task.due webhook.
// Tasks are claimed with an UPDATE ... RETURNING so that several server
// instances never send the same reminder twice.
func (a *App) ProcessDueTaskReminders(now time.Time) {
	var due []models.Task
	err := a.DB.Raw(`
		UPDATE tasks SET reminder_sent_at = ?
		WHERE id IN (
			SELECT id FROM tasks
			WHERE completed_at IS NULL AND reminder_sent_at IS NULL AND deleted_at IS NULL
			  AND due_at IS NOT NULL AND due_at <= ?
			ORDER BY due_at
			LIMIT 500
			FOR UPDATE SKIP LOCKED
		)
		RETURNING *`, now, now).Scan(&due).Error
	if err != nil {
		a.Log.Error("Failed to claim due task reminders", "error", err)
		return
	}

	for i := range due {
		task := &due[i]
		var contact models.Contact
		a.DB.Unscoped().Where("id = ?", task.ContactID).First(&contact)
		task.Contact = &contact

		shouldMask := a.ShouldMaskPhoneNumbers(task.OrganizationID)
		resp := taskToResponse(task, shouldMask)

		if a.WSHub != nil && task.AssigneeID != nil {
			a.WSHub.BroadcastToUser(task.OrganizationID, *task.AssigneeID, websocket.WSMessage{
				Type:    websocket.TypeTaskDue,
				Payload: resp,
			})
		}

		data := TaskDueEventData{
			TaskID:       task.ID.String(),
			Title:        task.Title,
			ContactID:    task.ContactID.String(),
			ContactPhone: contact.PhoneNumber,
			ContactName:  contact.ProfileName,
		}
		if task.DueAt != nil {
			data.DueAt = task.DueAt.Format(time.RFC3339)
		}
		if task.AssigneeID != nil {
			id := task.AssigneeID.String()
			data.AssigneeID = &id
		}
		a.DispatchWebhook(task.OrganizationID, models.WebhookEventTaskDue, data)
		a.emailTaskDue(task, resp.ContactName, resp.ContactPhone)
		if task.AssigneeID != nil {
			a.notifyUser(task.OrganizationID, *task.AssigneeID, "Follow-up due", task.Title, "/chat/"+task.ContactID.String())
		}
	}
}

// scopeVisibleTasks limits a tasks query to what the user may see.
func (a *App) scopeVisibleTasks(q *gorm.DB, userID, orgID uuid.UUID) *gorm.DB {
	if a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID) {
		return q
	}
	visibleContacts := a.scopeAssignedContact(
		a.DB.Model(&models.Contact{}).Select("id").Where("organization_id = ?", orgID), userID, orgID)
	return q.Where("tasks.assignee_id = ? OR tasks.contact_id IN (?)", userID, visibleContacts)
}

// contactVisible reports whether the user can see the contact.
func (a *App) contactVisible(contactID, userID, orgID uuid.UUID) bool {
	var count int64
	q := a.DB.Model(&models.Contact{}).Where("id = ? AND organization_id = ?", contactID, orgID)
	a.scopeAssignedContact(q, userID, orgID).Count(&count)
	return count > 0
}

// isOrgMember reports whether the user belongs to the organization.
func (a *App) isOrgMember(userID, orgID uuid.UUID) bool {
	var count int64
	a.DB.Model(&models.User{}).
		Where("id = ? AND (organization_id = ? OR id IN (?))", userID, orgID,
			a.DB.Model(&models.UserOrganization{}).Select("user_id").Where("organization_id = ?", orgID)).
		Count(&count)
	return count > 0
}

// findVisibleTask loads the task from the {id} path param, or sends 404.
func (a *App) findVisibleTask(r *fastglue.Request, userID, orgID uuid.UUID) (*models.Task, error) {
	taskID, err := parsePathUUID(r, "id", "task")
	if err != nil {
		return nil, err
	}
	var task models.Task
	q := a.DB.Model(&models.Task{}).Where("tasks.id = ? AND tasks.organization_id = ?", taskID, orgID)
	if err := a.scopeVisibleTasks(q, userID, orgID).First(&task).Error; err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Task not found", nil, "")
		return nil, errEnvelopeSent
	}
	return &task, nil
}

func (a *App) loadTaskResponse(taskID, orgID uuid.UUID) (TaskResponse, error) {
	var task models.Task
	if err := a.DB.Preload("Contact").Preload("Assignee").Preload("CreatedBy").
		Where("id = ? AND organization_id = ?", taskID, orgID).First(&task).Error; err != nil {
		return TaskResponse{}, err
	}
	return taskToResponse(&task, a.ShouldMaskPhoneNumbers(orgID)), nil
}

// broadcastTaskChanged tells the org's clients a task changed so open task
// lists refresh. Only ids go out: every client refetches through its own
// permission scope, so task details never reach users who cannot see them.
func (a *App) broadcastTaskChanged(orgID uuid.UUID, task TaskResponse, deleted bool) {
	if a.WSHub == nil {
		return
	}
	a.WSHub.BroadcastToOrg(orgID, websocket.WSMessage{
		Type:    websocket.TypeTaskChanged,
		Payload: map[string]any{"task_id": task.ID, "contact_id": task.ContactID, "deleted": deleted},
	})
}

func taskToResponse(t *models.Task, shouldMask bool) TaskResponse {
	resp := TaskResponse{
		ID:          t.ID,
		ContactID:   t.ContactID,
		AssigneeID:  t.AssigneeID,
		CreatedByID: t.CreatedByID,
		Title:       t.Title,
		Notes:       t.Notes,
		DueAt:       t.DueAt,
		CompletedAt: t.CompletedAt,
		Done:        t.CompletedAt != nil,
		Overdue:     t.CompletedAt == nil && t.DueAt != nil && t.DueAt.Before(time.Now()),
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
	if t.Contact != nil {
		resp.ContactPhone = t.Contact.PhoneNumber
		resp.ContactName = t.Contact.ProfileName
		if shouldMask {
			resp.ContactPhone = utils.MaskPhoneNumber(resp.ContactPhone)
			resp.ContactName = utils.MaskIfPhoneNumber(resp.ContactName)
		}
	}
	if t.Assignee != nil {
		resp.AssigneeName = t.Assignee.FullName
	}
	if t.CreatedBy != nil {
		resp.CreatedByName = t.CreatedBy.FullName
	}
	return resp
}
