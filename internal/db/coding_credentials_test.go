package db

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// fakeClock returns a controllable time source used by the cache TTL tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestResolverCacheTTL(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{now: time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)}
	cache := newResolverCache(30 * time.Second)
	cache.clock = clk.Now

	orgID := uuid.New()
	userID := uuid.New()
	provider := models.ProviderAnthropicSubscription

	val := []models.DecryptedCodingCredential{{ID: uuid.New(), OrgID: orgID, Provider: provider}}
	cache.put(orgID, &userID, provider, val)

	got, ok := cache.get(orgID, &userID, provider)
	if !ok || len(got) != 1 {
		t.Fatalf("expected fresh hit, got ok=%v len=%d", ok, len(got))
	}

	// Within TTL — still hits.
	clk.Advance(15 * time.Second)
	if _, ok := cache.get(orgID, &userID, provider); !ok {
		t.Fatalf("expected hit at 15s, got miss")
	}

	// Past TTL — miss.
	clk.Advance(20 * time.Second) // total 35s
	if _, ok := cache.get(orgID, &userID, provider); ok {
		t.Fatalf("expected miss at 35s, got hit")
	}
}

func TestResolverCacheReturnsCopy(t *testing.T) {
	t.Parallel()

	cache := newResolverCache(30 * time.Second)
	orgID := uuid.New()
	provider := models.ProviderOpenAISubscription

	id1 := uuid.New()
	cache.put(orgID, nil, provider, []models.DecryptedCodingCredential{{ID: id1}})

	got, ok := cache.get(orgID, nil, provider)
	if !ok {
		t.Fatalf("expected hit")
	}
	// Mutating the returned slice must not poison the cache.
	got[0].ID = uuid.New()
	again, _ := cache.get(orgID, nil, provider)
	if again[0].ID != id1 {
		t.Fatalf("cache returned mutable reference; got %v want %v", again[0].ID, id1)
	}
}

func TestResolverCacheInvalidateScope(t *testing.T) {
	t.Parallel()

	cache := newResolverCache(30 * time.Second)
	orgID := uuid.New()
	userID := uuid.New()

	val := []models.DecryptedCodingCredential{{ID: uuid.New()}}
	cache.put(orgID, &userID, models.ProviderAnthropicSubscription, val)
	cache.put(orgID, nil, models.ProviderAnthropicSubscription, val)
	cache.put(orgID, &userID, models.ProviderOpenAISubscription, val)

	cache.invalidateOrg(orgID, models.ProviderAnthropicSubscription)

	if _, ok := cache.get(orgID, &userID, models.ProviderAnthropicSubscription); ok {
		t.Fatalf("expected personal anthropic_subscription invalidated")
	}
	if _, ok := cache.get(orgID, nil, models.ProviderAnthropicSubscription); ok {
		t.Fatalf("expected org anthropic_subscription invalidated")
	}
	if _, ok := cache.get(orgID, &userID, models.ProviderOpenAISubscription); !ok {
		t.Fatalf("expected openai_subscription untouched")
	}
}

func TestHealthCacheShedAndExpire(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{now: time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)}
	hc := newHealthCache(60 * time.Second)
	hc.clock = clk.Now

	id := uuid.New()
	if hc.isShed(id) {
		t.Fatalf("fresh id should not be shed")
	}

	hc.shed(id)
	if !hc.isShed(id) {
		t.Fatalf("expected shed after marker")
	}

	clk.Advance(30 * time.Second)
	if !hc.isShed(id) {
		t.Fatalf("expected shed at 30s")
	}

	clk.Advance(40 * time.Second) // total 70s
	if hc.isShed(id) {
		t.Fatalf("expected expiry past TTL")
	}
}

func TestHealthCacheFilter(t *testing.T) {
	t.Parallel()

	hc := newHealthCache(60 * time.Second)
	a := uuid.New()
	b := uuid.New()
	c := uuid.New()
	hc.shed(b)

	creds := []models.DecryptedCodingCredential{{ID: a}, {ID: b}, {ID: c}}
	got := hc.filter(creds)
	if len(got) != 2 {
		t.Fatalf("expected 2 eligible, got %d", len(got))
	}
	if got[0].ID == b || got[1].ID == b {
		t.Fatalf("shed credential leaked through filter")
	}
}

func TestGroupByPriorityAndScope(t *testing.T) {
	t.Parallel()

	uid := uuid.New()
	personal := func(p int) models.DecryptedCodingCredential {
		return models.DecryptedCodingCredential{ID: uuid.New(), UserID: &uid, Priority: p}
	}
	org := func(p int) models.DecryptedCodingCredential {
		return models.DecryptedCodingCredential{ID: uuid.New(), UserID: nil, Priority: p}
	}

	// Resolver order: personal (priority 1, 1, 2) then org (1, 1).
	creds := []models.DecryptedCodingCredential{
		personal(1), personal(1), personal(2),
		org(1), org(1),
	}
	tiers := groupByPriorityAndScope(creds)
	if len(tiers) != 3 {
		t.Fatalf("expected 3 tiers, got %d", len(tiers))
	}
	if len(tiers[0]) != 2 || tiers[0][0].UserID == nil {
		t.Fatalf("tier 0 should be 2 personal rows, got %+v", tiers[0])
	}
	if len(tiers[1]) != 1 || tiers[1][0].Priority != 2 || tiers[1][0].UserID == nil {
		t.Fatalf("tier 1 should be 1 personal priority-2 row, got %+v", tiers[1])
	}
	if len(tiers[2]) != 2 || tiers[2][0].UserID != nil {
		t.Fatalf("tier 2 should be 2 org priority-1 rows, got %+v", tiers[2])
	}
}

func TestGroupByPriorityAndScopeEmpty(t *testing.T) {
	t.Parallel()
	if got := groupByPriorityAndScope(nil); got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}

func TestSameUserPointer(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	other := uuid.New()
	cases := []struct {
		name string
		a, b *uuid.UUID
		want bool
	}{
		{"both nil", nil, nil, true},
		{"left nil", nil, &id, false},
		{"right nil", &id, nil, false},
		{"equal", &id, &id, true},
		{"distinct", &id, &other, false},
	}
	for _, tc := range cases {
		if got := sameUserPointer(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: sameUserPointer(%v,%v)=%v want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

func TestMoveCodingCredentialInputValidate(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	cases := []struct {
		name string
		in   models.MoveCodingCredentialInput
		ok   bool
	}{
		{"to_top", models.MoveCodingCredentialInput{ToTop: true}, true},
		{"to_bottom", models.MoveCodingCredentialInput{ToBottom: true}, true},
		{"before_id", models.MoveCodingCredentialInput{BeforeID: &id}, true},
		{"after_id", models.MoveCodingCredentialInput{AfterID: &id}, true},
		{"none", models.MoveCodingCredentialInput{}, false},
		{"two", models.MoveCodingCredentialInput{ToTop: true, ToBottom: true}, false},
		{"top_and_id", models.MoveCodingCredentialInput{ToTop: true, BeforeID: &id}, false},
	}
	for _, tc := range cases {
		err := tc.in.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: expected ok, got %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}
