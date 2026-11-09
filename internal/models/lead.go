package models

import (
	"fmt"
	"regexp"
	"strings"
)

// Lead fields are keys stored under Contact.Metadata["lead"]. Keeping them in
// one namespaced object leaves other metadata (chatbot flow data, API-written
// keys) untouched. Each organization can define its own field list in
// Organization.Settings["lead_fields"]; DefaultLeadFields is used until then.

// LeadMetadataKey is the Contact.Metadata key that holds lead fields.
const LeadMetadataKey = "lead"

// LeadFieldsSettingsKey is the Organization.Settings key holding the field list.
const LeadFieldsSettingsKey = "lead_fields"

// LeadFieldMaxLength is the maximum length of a lead field value.
const LeadFieldMaxLength = 255

// Limits for organization-defined lead fields.
const (
	MaxLeadFields       = 20
	MaxLeadFieldLabel   = 50
	MaxLeadFieldOptions = 30
)

// leadFieldKeyPattern restricts keys to identifiers that are safe in CSV column
// names and in the server-built JSONB export expressions.
var leadFieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,29}$`)

// LeadField describes one lead field. Options, when set, are suggestions shown
// in the UI; any value is still accepted.
type LeadField struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Options []string `json:"options,omitempty"`
}

// DefaultLeadFields lists the lead fields used when an organization has not
// defined its own.
func DefaultLeadFields() []LeadField {
	return []LeadField{
		{Key: "source", Label: "Source", Options: []string{"Website", "WhatsApp", "Facebook", "Instagram", "Google Ads", "Referral", "Walk-in"}},
		{Key: "budget", Label: "Budget"},
		{Key: "interest", Label: "Interest"},
		{Key: "city", Label: "City"},
		{Key: "status", Label: "Status", Options: []string{"Hot", "Warm", "Cold"}},
	}
}

// LeadFieldsFromSettings returns the organization's lead fields, falling back
// to DefaultLeadFields when unset or unreadable.
func LeadFieldsFromSettings(settings JSONB) []LeadField {
	raw, ok := settings[LeadFieldsSettingsKey].([]any)
	if !ok || len(raw) == 0 {
		return DefaultLeadFields()
	}
	fields := make([]LeadField, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := m["key"].(string)
		label, _ := m["label"].(string)
		if !leadFieldKeyPattern.MatchString(key) {
			continue
		}
		f := LeadField{Key: key, Label: label}
		if opts, ok := m["options"].([]any); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok {
					f.Options = append(f.Options, s)
				}
			}
		}
		fields = append(fields, f)
	}
	if len(fields) == 0 {
		return DefaultLeadFields()
	}
	return fields
}

// ValidateLeadFields trims and checks an organization's field list in place.
func ValidateLeadFields(fields []LeadField) error {
	if len(fields) == 0 {
		return fmt.Errorf("at least one lead field is required")
	}
	if len(fields) > MaxLeadFields {
		return fmt.Errorf("at most %d lead fields are allowed", MaxLeadFields)
	}
	seen := make(map[string]bool, len(fields))
	for i := range fields {
		f := &fields[i]
		f.Key = strings.TrimSpace(f.Key)
		f.Label = strings.TrimSpace(f.Label)
		if !leadFieldKeyPattern.MatchString(f.Key) {
			return fmt.Errorf("invalid key %q: use lowercase letters, digits and _, starting with a letter (max 30)", f.Key)
		}
		if seen[f.Key] {
			return fmt.Errorf("duplicate key %q", f.Key)
		}
		seen[f.Key] = true
		if f.Label == "" {
			f.Label = f.Key
		}
		if len(f.Label) > MaxLeadFieldLabel {
			return fmt.Errorf("label for %q must be at most %d characters", f.Key, MaxLeadFieldLabel)
		}
		if len(f.Options) > MaxLeadFieldOptions {
			return fmt.Errorf("%q can have at most %d options", f.Key, MaxLeadFieldOptions)
		}
		opts := make([]string, 0, len(f.Options))
		for _, o := range f.Options {
			o = strings.TrimSpace(o)
			if o == "" {
				continue
			}
			if len(o) > MaxLeadFieldLabel {
				return fmt.Errorf("option %q for %q is too long", o, f.Key)
			}
			opts = append(opts, o)
		}
		f.Options = opts
	}
	return nil
}

// HasLeadField reports whether key is one of fields.
func HasLeadField(fields []LeadField, key string) bool {
	for _, f := range fields {
		if f.Key == key {
			return true
		}
	}
	return false
}
