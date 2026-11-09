package models

import (
	"time"

	"github.com/google/uuid"
)

// Automation triggers
const (
	AutomationTriggerLeadCreated  = "lead_created"  // lead arrived through the API or a form
	AutomationTriggerFirstMessage = "first_message" // unknown number wrote on WhatsApp
	AutomationTriggerStageChanged = "stage_changed" // lead moved onto a stage
	AutomationTriggerTagAdded     = "tag_added"     // a tag was added to a lead
)

// Automation action types
const (
	AutomationActionAssignUser   = "assign_user"
	AutomationActionAssignTeam   = "assign_team"
	AutomationActionAddTag       = "add_tag"
	AutomationActionMoveStage    = "move_stage"
	AutomationActionCreateTask   = "create_task"
	AutomationActionSendTemplate = "send_template"
)

// AutomationRule runs actions when a trigger fires and its filters match.
// Empty filters match everything.
type AutomationRule struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	Name           string     `gorm:"size:100;not null" json:"name"`
	Enabled        bool       `gorm:"default:false" json:"enabled"`
	Trigger        string     `gorm:"size:30;not null" json:"trigger"`
	PipelineID     *uuid.UUID `gorm:"type:uuid" json:"pipeline_id,omitempty"`
	StageID        *uuid.UUID `gorm:"type:uuid" json:"stage_id,omitempty"`
	Tag            string     `gorm:"size:50" json:"tag"`
	Source         string     `gorm:"size:100" json:"source"`
	Actions        JSONBArray `gorm:"type:jsonb;not null" json:"actions"` // []AutomationAction
	CreatedByID    uuid.UUID  `gorm:"type:uuid;not null" json:"created_by_id"`
}

func (AutomationRule) TableName() string {
	return "automation_rules"
}

// AutomationAction is one step of a rule. DelayHours > 0 makes it a drip step:
// it runs later, and only if the lead has not replied or left the stage.
type AutomationAction struct {
	Type           string            `json:"type"`
	DelayHours     int               `json:"delay_hours,omitempty"`
	UserID         *uuid.UUID        `json:"user_id,omitempty"`
	TeamID         *uuid.UUID        `json:"team_id,omitempty"`
	Tag            string            `json:"tag,omitempty"`
	StageID        *uuid.UUID        `json:"stage_id,omitempty"`
	TaskTitle      string            `json:"task_title,omitempty"`
	TaskDueHours   int               `json:"task_due_hours,omitempty"`
	TemplateID     *uuid.UUID        `json:"template_id,omitempty"`
	TemplateParams map[string]string `json:"template_params,omitempty"`
}

// AutomationJob is a delayed action waiting for its run time.
type AutomationJob struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null" json:"organization_id"`
	RuleID         uuid.UUID  `gorm:"type:uuid;index;not null" json:"rule_id"`
	ContactID      uuid.UUID  `gorm:"type:uuid;index;not null" json:"contact_id"`
	Action         JSONB      `gorm:"type:jsonb;not null" json:"action"`
	GuardStageID   *uuid.UUID `gorm:"type:uuid" json:"guard_stage_id,omitempty"` // skipped if the lead left this stage
	RunAt          time.Time  `gorm:"not null" json:"run_at"`
	ProcessedAt    *time.Time `json:"processed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

func (AutomationJob) TableName() string {
	return "automation_jobs"
}

// PushSubscription is one browser or phone that receives web push alerts.
type PushSubscription struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	UserID         uuid.UUID `gorm:"type:uuid;index;not null" json:"user_id"`
	Endpoint       string    `gorm:"type:text;uniqueIndex;not null" json:"endpoint"`
	P256DH         string    `gorm:"column:p256dh;size:255;not null" json:"p256dh"`
	Auth           string    `gorm:"size:255;not null" json:"auth"`
}

func (PushSubscription) TableName() string {
	return "push_subscriptions"
}

// Quote statuses
const (
	QuoteStatusDraft    = "draft"
	QuoteStatusSent     = "sent"
	QuoteStatusAccepted = "accepted"
	QuoteStatusPaid     = "paid"
)

// Quote is a price offer sent to a lead through a private link.
type Quote struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	ContactID      uuid.UUID  `gorm:"type:uuid;index;not null" json:"contact_id"`
	Title          string     `gorm:"size:255;not null" json:"title"`
	Items          JSONBArray `gorm:"type:jsonb;not null" json:"items"` // []QuoteItem
	TaxPercent     float64    `gorm:"type:numeric(5,2);default:0" json:"tax_percent"`
	Total          float64    `gorm:"type:numeric(14,2);default:0" json:"total"`
	Notes          string     `gorm:"type:text" json:"notes"`
	PaymentURL     string     `gorm:"type:text" json:"payment_url"`
	Status         string     `gorm:"size:20;not null;default:'draft'" json:"status"`
	Token          string     `gorm:"size:48;uniqueIndex;not null" json:"token"`
	CreatedByID    uuid.UUID  `gorm:"type:uuid;not null" json:"created_by_id"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
}

func (Quote) TableName() string {
	return "quotes"
}

// QuoteItem is one line of a quote.
type QuoteItem struct {
	Name     string  `json:"name"`
	Quantity float64 `json:"quantity"`
	Price    float64 `json:"price"`
}

// Organization.Settings keys for sales settings.
const (
	SalesSettingsKey = "sales_settings"
	LeadSLASubKey    = "lead_sla"
)

// LeadSLA reassigns new leads an owner has not answered in time.
type LeadSLA struct {
	Enabled   bool       `json:"enabled"`
	Minutes   int        `json:"minutes"`
	TeamID    *uuid.UUID `json:"team_id,omitempty"`
	EnabledAt *time.Time `json:"enabled_at,omitempty"` // only leads created after this are covered
}

// SalesSettings holds organization-wide sales options.
type SalesSettings struct {
	DefaultCountryCode string             `json:"default_country_code"`
	CurrencySymbol     string             `json:"currency_symbol"`
	LeadSLA            LeadSLA            `json:"lead_sla"`
	MonthlyTargets     map[string]float64 `json:"monthly_targets"` // user id -> target
}
