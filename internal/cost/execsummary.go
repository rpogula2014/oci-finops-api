package cost

import (
	"context"
	"errors"
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

// splitTopSeries reshapes the combined breakdown-series rows (from
// BreakdownSeriesQuery) into the two output sections in one pass:
//   - breakdown: one row per (dimension_value, currency), cost-desc order
//   - series: one NamedSeries per distinct name (currencies collapse into
//     one series), capped at top, with rows shaped like TimeseriesQuery
//     output ({bucket, currency, cost}) to preserve the wire contract.
func splitTopSeries(rows []map[string]any, top int) ([]map[string]any, []NamedSeries) {
	seenBreakdown := make(map[string]bool, len(rows))
	breakdown := make([]map[string]any, 0, len(rows))
	seriesIdx := make(map[string]int, len(rows))
	var series []NamedSeries
	for _, src := range rows {
		value, _ := src["dimension_value"].(string)
		currency, _ := src["currency"].(string)
		if key := value + "\x00" + currency; !seenBreakdown[key] {
			seenBreakdown[key] = true
			breakdown = append(breakdown, map[string]any{
				"dimension_value": src["dimension_value"],
				"currency":        src["currency"],
				"cost":            src["cost"],
				"resources":       src["resources"],
			})
		}
		idx, ok := seriesIdx[value]
		if !ok {
			idx = len(series)
			seriesIdx[value] = idx
			series = append(series, NamedSeries{Name: value, Rows: []map[string]any{}})
		}
		// LEFT JOIN emits a null date for names with no matching series bucket.
		if date, ok := src["date"].(string); ok && date != "" {
			series[idx].Rows = append(series[idx].Rows, map[string]any{
				"bucket":   date,
				"currency": src["currency"],
				"cost":     src["series_cost"],
			})
		}
	}
	if top >= 0 && len(series) > top {
		series = series[:top]
	}
	return orEmpty(breakdown), series
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
	// One query returns the top-20 breakdown and each name's monthly series,
	// replacing the old N+1 fan-out (1 breakdown query + one TimeseriesQuery
	// per top name) and its second serial round trip to ClickHouse.
	topSeriesQ, err := BreakdownSeriesQuery(p.Filters, p.Dimension, 20, "month")
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
	var topSeriesRows []map[string]any
	jobs := []struct {
		dst *[]map[string]any
		q   Query
	}{
		{&out.Summary, SummaryQuery(p.Filters)},
		{&out.Monthly, monthlyQ},
		{&out.CostCenters, ccQ},
		{&out.Environments, envQ},
		{&topSeriesRows, topSeriesQ},
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

	out.TopBreakdown, out.TopSeries = splitTopSeries(topSeriesRows, p.Top)
	return out, nil
}
