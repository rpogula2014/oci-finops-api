package httpapi

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/costscope-api/internal/cost"
)

func TestAnomaliesValidation(t *testing.T) {
	for _, path := range []string{
		"/v1/costs/anomalies?dimension=ocid",
		"/v1/costs/anomalies?window=6",
		"/v1/costs/anomalies?window=91",
		"/v1/costs/anomalies?min_z=0.5",
		"/v1/costs/anomalies?min_impact=-1",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		testHandler().Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"code":"VALIDATION_ERROR"`) {
			t.Fatalf("%s body=%s", path, rec.Body.String())
		}
	}
}

func TestAnomaliesDefaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/costs/anomalies", nil)
	rec := httptest.NewRecorder()
	testHandler().Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"dimension":"service"`, `"method":"mad_zscore"`, `"window":28`, `"min_z":3`, `"min_impact":50`} {
		if !strings.Contains(body, want) {
			t.Fatalf("meta missing %s: %s", want, body)
		}
	}
}

func TestTrendsValidationAndDefaults(t *testing.T) {
	for _, path := range []string{
		"/v1/costs/trends?dimension=ocid",
		"/v1/costs/trends?granularity=hour",
		"/v1/costs/trends?granularity=minute",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		testHandler().Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/costs/trends?dimension=cost_center", nil)
	rec := httptest.NewRecorder()
	testHandler().Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"granularity":"day"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

type nanRepo struct{ stubRepo }

func (nanRepo) Execute(context.Context, cost.Query) (cost.QueryResult, error) {
	return cost.QueryResult{Rows: []map[string]any{{"slope": math.NaN()}}}, nil
}

// A non-encodable row must surface as a 500 envelope, never a silent empty 200.
func TestNonEncodableRowFailsLoud(t *testing.T) {
	handler := NewHandler(cost.NewService(nanRepo{}), time.Second)
	handler.now = func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) }
	req := httptest.NewRequest(http.MethodGet, "/v1/costs/trends", nil)
	rec := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}
