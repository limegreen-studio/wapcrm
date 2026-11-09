package models

import (
	"time"

	"github.com/google/uuid"
)

// Pipeline is an organization-scoped sales pipeline. Contacts are placed on a
// pipeline via Contact.PipelineID / Contact.StageID.
type Pipeline struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	Name           string    `gorm:"size:100;not null" json:"name"`
	IsDefault      bool      `gorm:"default:false" json:"is_default"`

	// Relations
	Organization *Organization   `gorm:"foreignKey:OrganizationID" json:"organization,omitempty"`
	Stages       []PipelineStage `gorm:"foreignKey:PipelineID" json:"stages,omitempty"`
}

func (Pipeline) TableName() string {
	return "pipelines"
}

// PipelineStage is one column of a pipeline's Kanban board.
type PipelineStage struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	PipelineID     uuid.UUID `gorm:"type:uuid;index;not null" json:"pipeline_id"`
	Name           string    `gorm:"size:100;not null" json:"name"`
	Color          string    `gorm:"size:20" json:"color"` // same palette as tags
	Position       int       `gorm:"not null;default:0" json:"position"`
	Kind           string    `gorm:"size:10;not null;default:'open'" json:"kind"` // open, won, lost
	Probability    int       `gorm:"not null;default:0" json:"probability"`       // percent, for the revenue forecast
}

func (PipelineStage) TableName() string {
	return "pipeline_stages"
}

// StageHistory is one recorded move of a contact between pipeline stages.
// A nil FromStageID means the contact entered the pipeline; a nil ToStageID
// means it left. Stage names are copied so reports survive stage deletion.
type StageHistory struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null" json:"organization_id"`
	ContactID      uuid.UUID  `gorm:"type:uuid;not null" json:"contact_id"`
	PipelineID     *uuid.UUID `gorm:"type:uuid" json:"pipeline_id,omitempty"`
	FromStageID    *uuid.UUID `gorm:"type:uuid" json:"from_stage_id,omitempty"`
	FromStageName  string     `gorm:"size:100" json:"from_stage_name"`
	ToStageID      *uuid.UUID `gorm:"type:uuid" json:"to_stage_id,omitempty"`
	ToStageName    string     `gorm:"size:100" json:"to_stage_name"`
	ToStageKind    string     `gorm:"size:10" json:"to_stage_kind"`
	ChangedByID    *uuid.UUID `gorm:"type:uuid" json:"changed_by_id,omitempty"`
	ChangedAt      time.Time  `gorm:"not null" json:"changed_at"`
	DealSeq        int        `gorm:"not null;default:0" json:"deal_seq"`
	DealValue      float64    `gorm:"type:numeric(14,2);not null;default:0" json:"deal_value"` // value when this move happened
	CloseReason    string     `gorm:"size:255" json:"close_reason"`
}

func (StageHistory) TableName() string {
	return "stage_history"
}

// Pipeline stage kinds
const (
	StageKindOpen = "open"
	StageKindWon  = "won"
	StageKindLost = "lost"
)

// IsValidStageKind checks if a stage kind is valid
func IsValidStageKind(kind string) bool {
	return kind == StageKindOpen || kind == StageKindWon || kind == StageKindLost
}

// DefaultPipelineStages is the stage set created for an organization's first pipeline.
func DefaultPipelineStages() []PipelineStage {
	return []PipelineStage{
		{Name: "New", Color: "blue", Kind: StageKindOpen, Probability: 10},
		{Name: "Contacted", Color: "purple", Kind: StageKindOpen, Probability: 20},
		{Name: "Qualified", Color: "yellow", Kind: StageKindOpen, Probability: 40},
		{Name: "Negotiation", Color: "gray", Kind: StageKindOpen, Probability: 70},
		{Name: "Won", Color: "green", Kind: StageKindWon, Probability: 100},
		{Name: "Lost", Color: "red", Kind: StageKindLost, Probability: 0},
	}
}
