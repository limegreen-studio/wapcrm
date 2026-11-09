package models

import (
	"encoding/json"

	"github.com/google/uuid"
)

// NoReplyRuleSettingsKey is the Organization.Settings key for NoReplyRule.
const NoReplyRuleSettingsKey = "no_reply_rule"

// NoReplyPipelineRulesKey holds per-pipeline rules (pipeline id -> NoReplyRule).
// A pipeline with an entry uses it instead of the organization rule.
const NoReplyPipelineRulesKey = "no_reply_pipeline_rules"

// NoReplyMaxSteps caps the follow-up steps after a rule's first action.
const NoReplyMaxSteps = 5

// Limits for NoReplyRule.Hours.
const (
	NoReplyMinHours = 1
	NoReplyMaxHours = 720 // 30 days
)

// NoReplyRule acts on leads that have not answered our last message.
//
// A lead qualifies when it is on an open pipeline stage, the last message in
// the conversation was ours, and it is older than Hours. The rule then runs
// once per silence: it fires again only after the customer has replied.
type NoReplyRule struct {
	Enabled bool `json:"enabled"`
	Hours   int  `json:"hours"`

	// Create a follow-up task for the lead owner, due immediately (so the
	// owner gets the task reminder).
	CreateTask bool   `json:"create_task"`
	TaskTitle  string `json:"task_title"`

	// Send an approved template. Values in TemplateParams may contain
	// {{name}} and {{phone}}, replaced with the contact's name and phone.
	TemplateID     *uuid.UUID        `json:"template_id,omitempty"`
	TemplateParams map[string]string `json:"template_params,omitempty"`

	// The admin who last saved the rule; notes and tasks the rule creates are
	// recorded under this user when the lead has no owner.
	UpdatedByID uuid.UUID `json:"updated_by_id"`

	// Steps run in order after the first action while the lead stays silent.
	// Each step's Hours counts from the previous step.
	Steps []NoReplyStep `json:"steps,omitempty"`
}

// NoReplyStep is one later action of a multi-step no-reply sequence.
type NoReplyStep struct {
	Hours          int               `json:"hours"`
	CreateTask     bool              `json:"create_task"`
	TaskTitle      string            `json:"task_title"`
	TemplateID     *uuid.UUID        `json:"template_id,omitempty"`
	TemplateParams map[string]string `json:"template_params,omitempty"`
}

// AllSteps returns the rule's first action followed by its later steps.
func (r NoReplyRule) AllSteps() []NoReplyStep {
	first := NoReplyStep{
		Hours:          r.Hours,
		CreateTask:     r.CreateTask,
		TaskTitle:      r.TaskTitle,
		TemplateID:     r.TemplateID,
		TemplateParams: r.TemplateParams,
	}
	return append([]NoReplyStep{first}, r.Steps...)
}

// NoReplyRuleFromSettings reads the rule from organization settings.
// A missing or unreadable rule is returned as disabled.
func NoReplyRuleFromSettings(settings JSONB) NoReplyRule {
	var rule NoReplyRule
	raw, ok := settings[NoReplyRuleSettingsKey]
	if !ok {
		return rule
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return NoReplyRule{}
	}
	if err := json.Unmarshal(b, &rule); err != nil {
		return NoReplyRule{}
	}
	return rule
}

// NoReplyPipelineRulesFromSettings reads per-pipeline rules from settings.
func NoReplyPipelineRulesFromSettings(settings JSONB) map[string]NoReplyRule {
	rules := map[string]NoReplyRule{}
	raw, ok := settings[NoReplyPipelineRulesKey]
	if !ok {
		return rules
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return map[string]NoReplyRule{}
	}
	if err := json.Unmarshal(b, &rules); err != nil {
		return map[string]NoReplyRule{}
	}
	return rules
}
