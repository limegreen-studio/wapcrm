package models

import (
	"time"

	"github.com/google/uuid"
)

// Task is a follow-up linked to a contact, e.g. "Call back on Monday".
type Task struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	ContactID      uuid.UUID  `gorm:"type:uuid;index;not null" json:"contact_id"`
	AssigneeID     *uuid.UUID `gorm:"type:uuid;index" json:"assignee_id,omitempty"`
	CreatedByID    uuid.UUID  `gorm:"type:uuid;not null" json:"created_by_id"`
	Title          string     `gorm:"size:255;not null" json:"title"`
	Notes          string     `gorm:"type:text" json:"notes"`
	DueAt          *time.Time `json:"due_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ReminderSentAt *time.Time `json:"reminder_sent_at,omitempty"` // set once the due reminder fired

	// Relations
	Organization *Organization `gorm:"foreignKey:OrganizationID" json:"organization,omitempty"`
	Contact      *Contact      `gorm:"foreignKey:ContactID" json:"contact,omitempty"`
	Assignee     *User         `gorm:"foreignKey:AssigneeID" json:"assignee,omitempty"`
	CreatedBy    *User         `gorm:"foreignKey:CreatedByID" json:"created_by,omitempty"`
}

func (Task) TableName() string {
	return "tasks"
}
