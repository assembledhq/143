package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLiveEventValidate(t *testing.T) {
	t.Parallel()
	resourceID := uuid.New()
	version := int64(2)
	valid := LiveEvent{SchemaVersion: 1, EventID: uuid.New(), Type: LiveEventSessionUpdated, Scope: LiveEventScopeResource, OrgID: uuid.New(), ResourceType: LiveResourceSession, ResourceID: &resourceID, Audience: LiveAudienceOrg, Version: &version, ChangedAt: time.Now(), Payload: json.RawMessage(`{"list_affected":true}`)}
	tests := []struct {
		name    string
		mutate  func(*LiveEvent)
		wantErr bool
	}{
		{name: "valid resource projection"},
		{name: "unknown schema", mutate: func(e *LiveEvent) { e.SchemaVersion = 9 }, wantErr: true},
		{name: "resource missing id", mutate: func(e *LiveEvent) { e.ResourceID = nil }, wantErr: true},
		{name: "collection carrying id", mutate: func(e *LiveEvent) { e.Scope = LiveEventScopeCollection }, wantErr: true},
		{name: "repository audience missing repository", mutate: func(e *LiveEvent) { e.Audience = LiveAudienceRepository }, wantErr: true},
		{name: "nonpositive version", mutate: func(e *LiveEvent) { zero := int64(0); e.Version = &zero }, wantErr: true},
		{name: "oversized payload", mutate: func(e *LiveEvent) { e.Payload = make([]byte, LiveEventMaxPayloadSize+1) }, wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			event := valid
			if tt.mutate != nil {
				tt.mutate(&event)
			}
			err := event.Validate()
			if tt.wantErr {
				require.Error(t, err, "invalid envelope should be rejected")
			} else {
				require.NoError(t, err, "valid envelope should be accepted")
			}
		})
	}
}

func TestLiveEventEnumsValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		validate func() error
		wantErr  bool
	}{
		{name: "event", validate: func() error { return LiveEventSessionUpdated.Validate() }},
		{name: "invalid event", validate: func() error { return LiveEventType("future").Validate() }, wantErr: true},
		{name: "resource", validate: func() error { return LiveResourcePreview.Validate() }},
		{name: "invalid resource", validate: func() error { return LiveResourceType("future").Validate() }, wantErr: true},
		{name: "scope", validate: func() error { return LiveEventScopeResource.Validate() }},
		{name: "invalid scope", validate: func() error { return LiveEventScope("future").Validate() }, wantErr: true},
		{name: "audience", validate: func() error { return LiveAudienceOrg.Validate() }},
		{name: "invalid audience", validate: func() error { return LiveAudienceScope("future").Validate() }, wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.validate()
			if tt.wantErr {
				require.Error(t, err, "unknown enum should fail validation")
			} else {
				require.NoError(t, err, "named enum should pass validation")
			}
		})
	}
}
