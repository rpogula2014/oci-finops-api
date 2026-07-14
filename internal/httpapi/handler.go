package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/example/costscope-api/internal/cost"
)

type Handler struct {
	service      *cost.Service
	queryTimeout time.Duration
	now          func() time.Time
}

func NewHandler(service *cost.Service, queryTimeout time.Duration) *Handler {
	return &Handler{service: service, queryTimeout: queryTimeout, now: time.Now}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /v1/costs/summary", h.summary)
	mux.HandleFunc("GET /v1/costs/exec-summary", h.execSummary)
	mux.HandleFunc("GET /v1/costs/timeseries", h.timeseries)
	mux.HandleFunc("GET /v1/costs/breakdown", h.breakdown)
	mux.HandleFunc("GET /v1/costs/resources/grouped", h.groupedResources)
	mux.HandleFunc("GET /v1/costs/resources", h.resources)
	mux.HandleFunc("GET /v1/costs/resources/{ocid}", h.resource)
	mux.HandleFunc("GET /v1/costs/lineitems", h.lineItems)
	mux.HandleFunc("GET /v1/costs/anomalies", h.anomalies)
	mux.HandleFunc("GET /v1/costs/trends", h.trends)
	mux.HandleFunc("GET /v1/costs/filters", h.filters)
	mux.HandleFunc("GET /v1/costs/freshness", h.freshness)
	mux.HandleFunc("GET /openapi.yaml", openapiSpec)
	mux.HandleFunc("GET /docs", swaggerUI)
	return requestID(requestLogger(recoverer(mux)))
}

func (h *Handler) withTimeout(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), h.queryTimeout)
}

func (h *Handler) parseFilters(r *http.Request) (cost.Filters, error) {
	q := r.URL.Query()
	now := h.now().UTC()
	start := now.AddDate(0, -1, 0)
	end := now
	var err error
	if value := q.Get("start"); value != "" {
		start, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return cost.Filters{}, fmt.Errorf("start must be RFC3339")
		}
	}
	if value := q.Get("end"); value != "" {
		end, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return cost.Filters{}, fmt.Errorf("end must be RFC3339")
		}
	}
	if !start.Before(end) {
		return cost.Filters{}, fmt.Errorf("start must be before end")
	}
	if end.Sub(start) > 400*24*time.Hour {
		return cost.Filters{}, fmt.Errorf("date range cannot exceed 400 days")
	}
	return cost.Filters{Start: start, End: end, Environment: q.Get("env"), CostCenter: q.Get("cost_center"), ComponentType: q.Get("component_type"), Compartment: q.Get("compartment"), Service: q.Get("service"), ResourceType: q.Get("resource_type"), ResourceName: q.Get("resource_name"), OCID: q.Get("ocid")}, nil
}

func boundedInt(raw string, fallback, min, max int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("must be between %d and %d", min, max)
	}
	return value, nil
}
func (h *Handler) run(w http.ResponseWriter, r *http.Request, query cost.Query, extra map[string]any) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	result, fresh, err := h.service.Run(ctx, query)
	if err != nil {
		slog.Error("clickhouse query failed", "path", r.URL.Path, "error", err)
		writeError(w, http.StatusServiceUnavailable, "UPSTREAM_ERROR", "cost data is unavailable")
		return
	}
	extra["freshness"] = fresh
	if result.Total > 0 {
		extra["total"] = result.Total
	}
	writeJSON(w, http.StatusOK, envelope{Data: result.Rows, Meta: extra})
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	if err := h.service.Ping(ctx); err != nil {
		slog.Error("clickhouse ping failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "UNHEALTHY", "ClickHouse is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, envelope{Data: map[string]string{"status": "ok"}, Meta: map[string]any{}})
}
func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	h.run(w, r, cost.SummaryQuery(f), map[string]any{})
}

