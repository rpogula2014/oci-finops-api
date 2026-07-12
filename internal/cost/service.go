package cost

import "context"

type Service struct{ repo Repository }

func NewService(repo Repository) *Service         { return &Service{repo: repo} }
func (s *Service) Ping(ctx context.Context) error { return s.repo.Ping(ctx) }
func (s *Service) Run(ctx context.Context, query Query) (QueryResult, Meta, error) {
	result, err := s.repo.Execute(ctx, query)
	if err != nil {
		return QueryResult{}, Meta{}, err
	}
	meta, err := s.repo.Freshness(ctx)
	return result, meta, err
}
func (s *Service) Freshness(ctx context.Context) (Meta, error) { return s.repo.Freshness(ctx) }
