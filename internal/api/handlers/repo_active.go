package handlers

import (
	"context"
	"errors"
	"reflect"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// Sentinels returned by requireActiveRepo. Callers map them to the HTTP shape
// that fits their endpoint — some 404 the missing case, others 400 it.
var (
	errRepoStoreUnconfigured = errors.New("repository lookup not configured")
	errRepoNotFound          = errors.New("repository not found")
	errRepoDisconnected      = errors.New("repository is disconnected")
)

// repoLookup is the slim interface requireActiveRepo needs from the repository
// store. Defined here (instead of taking *db.RepositoryStore) so that handlers
// that already inject a narrower interface — like AutomationHandler's
// automationRepoLookup — can share the same helper without an adapter.
type repoLookup interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

// requireActiveRepo fetches a repo scoped to orgID and verifies it is active.
// Centralises the three-step check (store wired? row exists? still active?)
// that every creation path (sessions, projects, automations, internal proposals)
// needs to run before accepting new work against a repository.
func requireActiveRepo(ctx context.Context, store repoLookup, orgID, repoID uuid.UUID) (models.Repository, error) {
	if isNilLookup(store) {
		return models.Repository{}, errRepoStoreUnconfigured
	}
	repo, err := store.GetByID(ctx, orgID, repoID)
	if err != nil {
		return models.Repository{}, errRepoNotFound
	}
	if !repo.IsActive() {
		return models.Repository{}, errRepoDisconnected
	}
	return repo, nil
}

// isNilLookup catches the typed-nil-interface trap: handlers store the lookup
// as a concrete pointer (e.g. *db.RepositoryStore), and a nil pointer wrapped
// in an interface compares != nil. Without this check, an unconfigured store
// would slip past the guard and panic on the GetByID call.
func isNilLookup(store repoLookup) bool {
	if store == nil {
		return true
	}
	v := reflect.ValueOf(store)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return v.IsNil()
	default:
		return false
	}
}
