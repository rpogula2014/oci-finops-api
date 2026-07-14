package cost

import (
	"context"
	"errors"
	"testing"
	"time"
)

type flakyFreshRepo struct{ fail bool }

func (flakyFreshRepo) Ping(context.Context) error { return nil }
func (flakyFreshRepo) Execute(context.Context, Query) (QueryResult, error) {
	return QueryResult{}, nil
}
func (r *flakyFreshRepo) Freshness(context.Context) (Meta, error) {
	if r.fail {
		return Meta{}, errors.New("boom")
	}
	now := time.Now()
	return Meta{LoadedAt: &now}, nil
}

func TestFreshnessServesStaleOnRefreshError(t *testing.T) {
	repo := &flakyFreshRepo{}
	service := NewService(repo)
	first, err := service.Freshness(context.Background())
	if err != nil || first.LoadedAt == nil {
		t.Fatalf("warm-up failed: %v", err)
	}
	// expire the cache, then break the repo: metadata staleness must not fail responses
	service.freshAt = time.Now().Add(-2 * freshnessTTL)
	repo.fail = true
	stale, err := service.Freshness(context.Background())
	if err != nil {
		t.Fatalf("expected stale value, got error: %v", err)
	}
	if stale.LoadedAt == nil || !stale.LoadedAt.Equal(*first.LoadedAt) {
		t.Fatal("expected the previously cached freshness")
	}
}

func TestFreshnessColdStartErrorPropagates(t *testing.T) {
	if _, err := NewService(&flakyFreshRepo{fail: true}).Freshness(context.Background()); err == nil {
		t.Fatal("no cached value exists; error must propagate")
	}
}
