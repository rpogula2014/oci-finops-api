package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/costscope-api/internal/cost"
)

// execRepo returns breakdown-shaped rows so the handler fans out per-name series.
type execRepo struct {
	freshnessErr error
}

func (execRepo) Ping(context.Context) error { return nil }
func (execRepo) Execute(_ context.Context, q cost.Query) (cost.QueryResult, error) {
	if strings.Contains(q.SQL, "dimension_value") {
		return cost.QueryResult{Rows: []map[string]any{
			{"dimension_value": "alpha", "currency": "USD", "cost": 10.0},
			{"dimension_value": "alpha", "currency": "EUR", "cost": 2.0}, // dup name, other currency
			{"dimension_value": "beta", "currency": "USD", "cost": 5.0},
		}}, nil
	}
	return cost.QueryResult{Rows: []map[string]any{{"cost": 42}}}, nil
}
func (r execRepo) Freshness(context.Context) (cost.Meta, error) {
	if r.freshnessErr != nil {
		return cost.Meta{}, r.freshnessErr
	}
	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	return cost.Meta{DataThrough: &now}, nil
}

func execHandler(repo cost.Repository) *Handler {
	handler := NewHandler(cost.NewService(repo), time.Second)
	handler.now = func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) }
	return handler
}

func TestExecSummaryHappyPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/costs/exec-summary?dimension=cost_center&top=7", nil)
	rec := httptest.NewRecorder()
	execHandler(execRepo{}).Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data cost.ExecSummary `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d := body.Data
	if len(d.Summary) == 0 || len(d.Monthly) == 0 || len(d.CostCenters) == 0 || len(d.Environments) == 0 || len(d.TopBreakdown) == 0 {
		t.Fatalf("missing sections: %+v", d)
	}
	// dup "alpha" rows collapse to one series; order follows breakdown cost order
	if len(d.TopSeries) != 2 || d.TopSeries[0].Name != "alpha" || d.TopSeries[1].Name != "beta" {
		t.Fatalf("top_series=%+v", d.TopSeries)
	}
	if d.Freshness == nil || d.Freshness.DataThrough == nil {
		t.Fatalf("freshness missing: %+v", d.Freshness)
	}
}

func TestExecSummaryInvalidDimension(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/costs/exec-summary?dimension=bogus", nil)
	rec := httptest.NewRecorder()
	execHandler(execRepo{}).Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"VALIDATION_ERROR"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestExecSummaryFreshnessBestEffort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/costs/exec-summary", nil)
	rec := httptest.NewRecorder()
	execHandler(execRepo{freshnessErr: errors.New("boom")}).Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"freshness":null`) {
		t.Fatalf("expected freshness:null, body=%s", rec.Body.String())
	}
}