// execSummary aggregates everything the executive summary page needs into one
// response; the per-section queries run concurrently in cost.Service.
func (h *Handler) execSummary(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	q := r.URL.Query()
	dimension := q.Get("dimension")
	if dimension == "" {
		dimension = "cost_center"
	}
	if !cost.ValidDimension(dimension) {
		bad(w, fmt.Errorf("unsupported dimension"))
		return
	}
	top, err := boundedInt(q.Get("top"), 7, 1, 20)
	if err != nil {
		bad(w, fmt.Errorf("top %w", err))
		return
	}
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	data, err := h.service.ExecSummary(ctx, cost.ExecSummaryParams{Filters: f, Dimension: dimension, Top: top})
	if err != nil {
		slog.Error("clickhouse exec-summary failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "UPSTREAM_ERROR", "cost data is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, envelope{Data: data, Meta: map[string]any{"dimension": dimension, "top": top}})
}

func (h *Handler) timeseries(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "day"
	}
	query, err := cost.TimeseriesQuery(f, granularity)
	if err != nil {
		bad(w, err)
		return
	}
	h.run(w, r, query, map[string]any{"granularity": granularity})
}
func (h *Handler) breakdown(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	dimension := r.URL.Query().Get("dimension")
	if dimension == "" {
		dimension = "service"
	}
	limit, err := boundedInt(r.URL.Query().Get("limit"), 20, 1, 100)
	if err != nil {
		bad(w, fmt.Errorf("limit %w", err))
		return
	}
	series, err := parseOptionalBool(r.URL.Query().Get("series"))
	if err != nil {
		bad(w, fmt.Errorf("series %w", err))
		return
	}
	if !series {
		query, err := cost.BreakdownQuery(f, dimension, limit)
		if err != nil {
			bad(w, err)
			return
		}
		h.run(w, r, query, map[string]any{"dimension": dimension})
		return
	}
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "day"
	}
	query, err := cost.BreakdownSeriesQuery(f, dimension, limit, granularity)
	if err != nil {
		bad(w, err)
		return
	}
	h.runBreakdownSeries(w, r, query, dimension, granularity)
}

func parseOptionalBool(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("must be true or false")
	}
	return value, nil
}

// runBreakdownSeries folds the joined result set into the public row shape.
// The costs are strings in this response so decimal precision is not lost in
// JavaScript before the UI chooses how to display them.
func (h *Handler) runBreakdownSeries(w http.ResponseWriter, r *http.Request, query cost.Query, dimension, granularity string) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	result, fresh, err := h.service.Run(ctx, query)
	if err != nil {
		slog.Error("clickhouse breakdown series query failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "UPSTREAM_ERROR", "cost data is unavailable")
		return
	}
	type groupedRow struct {
		row    map[string]any
		series []map[string]any
	}
	grouped := make(map[string]*groupedRow, len(result.Rows))
	ordered := make([]*groupedRow, 0, len(result.Rows))
	for _, source := range result.Rows {
		value, _ := source["dimension_value"].(string)
		currency, _ := source["currency"].(string)
		key := value + "\x00" + currency
		entry := grouped[key]
		if entry == nil {
			entry = &groupedRow{row: map[string]any{
				"dimension_value": source["dimension_value"],
				"currency":        source["currency"],
				"cost":            source["cost"],
				"resources":       source["resources"],
			}}
			grouped[key] = entry
			ordered = append(ordered, entry)
		}
		if date, ok := source["date"].(string); ok && date != "" {
			entry.series = append(entry.series, map[string]any{"date": date, "cost": source["series_cost"]})
		}
	}
	data := make([]map[string]any, 0, len(ordered))
	for _, entry := range ordered {
		entry.row["series"] = entry.series
		data = append(data, entry.row)
	}
	writeJSON(w, http.StatusOK, envelope{Data: data, Meta: map[string]any{"dimension": dimension, "granularity": granularity, "freshness": fresh}})
}
func (h *Handler) resources(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	q := r.URL.Query()
	limit, err := boundedInt(q.Get("limit"), 50, 1, 500)
	if err != nil {
		bad(w, fmt.Errorf("limit %w", err))
		return
	}
	page, err := boundedInt(q.Get("page"), 1, 1, 100000)
	if err != nil {
		bad(w, fmt.Errorf("page %w", err))
		return
	}
	sort := q.Get("sort")
	if sort == "" {
		sort = "cost"
	}
	direction := strings.ToLower(q.Get("direction"))
	if direction == "" {
		direction = "desc"
	}
	query, err := cost.ResourcesQuery(f, cost.Page{Limit: limit, Offset: (page - 1) * limit, Sort: sort, Direction: direction})
	if err != nil {
		bad(w, err)
		return
	}
	h.run(w, r, query, map[string]any{"page": page, "limit": limit})
}

