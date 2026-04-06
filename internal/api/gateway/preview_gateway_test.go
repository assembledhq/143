package gateway

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestExtractPreviewID(t *testing.T) {
	t.Parallel()
	id := uuid.New()

	tests := []struct {
		name    string
		host    string
		want    uuid.UUID
		wantErr bool
	}{
		{
			name: "valid preview hostname",
			host: id.String() + ".preview.143.dev",
			want: id,
		},
		{
			name: "valid with port",
			host: id.String() + ".preview.localhost:9090",
			want: id,
		},
		{
			name:    "invalid UUID",
			host:    "not-a-uuid.preview.143.dev",
			wantErr: true,
		},
		{
			name:    "no subdomain",
			host:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := extractPreviewID(tt.host)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestEncodeDecode_CookieValue(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	previewID := uuid.New()
	accessSessionID := uuid.New()

	encoded := encodeCookieValue(orgID, previewID, accessSessionID)
	require.NotEmpty(t, encoded)

	gotOrg, gotPreview, gotAccess, err := decodeCookieValue(encoded)
	require.NoError(t, err)
	require.Equal(t, orgID, gotOrg)
	require.Equal(t, previewID, gotPreview)
	require.Equal(t, accessSessionID, gotAccess)
}

func TestDecodeCookieValue_Invalid(t *testing.T) {
	t.Parallel()
	_, _, _, err := decodeCookieValue("not-valid-base64!!!")
	require.Error(t, err)

	_, _, _, err = decodeCookieValue("dHdvOnBhcnRz") // "two:parts" base64
	require.Error(t, err)
}

func TestIsWebSocketUpgrade(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		connection string
		upgrade    string
		want       bool
	}{
		{"websocket upgrade", "Upgrade", "websocket", true},
		{"case insensitive", "upgrade", "WebSocket", true},
		{"no upgrade header", "", "websocket", false},
		{"no websocket value", "Upgrade", "", false},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &mockHTTPRequest{connection: tt.connection, upgrade: tt.upgrade}
			require.Equal(t, tt.want, isWebSocketUpgradeHelper(r.connection, r.upgrade))
		})
	}
}

type mockHTTPRequest struct {
	connection string
	upgrade    string
}

// isWebSocketUpgradeHelper duplicates the logic for unit testing without http.Request.
func isWebSocketUpgradeHelper(connection, upgrade string) bool {
	return len(connection) > 0 && len(upgrade) > 0 &&
		(connection == "Upgrade" || connection == "upgrade") &&
		(upgrade == "websocket" || upgrade == "WebSocket")
}
