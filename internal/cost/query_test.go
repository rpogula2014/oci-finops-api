package cost

import (
	"fmt"
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
	if !strings.Contains(query.SQL, rnameDisplayExpr) || !strings.Contains(query.SQL, "product_service") || !strings.Contains(query.SQL, "right(product_resourceid, 8)") {
		t.Fatal("missing required resource-name fallback")
	}
}

func TestResourceNameCompositeIsUsedForBreakdownFiltersAndOptions(t *testing.T) {
	breakdown, err := BreakdownQuery(fixture(), "resource_name", 20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(breakdown.SQL, rnameDisplayExpr) {
		t.Fatalf("breakdown must group on the display expression: %s", breakdown.SQL)
	}

	f := fixture()
	f.ResourceName = "untagged · Compute · …12345678"
	filtered := SummaryQuery(f)
	if !strings.Contains(filtered.SQL, rnameDisplayExpr+" = ?") || filtered.Args[len(filtered.Args)-1] != f.ResourceName {
		t.Fatalf("composite resource name must round-trip as a bound filter: %#v", filtered)
	}

	options := FiltersQuery(fixture())
	if !strings.Contains(options.SQL, rnameDisplayExpr) {
		t.Fatalf("filter options must use the display expression: %s", options.SQL)
	}
}

func TestResourceNameDoesNotUseUntaggedSentinel(t *testing.T) {
	f := fixture()
	f.ResourceName = "__untagged__"
	query := SummaryQuery(f)
	if strings.Contains(query.SQL, rnameDisplayExpr+" = ''") || query.Args[len(query.Args)-1] != "__untagged__" {
		t.Fatalf("resource-name sentinel must be a literal composite filter value: %#v", query)
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

func TestGroupedResourcesQueryScopesAndSearchesWithBoundValues(t *testing.T) {
	query, err := GroupedResourcesQuery(fixture(), "environment", "cost_center", "dev", "", "TREADSY", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query.SQL, "GROUP BY group_value") || !strings.Contains(query.SQL, ccExpr+" group_value") {
		t.Fatalf("second-level query must group by cost center: %s", query.SQL)
	}
	if !strings.Contains(query.SQL, "cost_currencycode = 'USD'") || !strings.Contains(query.SQL, "ILIKE ?") {
		t.Fatalf("grouped query must guard USD and use a parameterized search: %s", query.SQL)
	}
	for _, value := range query.Args {
		if value == "TREADSY" || value == "dev" && strings.Contains(query.SQL, "TREADSY") {
			t.Fatalf("request value was interpolated into SQL: %s", query.SQL)
		}
	}
	foundParent, foundSearch := false, false
	for _, value := range query.Args {
		foundParent = foundParent || value == "dev"
		foundSearch = foundSearch || value == "%TREADSY%"
	}
	if !foundParent || !foundSearch {
		t.Fatalf("parent/search values were not bound: %#v", query.Args)
	}
}

func TestGroupedResourcesQueryBuildsLeavesAndOtherRows(t *testing.T) {
	query, err := GroupedResourcesQuery(fixture(), "period", "resource_type", "2026-01", "COMPUTE", "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"'leaf' kind", "'other' kind", fmt.Sprintf("rank > %d", groupedResourcesLimit), "formatDateTime(toStartOfMonth", "GROUP BY ocid, period"} {
		if !strings.Contains(query.SQL, fragment) {
			t.Fatalf("missing %q from leaf query: %s", fragment, query.SQL)
		}
	}
	if !strings.Contains(query.SQL, "cost_currencycode = 'USD'") {
		t.Fatalf("leaf query must exclude non-USD rows: %s", query.SQL)
	}
}

func TestGroupedResourcesQueryHideZeroFiltersZeroCost(t *testing.T) {
	// hideZero must drop $0 rows so subtotals/counts reflect only real spend.
	on, err := GroupedResourcesQuery(fixture(), "environment", "", "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(on.SQL, "HAVING round(sum(cost_attributedcost), 2) != 0") {
		t.Fatalf("hideZero must filter zero-cost groups: %s", on.SQL)
	}
	off, err := GroupedResourcesQuery(fixture(), "environment", "", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off.SQL, "!= 0") {
		t.Fatalf("without hideZero no zero-cost filter should appear: %s", off.SQL)
	}
}

func TestGroupedResourcesQueryHandlesUntaggedAndRejectsInvalidDimensions(t *testing.T) {
	query, err := GroupedResourcesQuery(fixture(), "environment", "", "__untagged__", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query.SQL, envExpr+" = ''") {
		t.Fatalf("untagged group scope must compare empty tag values: %s", query.SQL)
	}
	for _, arg := range query.Args {
		if arg == "__untagged__" {
			t.Fatalf("untagged sentinel must not be bound: %#v", query.Args)
		}
	}
	for _, args := range [][]string{{"ocid", "", "", "", ""}, {"service", "service", "", "", ""}} {
		if _, err := GroupedResourcesQuery(fixture(), args[0], args[1], args[2], args[3], args[4], false); err == nil {
			t.Fatalf("expected dimension validation for %#v", args)
		}
	}
}
