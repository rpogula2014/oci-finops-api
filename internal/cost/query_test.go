package cost

import (
	"strings"
	"testing"
	"time"
)

func fixture() Filters {
	return Filters{Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Service: "Compute' OR 1=1"}
}

func TestValuesAreBound(t *testing.T) {
	query := SummaryQuery(fixture())
	if strings.Contains(query.SQL, "Compute' OR 1=1") {
		t.Fatal("user value interpolated into SQL")
	}
	if got := query.Args[len(query.Args)-1]; got != "Compute' OR 1=1" {
		t.Fatalf("unexpected bound value: %v", got)
	}
}

func TestIdentifierAllowlists(t *testing.T) {
	if _, err := BreakdownQuery(fixture(), "cost); DROP TABLE x", 20); err == nil {
		t.Fatal("expected dimension rejection")
	}
	if _, err := TimeseriesQuery(fixture(), "minute"); err == nil {
		t.Fatal("expected granularity rejection")
	}
	if _, err := ResourcesQuery(fixture(), Page{Limit: 50, Sort: "random()", Direction: "desc"}); err == nil {
		t.Fatal("expected sort rejection")
	}
	if _, err := ResourcesQuery(fixture(), Page{Limit: 50, Sort: "cost", Direction: "desc; drop"}); err == nil {
		t.Fatal("expected direction rejection")
	}
}

func TestResourceNameFallback(t *testing.T) {
	query, err := ResourcesQuery(fixture(), Page{Limit: 50, Sort: "cost", Direction: "desc"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query.SQL, "untagged · ") {
		t.Fatal("missing required resource-name fallback")
	}
}

func TestBreakdownSeriesQueryUsesOneBoundQuery(t *testing.T) {
	query, err := BreakdownSeriesQuery(fixture(), "service", 15, "day")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query.SQL, "series_cost") || !strings.Contains(query.SQL, "formatDateTime") || !strings.Contains(query.SQL, "IN (SELECT dimension_value, currency FROM b)") {
		t.Fatalf("missing series projection: %s", query.SQL)
	}
	if len(query.Args) != 7 { // date range + filter, limit, then date range + filter again
		t.Fatalf("args=%v", query.Args)
	}
	if query.Args[2] != fixture().Service || query.Args[3] != 15 || query.Args[6] != fixture().Service {
		t.Fatalf("unexpected argument order: %#v", query.Args)
	}
	if _, err := BreakdownSeriesQuery(fixture(), "service", 15, "minute"); err == nil {
		t.Fatal("expected granularity rejection")
	}
}

func TestUntaggedFiltersUseEmptyComparisonWithoutBindingSentinel(t *testing.T) {
	f := fixture()
	f.Service = "__untagged__"
	query, err := BreakdownSeriesQuery(f, "service", 15, "day")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query.SQL, "product_service = ''") {
		t.Fatalf("untagged comparison missing: %s", query.SQL)
	}
	for _, arg := range query.Args {
		if arg == "__untagged__" {
			t.Fatalf("sentinel must not be bound: %#v", query.Args)
		}
	}
}
