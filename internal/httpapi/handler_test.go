package httpapi

import (
	"context"
	"encoding/json"
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

func TestGroupedResourcesValidation(t *testing.T) {
	for _, path := range []string{
		"/v1/costs/resources/grouped",
		"/v1/costs/resources/grouped?group1=ocid",
		"/v1/costs/resources/grouped?group1=service&group2=service",
		"/v1/costs/resources/grouped?group1=service&grain=day",
		"/v1/costs/resources/grouped?group1=service&group2_value=x",
	} {
		rec := httptest.NewRecorder()
		testHandler().Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestGroupedResourcesReturnsDiscriminatedRowsAndEmptyArrays(t *testing.T) {
	repo := &captureRepo{rows: []map[string]any{
		{"kind": "group", "depth": 0, "group_value": "dev", "currency": "USD", "subtotal_cost": "12.50", "row_count": uint64(2), "ignored": true},
		{"kind": "other", "depth": 0, "group_value": "Other", "currency": "USD", "subtotal_cost": "3.00", "row_count": uint64(1)},
		{"kind": "leaf", "period": "2026-01", "environment": "dev", "cost_center": "cc", "component_type": "app", "compartment": "prod", "service": "COMPUTE", "resource_type": "instance", "resource_name": "app-1", "ocid": "ocid1", "currency": "USD", "cost": "12.50", "subtotal_cost": "ignored"},
	}}
	rec := httptest.NewRecorder()
	captureHandler(repo).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/costs/resources/grouped?group1=environment&group2=cost_center&group1_value=dev&q=app&grain=month", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 3 || body.Data[0]["kind"] != "group" || body.Data[2]["period"] != "2026-01" {
		t.Fatalf("unexpected rows: %s", rec.Body.String())
	}
	if _, ok := body.Data[0]["ignored"]; ok {
		t.Fatalf("group response must not leak query-only fields: %s", rec.Body.String())
	}
	if !strings.Contains(repo.query.SQL, "cost_currencycode = 'USD'") || !strings.Contains(repo.query.SQL, "ILIKE ?") {
		t.Fatalf("grouped query missing USD or search guard: %s", repo.query.SQL)
	}

	empty := &captureRepo{}
	rec = httptest.NewRecorder()
	captureHandler(empty).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/costs/resources/grouped?group1=period", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("empty grouped result must be an array: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
