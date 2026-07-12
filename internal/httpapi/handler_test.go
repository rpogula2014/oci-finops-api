package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/costscope-api/internal/cost"
)

type stubRepo struct{}

func (stubRepo) Ping(context.Context) error { return nil }
func (stubRepo) Execute(context.Context, cost.Query) (cost.QueryResult, error) {
	return cost.QueryResult{Rows: []map[string]any{{"cost": 42}}}, nil
}
func (stubRepo) Freshness(context.Context) (cost.Meta, error) { return cost.Meta{}, nil }

func testHandler() *Handler {
	handler := NewHandler(cost.NewService(stubRepo{}), time.Second)
	handler.now = func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) }
	return handler
}

func TestValidationEnvelope(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/costs/resources?limit=501", nil)
	rec := httptest.NewRecorder()
	testHandler().Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"VALIDATION_ERROR"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestHealthAndSummary(t *testing.T) {
	for _, path := range []string{"/healthz", "/v1/costs/summary"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		testHandler().Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"error":null`) {
			t.Fatalf("%s body=%s", path, rec.Body.String())
		}
	}
}
