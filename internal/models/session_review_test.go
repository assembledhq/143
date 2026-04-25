package models

import "testing"

func TestSessionReviewModeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    SessionReviewMode
		wantErr bool
	}{
		{name: "default is valid", mode: SessionReviewModeDefault},
		{name: "security is valid", mode: SessionReviewModeSecurity},
		{name: "unknown is invalid", mode: SessionReviewMode("unknown"), wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.mode.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Validate(%q) should return an error for unsupported modes", tt.mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate(%q) should accept supported review modes: %v", tt.mode, err)
			}
		})
	}
}
