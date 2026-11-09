package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/limegreen-studio/wapcrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The stage-change broadcast goes to every user in the org, so it must not
// carry contact details that scoped agents are not allowed to see.
func TestStageChangedPayload_IdsOnly(t *testing.T) {
	pid, sid := uuid.New(), uuid.New()
	now := time.Now()
	c := &models.Contact{
		BaseModel:          models.BaseModel{ID: uuid.New()},
		PhoneNumber:        "919999999999",
		ProfileName:        "Secret Name",
		LastMessagePreview: "private message",
		Tags:               models.JSONBArray{"vip"},
		Metadata:           models.JSONB{"lead": map[string]any{"budget": "1Cr"}},
		PipelineID:         &pid,
		StageID:            &sid,
		StageEnteredAt:     &now,
	}

	b, err := json.Marshal(stageChangedPayload(c))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	assert.ElementsMatch(t, []string{"contact_id", "pipeline_id", "stage_id", "stage_entered_at"}, keys(got))
	for _, secret := range []string{"919999999999", "Secret Name", "private message", "vip", "1Cr"} {
		assert.NotContains(t, string(b), secret)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
