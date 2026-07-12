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
	mux.HandleFunc("GET /v1/costs/resources", h.resources)
	mux.HandleFunc("GET /v1/costs/resources/{ocid}", h.resource)
	mux.HandleFunc("GET /v1/costs/lineitems", h.lineItems)
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
	query, err := cost.BreakdownQuery(f, dimension, limit)
	if err != nil {
		bad(w, err)
		return
	}
	h.run(w, r, query, map[string]any{"dimension": dimension})
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
	// line items must be narrowed to one resource by OCID or name
	if f.OCID == "" && f.ResourceName == "" {
		bad(w, fmt.Errorf("ocid or resource_name is required"))
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
