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
