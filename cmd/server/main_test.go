package main

import (
	"testing"
)

func TestBuildBaseMetadata(t *testing.T) {
	tests := []struct {
		name                   string
		previewCapable         bool
		previewInternalBaseURL string
		wantPreviewCapable     bool
		wantInternalBaseURL    string
	}{
		{
			name:                   "preview-capable worker advertises both fields",
			previewCapable:         true,
			previewInternalBaseURL: "http://worker-1:8080",
			wantPreviewCapable:     true,
			wantInternalBaseURL:    "http://worker-1:8080",
		},
		{
			name:                   "non-preview-capable node omits preview_capable",
			previewCapable:         false,
			previewInternalBaseURL: "",
			wantPreviewCapable:     false,
			wantInternalBaseURL:    "",
		},
		{
			name:                   "internal base URL without capability still emits URL",
			previewCapable:         false,
			previewInternalBaseURL: "http://worker-1:8080",
			wantPreviewCapable:     false,
			wantInternalBaseURL:    "http://worker-1:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := buildBaseMetadata(tt.previewCapable, tt.previewInternalBaseURL)

			if _, ok := metadata["build_sha"]; !ok {
				t.Errorf("expected build_sha to always be present")
			}

			gotCapable, hasCapable := metadata["preview_capable"]
			if tt.wantPreviewCapable {
				if !hasCapable || gotCapable != true {
					t.Errorf("expected preview_capable=true, got %v (present=%v)", gotCapable, hasCapable)
				}
			} else if hasCapable {
				t.Errorf("expected preview_capable to be omitted, got %v", gotCapable)
			}

			gotURL, hasURL := metadata["preview_internal_base_url"]
			if tt.wantInternalBaseURL != "" {
				if !hasURL || gotURL != tt.wantInternalBaseURL {
					t.Errorf("expected preview_internal_base_url=%q, got %v (present=%v)", tt.wantInternalBaseURL, gotURL, hasURL)
				}
			} else if hasURL {
				t.Errorf("expected preview_internal_base_url to be omitted, got %v", gotURL)
			}
		})
	}
}

// TestBuildWorkerMetadataProvider_PreservesPreviewFields guards against the
// regression where SetMetadataProvider in startProcessWorkers replaced the
// initial provider without re-emitting preview-routing fields, causing the
// next heartbeat to wipe preview_capable and break Start Preview routing.
func TestBuildWorkerMetadataProvider_PreservesPreviewFields(t *testing.T) {
	provider := buildWorkerMetadataProvider(nil, true, "http://worker-1:8080")

	metadata := provider()

	if got, ok := metadata["preview_capable"]; !ok || got != true {
		t.Errorf("preview_capable must persist across worker startup, got %v (present=%v)", got, ok)
	}
	if got, ok := metadata["preview_internal_base_url"]; !ok || got != "http://worker-1:8080" {
		t.Errorf("preview_internal_base_url must persist across worker startup, got %v (present=%v)", got, ok)
	}
	if _, ok := metadata["active_job_count"]; !ok {
		t.Errorf("expected active_job_count to be present in worker metadata")
	}
	if _, ok := metadata["active_run_agent_count"]; !ok {
		t.Errorf("expected active_run_agent_count to be present in worker metadata")
	}
}

func TestBuildWorkerMetadataProvider_NonPreviewCapable(t *testing.T) {
	provider := buildWorkerMetadataProvider(nil, false, "")

	metadata := provider()

	if _, ok := metadata["preview_capable"]; ok {
		t.Errorf("preview_capable should be omitted when worker is not preview-capable")
	}
	if _, ok := metadata["preview_internal_base_url"]; ok {
		t.Errorf("preview_internal_base_url should be omitted when not configured")
	}
}
