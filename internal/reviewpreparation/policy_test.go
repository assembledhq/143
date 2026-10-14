package reviewpreparation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type repositoryStub struct {
	repository          models.Repository
	err                 error
	calls               int
	orgID, repositoryID uuid.UUID
}

func (s *repositoryStub) GetByID(_ context.Context, orgID, repositoryID uuid.UUID) (models.Repository, error) {
	s.calls++
	s.orgID, s.repositoryID = orgID, repositoryID
	return s.repository, s.err
}

func TestPolicyActivation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		enabled         bool
		names           []string
		nilPolicy       bool
		mutate          func(*models.Session, *repositoryStub)
		active, wantErr bool
		calls           int
	}{
		{name: "nil default", nilPolicy: true},
		{name: "disabled populated list", names: []string{"sample/selected"}},
		{name: "enabled empty list", enabled: true},
		{name: "enabled selected", enabled: true, names: []string{" SAMPLE/Selected "}, active: true, calls: 1},
		{name: "outside list keeps legacy", enabled: true, names: []string{"other/repository"}, calls: 1},
		{name: "outside list accepts missing legacy revision", enabled: true, names: []string{"other/repository"}, calls: 1, mutate: func(s *models.Session, _ *repositoryStub) { s.RevisionContext = nil }},
		{name: "outside list accepts old revision", enabled: true, names: []string{"other/repository"}, calls: 1, mutate: func(s *models.Session, _ *repositoryStub) { s.RevisionContext = json.RawMessage(`{"head_sha":"head"}`) }},
		{name: "nonreview bypass", enabled: true, names: []string{"sample/selected"}, mutate: func(s *models.Session, _ *repositoryStub) { s.Origin = models.SessionOriginManual }},
		{name: "lookup failure fails closed", enabled: true, names: []string{"sample/selected"}, calls: 1, wantErr: true, mutate: func(_ *models.Session, s *repositoryStub) { s.err = errors.New("lookup unavailable") }},
		{name: "missing session identity fails closed", enabled: true, names: []string{"sample/selected"}, wantErr: true, mutate: func(s *models.Session, _ *repositoryStub) { s.RepositoryID = nil }},
		{name: "missing canonical identity fails closed", enabled: true, names: []string{"sample/selected"}, calls: 1, wantErr: true, mutate: func(_ *models.Session, s *repositoryStub) { s.repository.FullName = "" }},
		{name: "cross tenant fails closed", enabled: true, names: []string{"sample/selected"}, calls: 1, wantErr: true, mutate: func(_ *models.Session, s *repositoryStub) { s.repository.OrgID = uuid.New() }},
		{name: "wrong repository ID fails closed", enabled: true, names: []string{"sample/selected"}, calls: 1, wantErr: true, mutate: func(_ *models.Session, s *repositoryStub) { s.repository.ID = uuid.New() }},
		{name: "selected missing revision fails closed", enabled: true, names: []string{"sample/selected"}, calls: 1, wantErr: true, mutate: func(s *models.Session, _ *repositoryStub) { s.RevisionContext = json.RawMessage(`{}`) }},
		{name: "selected invalid revision fails closed", enabled: true, names: []string{"sample/selected"}, calls: 1, wantErr: true, mutate: func(s *models.Session, _ *repositoryStub) { s.RevisionContext = json.RawMessage(`invalid`) }},
		{name: "revision conflict fails closed", enabled: true, names: []string{"sample/selected"}, calls: 1, wantErr: true, mutate: func(s *models.Session, _ *repositoryStub) {
			s.RevisionContext = json.RawMessage(`{"github_repo":"other/repository"}`)
		}},
		{name: "outside revision conflict fails closed", enabled: true, names: []string{"other/repository"}, calls: 1, wantErr: true, mutate: func(s *models.Session, _ *repositoryStub) {
			s.RevisionContext = json.RawMessage(`{"github_repo":"other/repository"}`)
		}},
		{name: "clone conflict fails closed", enabled: true, names: []string{"sample/selected"}, calls: 1, wantErr: true, mutate: func(_ *models.Session, s *repositoryStub) {
			s.repository.CloneURL = "https://github.com/other/repository.git"
		}},
		{name: "SSH identity matches", enabled: true, names: []string{"sample/selected"}, calls: 1, active: true, mutate: func(_ *models.Session, s *repositoryStub) {
			s.repository.CloneURL = "git@github.com:sample/selected.git"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID := uuid.New(), uuid.New()
			session := models.Session{OrgID: orgID, RepositoryID: &repoID, Origin: models.SessionOriginCodeReview, RevisionContext: json.RawMessage(`{"github_repo":"sample/selected"}`)}
			store := &repositoryStub{repository: models.Repository{ID: repoID, OrgID: orgID, FullName: "sample/selected", CloneURL: "https://github.com/sample/selected.git"}}
			if tt.mutate != nil {
				tt.mutate(&session, store)
			}
			var policy *Policy
			if !tt.nilPolicy {
				var err error
				policy, err = NewPolicy(tt.enabled, tt.names)
				require.NoError(t, err, "valid policy configuration must construct")
			}
			active, err := policy.ForSession(context.Background(), store, &session)
			if tt.wantErr {
				require.Error(t, err, "identity uncertainty must fail closed")
			} else {
				require.NoError(t, err, "known activation state must resolve")
			}
			require.Equal(t, tt.active, active, "only a configured verified review repository must activate")
			require.Equal(t, tt.calls, store.calls, "disabled policy must perform no repository lookup")
			if tt.calls > 0 {
				require.Equal(t, orgID, store.orgID, "canonical lookup must be scoped to the session tenant")
				require.Equal(t, repoID, store.repositoryID, "canonical lookup must use the session repository ID")
			}
		})
	}
}

func TestPolicyConfigurationValidationAndCopy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		names   []string
		wantErr bool
	}{
		{name: "empty"}, {name: "valid", names: []string{"sample/selected"}},
		{name: "missing owner", names: []string{"selected"}, wantErr: true},
		{name: "URL not full name", names: []string{"https://github.com/sample/selected"}, wantErr: true},
		{name: "empty entry", names: []string{""}, wantErr: true},
		{name: "traversal", names: []string{"../selected"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewPolicy(true, tt.names)
			if tt.wantErr {
				require.Error(t, err, "invalid deployment configuration must fail startup")
			} else {
				require.NoError(t, err, "valid deployment configuration must construct")
			}
		})
	}
	names := []string{"sample/selected"}
	policy, err := NewPolicy(true, names)
	require.NoError(t, err, "valid copied configuration must construct")
	names[0] = "other/repository"
	_, present := policy.repositories["sample/selected"]
	require.True(t, present, "caller mutations must not change the shared policy")
}
