package cost

import (
	"context"
	"sync"
	"time"
)

// freshnessTTL bounds how stale the cached freshness metadata can be. Cost data
// loads once a day, but the freshness query full-scans the view (~0.7 CPU-s), so
// recomputing it per request dominated server CPU under concurrent load.
const freshnessTTL = 60 * time.Second

type Service struct {
	repo Repository

	mu        sync.Mutex
	freshness Meta
	freshAt   time.Time
}

func NewService(repo Repository) *Service         { return &Service{repo: repo} }
func (s *Service) Ping(ctx context.Context) error { return s.repo.Ping(ctx) }
func (s *Service) Run(ctx context.Context, query Query) (QueryResult, Meta, error) {
	result, err := s.repo.Execute(ctx, query)
	if err != nil {
		return QueryResult{}, Meta{}, err
	}
	meta, err := s.Freshness(ctx)
	return result, meta, err
}

func (s *Service) Freshness(ctx context.Context) (Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.freshAt.IsZero() && time.Since(s.freshAt) < freshnessTTL {
		return s.freshness, nil
	}
	meta, err := s.repo.Freshness(ctx)
	if err != nil {
		// Freshness is metadata; a stale value beats failing a response whose
		// data query already succeeded.
		if !s.freshAt.IsZero() {
			return s.freshness, nil
		}
		return Meta{}, err
	}
	s.freshness, s.freshAt = meta, time.Now()
	return meta, nil
}
