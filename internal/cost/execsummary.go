package cost

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Page-scoped aggregate for the executive summary page: one HTTP round trip
// instead of the 6 base + up-to-7 per-series calls the UI used to make.
type ExecSummaryParams struct {
	Filters   Filters
	Dimension string
	Top       int
}

type NamedSeries struct {
	Name string           `json:"name"`
	Rows []map[string]any `json:"rows"`
}

type ExecSummary struct {
	Summary      []map[string]any `json:"summary"`
	Monthly      []map[string]any `json:"monthly"`
	CostCenters  []map[string]any `json:"cost_centers"`
	Environments []map[string]any `json:"environments"`
	TopBreakdown []map[string]any `json:"top_breakdown"`
	TopSeries    []NamedSeries    `json:"top_series"`
	Freshness    *Meta            `json:"freshness"`
}

func ValidDimension(dimension string) bool {
	_, ok := dimensions[dimension]
	return ok
}

// withDimensionFilter narrows filters to one dimension value, mirroring the
// UI's per-series filter; "" means untagged and maps to the API sentinel.
func withDimensionFilter(f Filters, dimension, value string) (Filters, error) {
	if value == "" {
		value = "__untagged__"
	}
	switch dimension {
	case "service":
		f.Service = value
	case "compartment":
		f.Compartment = value
	case "environment":
		f.Environment = value
	case "cost_center":
		f.CostCenter = value
	case "component_type":
		f.ComponentType = value
	case "resource_type":
		f.ResourceType = value
	case "resource_name":
		f.ResourceName = value
	default:
		return f, fmt.Errorf("unsupported dimension")
	}
	return f, nil
}

// topNames returns up to n distinct dimension values in cost-desc order
// (breakdown rows may repeat a value across currencies).
func topNames(rows []map[string]any, n int) []string {
	seen := make(map[string]bool, n)
	names := make([]string, 0, n)
	for _, row := range rows {
		name, ok := row["dimension_value"].(string)
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
		if len(names) == n {
			break
		}
	}
	return names
}

func orEmpty(rows []map[string]any) []map[string]any {
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}

func (s *Service) ExecSummary(ctx context.Context, p ExecSummaryParams) (ExecSummary, error) {
	monthlyQ, err := TimeseriesQuery(p.Filters, "month")
	if err != nil {
		return ExecSummary{}, err
	}
	topQ, err := BreakdownQuery(p.Filters, p.Dimension, 20)
	if err != nil {
		return ExecSummary{}, err
	}
	ccQ, err := BreakdownQuery(p.Filters, "cost_center", 50)
	if err != nil {
		return ExecSummary{}, err
	}
	envQ, err := BreakdownQuery(p.Filters, "environment", 50)
	if err != nil {
		return ExecSummary{}, err
	}

	var out ExecSummary
	jobs := []struct {
		dst *[]map[string]any
		q   Query
	}{
		{&out.Summary, SummaryQuery(p.Filters)},
		{&out.Monthly, monthlyQ},
		{&out.CostCenters, ccQ},
		{&out.Environments, envQ},
		{&out.TopBreakdown, topQ},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(jobs))
	for i, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.repo.Execute(ctx, job.q)
			if err != nil {
				errs[i] = err
				return
			}
			*job.dst = orEmpty(result.Rows)
		}()
	}
	// freshness is best-effort: page renders without it (freshness: null)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if meta, err := s.repo.Freshness(ctx); err == nil {
			out.Freshness = &meta
		}
	}()
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return ExecSummary{}, err
	}

	names := topNames(out.TopBreakdown, p.Top)
	out.TopSeries = make([]NamedSeries, len(names))
	serErrs := make([]error, len(names))
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := withDimensionFilter(p.Filters, p.Dimension, name)
			if err != nil {
				serErrs[i] = err
				return
			}
			q, err := TimeseriesQuery(f, "month")
			if err != nil {
				serErrs[i] = err
				return
			}
			result, err := s.repo.Execute(ctx, q)
			if err != nil {
				serErrs[i] = err
				return
			}
			out.TopSeries[i] = NamedSeries{Name: name, Rows: orEmpty(result.Rows)}
		}()
	}
	wg.Wait()
	if err := errors.Join(serErrs...); err != nil {
		return ExecSummary{}, err
	}
	return out, nil
}