func (h *Handler) groupedResources(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	q := r.URL.Query()
	group1 := q.Get("group1")
	group2 := q.Get("group2")
	if !cost.ValidGroupedResourceDimension(group1) {
		bad(w, fmt.Errorf("unsupported group1"))
		return
	}
	if group2 != "" && !cost.ValidGroupedResourceDimension(group2) {
		bad(w, fmt.Errorf("unsupported group2"))
		return
	}
	if group1 == group2 && group2 != "" {
		bad(w, fmt.Errorf("group1 and group2 must differ"))
		return
	}
	if q.Get("group2_value") != "" && (group2 == "" || q.Get("group1_value") == "") {
		bad(w, fmt.Errorf("group2_value requires group1_value and group2"))
		return
	}
	if grain := q.Get("grain"); grain != "" && grain != "month" {
		bad(w, fmt.Errorf("grain must be month"))
		return
	}
	query, err := cost.GroupedResourcesQuery(f, group1, group2, q.Get("group1_value"), q.Get("group2_value"), q.Get("q"), q.Get("hide_zero") == "true")
	if err != nil {
		bad(w, err)
		return
	}
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	result, fresh, err := h.service.Run(ctx, query)
	if err != nil {
		slog.Error("clickhouse grouped resources query failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "UPSTREAM_ERROR", "cost data is unavailable")
		return
	}
	rows := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		rows = append(rows, groupedResourceRow(row))
	}
	writeJSON(w, http.StatusOK, envelope{Data: rows, Meta: map[string]any{"freshness": fresh}})
}

func groupedResourceRow(source map[string]any) map[string]any {
	kind, _ := source["kind"].(string)
	keys := []string{"kind", "depth", "group_value", "currency", "subtotal_cost", "row_count"}
	if kind == "leaf" {
		keys = []string{"kind", "period", "environment", "cost_center", "component_type", "compartment", "service", "resource_type", "resource_name", "ocid", "currency", "cost"}
	}
	row := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := source[key]; ok {
			row[key] = value
		}
	}
	return row
}
func (h *Handler) resource(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	ocid := r.PathValue("ocid")
	if ocid == "" || len(ocid) > 512 {
		bad(w, fmt.Errorf("invalid ocid"))
		return
	}
	h.run(w, r, cost.ResourceQuery(f, ocid), map[string]any{})
}
func (h *Handler) lineItems(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "day"
	}
	query, err := cost.LineItemsRollupQuery(f, granularity)
	if err != nil {
		bad(w, err)
		return
	}
	h.run(w, r, query, map[string]any{"granularity": granularity})
}

func boundedFloat(raw string, fallback, min, max float64) (float64, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("must be between %g and %g", min, max)
	}
	return value, nil
}

func (h *Handler) anomalies(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	q := r.URL.Query()
	dimension := q.Get("dimension")
	if dimension == "" {
		dimension = "service"
	}
	window, err := boundedInt(q.Get("window"), 28, 7, 90)
	if err != nil {
		bad(w, fmt.Errorf("window %w", err))
		return
	}
	minZ, err := boundedFloat(q.Get("min_z"), 3, 1, 20)
	if err != nil {
		bad(w, fmt.Errorf("min_z %w", err))
		return
	}
	minImpact, err := boundedFloat(q.Get("min_impact"), 50, 0, 1e9)
	if err != nil {
		bad(w, fmt.Errorf("min_impact %w", err))
		return
	}
	query, err := cost.AnomaliesQuery(f, dimension, window, minZ, minImpact)
	if err != nil {
		bad(w, err)
		return
	}
	h.run(w, r, query, map[string]any{"dimension": dimension, "method": "mad_zscore", "window": window, "min_z": minZ, "min_impact": minImpact})
}

func (h *Handler) trends(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	q := r.URL.Query()
	dimension := q.Get("dimension")
	if dimension == "" {
		dimension = "service"
	}
	granularity := q.Get("granularity")
	if granularity == "" {
		granularity = "day"
	}
	query, err := cost.TrendsQuery(f, dimension, granularity)
	if err != nil {
		bad(w, err)
		return
	}
	h.run(w, r, query, map[string]any{"dimension": dimension, "granularity": granularity})
}

func (h *Handler) filters(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseFilters(r)
	if err != nil {
		bad(w, err)
		return
	}
	h.run(w, r, cost.FiltersQuery(f), map[string]any{"required_tags": cost.RequiredTags})
}
func (h *Handler) freshness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.withTimeout(r)
	defer cancel()
	data, err := h.service.Freshness(ctx)
	if err != nil {
		slog.Error("clickhouse freshness query failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "UPSTREAM_ERROR", "cost data is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, envelope{Data: data, Meta: map[string]any{}})
}
func bad(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
}
