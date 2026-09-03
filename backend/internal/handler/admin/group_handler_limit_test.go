package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateGroupRequestDoesNotOwnSubscriptionLimits(t *testing.T) {
	var req UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"openai"}`), &req))
	require.Equal(t, "openai", req.Name)
}
