package codexauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestInitiateDeviceAuth_IntervalParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		intervalValue     any
		expectedPollIntvl int
		expectErr         bool
	}{
		{
			name:              "accepts numeric interval string",
			intervalValue:     "5",
			expectedPollIntvl: 5,
		},
		{
			name:              "accepts numeric interval",
			intervalValue:     7,
			expectedPollIntvl: 7,
		},
		{
			name:          "rejects non-numeric interval string",
			intervalValue: "five",
			expectErr:     true,
		},
	}

	for i := range tests {
		tc := tests[i]
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"device_auth_id":   "dev_123",
					"user_code":        "ABCD-1234",
					"verification_uri": "https://auth.openai.com/codex/device",
					"expires_in":       900,
					"interval":         tc.intervalValue,
				}), "test server should return valid JSON")
			}))
			defer server.Close()

			svc := NewService(newMockCredentialStore(), zerolog.Nop())
			svc.SetHTTPClient(server.Client())
			svc.SetIssuer(server.URL)

			orgID := uuid.New()
			_, err := svc.InitiateDeviceAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "")
			if tc.expectErr {
				require.Error(t, err, "initiate should fail for invalid interval values")
				return
			}

			require.NoError(t, err, "initiate should succeed for supported interval formats")
			val, ok := svc.pending.Load(pendingKey(models.Scope{OrgID: orgID}, ""))
			require.True(t, ok, "pending auth should be stored after successful initiation")

			pending, ok := val.(*PendingAuth)
			require.True(t, ok, "pending auth should have expected type")
			require.Equal(t, tc.expectedPollIntvl, pending.Interval, "pending auth should store parsed poll interval")
		})
	}
}
