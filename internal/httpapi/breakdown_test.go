package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/example/costscope-api/internal/cost"
)

type captureRepo struct {
	query cost.Query
	rows  []map[string]any
}

func (r *captureRepo) Ping(context.Context) error { return nil }
func (r *captureRepo) Execute(_ context.Context, q cost.Query) (cost.QueryResult, error) {
	r.query = q
	return cost.QueryResult{Rows: r.rows}, nil
}
func (r *captureRepo) Freshness(context.Context) (cost.Meta, error) { return cost.Meta{}, nil }

func captureHandler(repo cost.Repository) *Handler {
	h := NewHandler(cost.NewService(repo), time.Second)
	h.now = func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) }
	return h
}

func TestFiltersAcceptSharedFilterParams(t *testing.T) {
	repo := &captureRepo{rows: []map[string]any{{"services": []string{"COMPUTE"}}}}
	rec := httptest.NewRecorder()
	captureHandler(repo).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/costs/filters?service=COMPUTE", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	found := false
	for _, arg := range repo.query.Args {
		if arg == "COMPUTE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("shared filter was not bound: %#v", repo.query.Args)
	}
}

func TestBreakdownSeriesGroupsJoinedRows(t *testing.T) {
	repo := &captureRepo{rows: []map[string]any{
		{"dimension_value": "COMPUTE", "currency": "USD", "cost": "30.00", "resources": uint64(1), "date": "2026-01-01T00:00:00Z", "series_cost": "10.00"},
		{"dimension_value": "COMPUTE", "currency": "USD", "cost": "30.00", "resources": uint64(1), "date": "2026-01-02T00:00:00Z", "series_cost": "20.00"},
	}}
	rec := httptest.NewRecorder()
	captureHandler(repo).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/costs/breakdown?series=true", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []struct {
			// decimal strings by contract: series-mode costs must never be JSON floats
			Cost   string `json:"cost"`
			Series []struct {
				Date string `json:"date"`
				Cost string `json:"cost"`
			} `json:"series"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].Cost != "30.00" || len(body.Data[0].Series) != 2 || body.Data[0].Series[1].Cost != "20.00" {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
}

func TestLineItemsAcceptAncestorFilters(t *testing.T) {
	repo := &captureRepo{rows: []map[string]any{{"cost": "1.00"}}}
	rec := httptest.NewRecorder()
	captureHandler(repo).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/costs/lineitems?service=COMPUTE", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
