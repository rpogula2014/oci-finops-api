package cost

import (
	"fmt"
	"strings"
)

// The source view exposes raw OCI tags as Map(String, String); normalized
// dimension columns do not exist, so every tag dimension is a map lookup.
const (
	envExpr          = "tags['ATD-Billing.Environment']"
	ccExpr           = "tags['ATD-Billing.CostCenter']"
	compExpr         = "tags['ATD-Billing.ComponentType']"
	rtypeExpr        = "tags['ATD-Ops.ResourceType']"
	rnameExpr        = "tags['ATD-Ops.ResourceName']"
	rnameDisplayExpr = "if(empty(" + rnameExpr + "), concat('untagged · ', product_service, ' · …', right(product_resourceid, 8)), " + rnameExpr + ")"
	// Grouping expression: untagged resources collapse into one empty-valued bucket
	// (surfaced as "(untagged)" in the UI) rather than fragmenting per-OCID like the
	// display expression does.
	rnameGroupExpr = "if(empty(" + rnameExpr + "), '', " + rnameExpr + ")"
)

var dimensions = map[string]string{
	"service": "product_service", "compartment": "product_compartmentname", "environment": envExpr,
	"cost_center": ccExpr, "component_type": compExpr, "resource_type": rtypeExpr, "resource_name": rnameDisplayExpr,
}
var sorts = map[string]string{"cost": "cost", "resource_name": "resource_name", "service": "service", "compartment": "compartment"}
var buckets = map[string]string{"hour": "toStartOfHour", "day": "toStartOfDay", "week": "toMonday", "month": "toStartOfMonth"}

const groupedResourcesLimit = 200

var groupedResourceDimensions = map[string]string{
	"service": "product_service", "compartment": "product_compartmentname", "environment": envExpr,
	"cost_center": ccExpr, "component_type": compExpr, "resource_type": rtypeExpr, "resource_name": rnameGroupExpr,
	"period": "formatDateTime(toStartOfMonth(lineitem_intervalusagestart), '%Y-%m')",
}

func where(f Filters) (string, []any) {
	parts := []string{"lineitem_intervalusagestart >= ?", "lineitem_intervalusagestart < ?"}
	args := []any{f.Start, f.End}
	for _, item := range []struct{ column, value string }{
		{envExpr, f.Environment}, {ccExpr, f.CostCenter}, {compExpr, f.ComponentType}, {"product_compartmentname", f.Compartment},
		{"product_service", f.Service}, {rtypeExpr, f.ResourceType}, {rnameDisplayExpr, f.ResourceName}, {"product_resourceid", f.OCID},
	} {
		// "__untagged__" selects rows where the dimension is empty; "" still means unfiltered
		if item.value == "__untagged__" && item.column != rnameDisplayExpr {
			parts = append(parts, item.column+" = ''")
		} else if item.value != "" {
			parts = append(parts, item.column+" = ?")
			args = append(args, item.value)
		}
	}
	return strings.Join(parts, " AND "), args
}

func SummaryQuery(f Filters) Query {
	w, a := where(f)
	return Query{fmt.Sprintf("SELECT cost_currencycode currency, round(sum(cost_attributedcost), 2) cost, uniqExact(product_resourceid) resources, count() line_items FROM %s WHERE %s GROUP BY currency ORDER BY cost DESC", SourceView, w), a}
}

func TimeseriesQuery(f Filters, granularity string) (Query, error) {
	bucket, ok := buckets[granularity]
	if !ok {
		return Query{}, fmt.Errorf("unsupported granularity")
	}
	w, a := where(f)
	return Query{fmt.Sprintf("SELECT %s(lineitem_intervalusagestart) bucket, cost_currencycode currency, round(sum(cost_attributedcost), 2) cost FROM %s WHERE %s GROUP BY bucket, currency ORDER BY bucket, currency", bucket, SourceView, w), a}, nil
}

func BreakdownQuery(f Filters, dimension string, limit int) (Query, error) {
	column, ok := dimensions[dimension]
	if !ok {
		return Query{}, fmt.Errorf("unsupported dimension")
	}
	w, a := where(f)
	a = append(a, limit)
	return Query{fmt.Sprintf("SELECT %s dimension_value, cost_currencycode currency, round(sum(cost_attributedcost), 2) cost, uniqExact(product_resourceid) resources FROM %s WHERE %s GROUP BY dimension_value, currency ORDER BY cost DESC LIMIT ?", column, SourceView, w), a}, nil
}

// BreakdownSeriesQuery returns the same top-level breakdown rows along with a
// time series for each row. It keeps the aggregation in one ClickHouse query,
// rather than requiring the client to make one timeseries request per row.
func BreakdownSeriesQuery(f Filters, dimension string, limit int, granularity string) (Query, error) {
	column, ok := dimensions[dimension]
	if !ok {
		return Query{}, fmt.Errorf("unsupported dimension")
	}
	bucket, ok := buckets[granularity]
	if !ok {
		return Query{}, fmt.Errorf("unsupported granularity")
	}
	w, a := where(f)
	seriesWhere, seriesArgs := where(f)
	args := append(append(a, limit), seriesArgs...)
	return Query{fmt.Sprintf(`WITH b AS (
  SELECT %[1]s dimension_value, cost_currencycode currency,
         toString(round(sum(cost_attributedcost), 2)) cost,
         uniqExact(product_resourceid) resources,
         sum(cost_attributedcost) sort_cost
  FROM %[2]s WHERE %[3]s
  GROUP BY dimension_value, currency
  ORDER BY sort_cost DESC
  LIMIT ?
)
SELECT b.dimension_value, b.currency, b.cost, b.resources, s.date, s.series_cost
FROM b
LEFT JOIN (
  SELECT %[1]s dimension_value, cost_currencycode currency,
         formatDateTime(%[4]s(lineitem_intervalusagestart), '%%FT%%TZ') date,
         toString(round(sum(cost_attributedcost), 2)) series_cost
  FROM %[2]s WHERE %[5]s
    AND (%[1]s, cost_currencycode) IN (SELECT dimension_value, currency FROM b)
  GROUP BY dimension_value, currency, date
) s ON b.dimension_value = s.dimension_value AND b.currency = s.currency
ORDER BY b.sort_cost DESC, s.date`, column, SourceView, w, bucket, seriesWhere), args}, nil
}

func ResourcesQuery(f Filters, p Page) (Query, error) {
	sort, ok := sorts[p.Sort]
	if !ok {
		return Query{}, fmt.Errorf("unsupported sort")
	}
	direction := strings.ToUpper(p.Direction)
	if direction != "ASC" && direction != "DESC" {
		return Query{}, fmt.Errorf("unsupported direction")
	}
	w, a := where(f)
	a = append(a, p.Limit, p.Offset)
	return Query{fmt.Sprintf("SELECT product_resourceid ocid, any(%s) resource_name, any(product_service) service, any(product_compartmentname) compartment, any(product_region) region, any(%s) environment, any(%s) cost_center, any(%s) component_type, any(%s) resource_type, cost_currencycode currency, round(sum(cost_attributedcost), 2) cost, count() OVER () total FROM %s WHERE %s GROUP BY ocid, currency ORDER BY %s %s LIMIT ? OFFSET ?", rnameDisplayExpr, envExpr, ccExpr, compExpr, rtypeExpr, SourceView, w, sort, direction), a}, nil
}

// ValidGroupedResourceDimension excludes OCID because it identifies a leaf, not a group.
func ValidGroupedResourceDimension(dimension string) bool {
	_, ok := groupedResourceDimensions[dimension]
	return ok
}

// GroupedResourcesQuery selects either group rows or resource-month leaves for one
// expansion level. Values only ever become query arguments; dimensions come from the
// service-owned allowlist above.
func GroupedResourcesQuery(f Filters, group1, group2, group1Value, group2Value, search string, hideZero bool) (Query, error) {
	group1Column, ok := groupedResourceDimensions[group1]
	if !ok {
		return Query{}, fmt.Errorf("unsupported grouped resource dimension")
	}
	if group2 != "" && !ValidGroupedResourceDimension(group2) {
		return Query{}, fmt.Errorf("unsupported grouped resource dimension")
	}
	if group1 == group2 && group2 != "" {
		return Query{}, fmt.Errorf("group dimensions must differ")
	}

	w, args := where(f)
	w += " AND cost_currencycode = 'USD'"
	if group1Value != "" {
		w, args = groupedResourceScope(w, args, group1Column, group1, group1Value)
	}
	if group2Value != "" {
		w, args = groupedResourceScope(w, args, groupedResourceDimensions[group2], group2, group2Value)
	}
	if term := strings.TrimSpace(search); term != "" {
		pattern := "%" + term + "%"
		columns := []string{rnameDisplayExpr, "product_resourceid", "product_service", "product_compartmentname", "product_region", rtypeExpr, envExpr, ccExpr, compExpr}
		matches := make([]string, len(columns))
		for i, column := range columns {
			matches[i] = column + " ILIKE ?"
			args = append(args, pattern)
		}
		w += " AND (" + strings.Join(matches, " OR ") + ")"
	}

	// Hide-noise: drop groups/leaves whose rounded cost is exactly zero.
	having := ""
	if hideZero {
		having = "\n  HAVING round(sum(cost_attributedcost), 2) != 0"
	}

	if group1Value == "" {
		return groupedResourceGroupsQuery(w, args, group1Column, 0, having), nil
	}
	if group2 != "" && group2Value == "" {
		return groupedResourceGroupsQuery(w, args, groupedResourceDimensions[group2], 1, having), nil
	}
	depth := 1
	if group2 != "" {
		depth = 2
	}
	return groupedResourceLeavesQuery(w, args, depth, having), nil
}

func groupedResourceScope(w string, args []any, column, dimension, value string) (string, []any) {
	// Untagged selects empty-tag rows via `column = ''`. resource_name is included
	// here (unlike the flat filter) because its grouping expression maps untagged to
	// '' — see rnameGroupExpr. period is excluded: its bucket is never empty.
	if value == "__untagged__" && dimension != "period" {
		return w + " AND " + column + " = ''", args
	}
	return w + " AND " + column + " = ?", append(args, value)
}

func groupedResourceGroupsQuery(w string, args []any, column string, depth int, having string) Query {
	return Query{fmt.Sprintf(`WITH grouped AS (
  SELECT %[1]s group_value, sum(cost_attributedcost) subtotal_cost, count() row_count
  FROM %[2]s WHERE %[3]s
  GROUP BY group_value%[6]s
), ranked AS (
  SELECT *, row_number() OVER (ORDER BY subtotal_cost DESC) rank FROM grouped
)
SELECT kind, depth, group_value, currency, toString(round(sort_cost, 2)) subtotal_cost, row_count
FROM (
  SELECT 'group' kind, %[4]d depth, group_value, 'USD' currency, subtotal_cost sort_cost, row_count, 0 is_other
  FROM ranked WHERE rank <= %[5]d
  UNION ALL
  SELECT 'other' kind, %[4]d depth, 'Other' group_value, 'USD' currency, sum(subtotal_cost) sort_cost, sum(row_count) row_count, 1 is_other
  FROM ranked WHERE rank > %[5]d HAVING count() > 0
)
ORDER BY is_other, sort_cost DESC`, column, SourceView, w, depth, groupedResourcesLimit, having), args}
}

func groupedResourceLeavesQuery(w string, args []any, depth int, having string) Query {
	period := groupedResourceDimensions["period"]
	return Query{fmt.Sprintf(`WITH leaves AS (
  SELECT product_resourceid ocid,
         any(%[1]s) resource_name,
         any(product_service) service,
         any(product_compartmentname) compartment,
         any(product_region) region,
         any(%[2]s) environment,
         any(%[3]s) cost_center,
         any(%[4]s) component_type,
         any(%[5]s) resource_type,
         %[6]s period,
         sum(cost_attributedcost) cost,
         count() row_count
  FROM %[7]s WHERE %[8]s
  GROUP BY ocid, period%[11]s
), ranked AS (
  SELECT *, row_number() OVER (ORDER BY cost DESC) rank FROM leaves
)
SELECT kind, depth, group_value, currency, subtotal_cost, row_count,
       period, environment, cost_center, component_type, compartment, service, resource_type, resource_name, ocid, cost
FROM (
  SELECT 'leaf' kind, %[9]d depth, '' group_value, 'USD' currency, '' subtotal_cost, 0 row_count,
         period, environment, cost_center, component_type, compartment, service, resource_type, resource_name, ocid,
         toString(round(ranked.cost, 2)) cost, ranked.cost sort_cost, 0 is_other
  FROM ranked WHERE rank <= %[10]d
  UNION ALL
  SELECT 'other' kind, %[9]d depth, 'Other' group_value, 'USD' currency,
         toString(round(sum(ranked.cost), 2)) subtotal_cost, sum(row_count),
         '', '', '', '', '', '', '', '', '', '', sum(ranked.cost) sort_cost, 1 is_other
  FROM ranked WHERE rank > %[10]d HAVING count() > 0
)
ORDER BY is_other, sort_cost DESC`, rnameDisplayExpr, envExpr, ccExpr, compExpr, rtypeExpr, period, SourceView, w, depth, groupedResourcesLimit, having), args}
}

func ResourceQuery(f Filters, ocid string) Query {
	f.OCID = ocid
	w, a := where(f)
	return Query{fmt.Sprintf("SELECT product_resourceid ocid, any(%s) resource_name, any(product_description) description, any(product_service) service, any(product_compartmentid) compartment_id, any(product_compartmentname) compartment, any(product_region) region, any(product_availabilitydomain) availability_domain, any(%s) environment, any(%s) cost_center, any(%s) component_type, any(%s) resource_type, cost_currencycode currency, round(sum(cost_attributedcost), 2) cost, min(lineitem_intervalusagestart) first_seen, max(lineitem_intervalusageend) last_seen FROM %s WHERE %s GROUP BY ocid, currency", rnameDisplayExpr, envExpr, ccExpr, compExpr, rtypeExpr, SourceView, w), a}
}

// LineItemsRollupQuery buckets a resource's line items by day/week/month.
func LineItemsRollupQuery(f Filters, granularity string) (Query, error) {
	bucket, ok := buckets[granularity]
	if !ok || granularity == "hour" {
		return Query{}, fmt.Errorf("granularity must be day, week or month")
	}
	w, a := where(f)
	return Query{fmt.Sprintf("SELECT %s(lineitem_intervalusagestart) bucket, cost_currencycode currency, round(sum(cost_attributedcost), 2) cost, round(sum(cost_mycost), 2) my_cost, count() line_items, countIf(cost_overageflag = 'Y') overage_items FROM %s WHERE %s GROUP BY bucket, currency ORDER BY bucket DESC, currency", bucket, SourceView, w), a}, nil
}

// AnomaliesQuery flags days whose cost deviates from the trailing-window median
// by a robust z-score: 0.6745 * (cost - median) / MAD. MAD-based scoring resists
// baseline inflation from past spikes, unlike mean/stddev. The two window levels
// exist because ClickHouse rejects nested window aggregates (ILLEGAL_AGGREGATION);
// MAD is therefore computed against each day's own rolling median. z is clamped to
// ±99 for near-zero MAD, and the current (partial) day is excluded.
func AnomaliesQuery(f Filters, dimension string, window int, minZ, minImpact float64) (Query, error) {
	column, ok := dimensions[dimension]
	if !ok {
		return Query{}, fmt.Errorf("unsupported dimension")
	}
	// Widen the scan so the first reported day has a full baseline behind it.
	warm := f
	warm.Start = f.Start.AddDate(0, 0, -window)
	w, a := where(warm)
	minObs := window / 2
	a = append(a, f.Start, minObs, minImpact, minZ)
	return Query{fmt.Sprintf(`WITH daily AS (
  SELECT %[1]s dimension_value, cost_currencycode currency, toDate(lineitem_intervalusagestart) day, toFloat64(sum(cost_attributedcost)) cost
  FROM %[2]s WHERE %[3]s
  GROUP BY dimension_value, currency, day
), base AS (
  SELECT *, medianExact(cost) OVER w baseline, count(*) OVER w n
  FROM daily WINDOW w AS (PARTITION BY dimension_value, currency ORDER BY day ROWS BETWEEN %[4]d PRECEDING AND 1 PRECEDING)
), scored AS (
  SELECT *, medianExact(abs(cost - baseline)) OVER w mad
  FROM base WINDOW w AS (PARTITION BY dimension_value, currency ORDER BY day ROWS BETWEEN %[4]d PRECEDING AND 1 PRECEDING)
), final AS (
  SELECT dimension_value, currency, day, cost, baseline, n,
         round(greatest(least(0.6745 * (cost - baseline) / nullIf(mad, 0), 99), -99), 2) z_score
  FROM scored
)
SELECT dimension_value, currency, day, round(cost, 2) cost, round(baseline, 2) baseline,
       round(cost - baseline, 2) deviation, z_score,
       if(abs(z_score) >= 5, 'critical', 'warning') severity,
       if(cost > baseline, 'spike', 'drop') direction
FROM final
WHERE day >= toDate(?) AND day < toDate(now('UTC')) AND n >= ? AND abs(cost - baseline) >= ? AND abs(z_score) >= ?
ORDER BY day DESC, abs(z_score) DESC`, column, SourceView, w, window), a}, nil
}

// TrendsQuery compares the requested period against the equal-length period
// immediately before it, per dimension value. slope is the per-day rate of change
// fitted over the current period's buckets (simpleLinearRegression, x in days).
func TrendsQuery(f Filters, dimension, granularity string) (Query, error) {
	column, ok := dimensions[dimension]
	if !ok {
		return Query{}, fmt.Errorf("unsupported dimension")
	}
	bucket, ok := buckets[granularity]
	if !ok || granularity == "hour" {
		return Query{}, fmt.Errorf("granularity must be day, week or month")
	}
	// One scan covers both periods; sumIf splits them at f.Start.
	span := f
	span.Start = f.Start.Add(-f.End.Sub(f.Start))
	w, a := where(span)
	a = append(a, f.Start, f.Start, f.Start)
	// Periods split on day buckets so a week/month straddling f.Start cannot leak
	// current-period spend into previous_cost; granularity only coarsens the slope
	// fit's time axis (slope stays a per-day rate).
	return Query{fmt.Sprintf(`WITH per_day AS (
  SELECT %[1]s dimension_value, cost_currencycode currency, toStartOfDay(lineitem_intervalusagestart) day, toFloat64(sum(cost_attributedcost)) cost
  FROM %[2]s WHERE %[3]s
  GROUP BY dimension_value, currency, day
), agg AS (
  SELECT dimension_value, currency,
         sumIf(cost, day >= ?) current_cost,
         sumIf(cost, day < ?) previous_cost,
         (simpleLinearRegressionIf(toFloat64(toUnixTimestamp(toDateTime(%[4]s(day)))) / 86400, cost, day >= ?)).1 slope
  FROM per_day GROUP BY dimension_value, currency
)
SELECT dimension_value, currency, round(current_cost, 2) current_cost, round(previous_cost, 2) previous_cost,
       round(current_cost - previous_cost, 2) change_amount,
       round((current_cost - previous_cost) / nullIf(previous_cost, 0) * 100, 2) change_pct,
       if(isFinite(slope), round(slope, 4), NULL) slope,
       multiIf(previous_cost = 0 AND current_cost > 0, 'new',
               current_cost = 0 AND previous_cost > 0, 'gone',
               abs(current_cost - previous_cost) / nullIf(previous_cost, 0) * 100 < 5, 'flat',
               current_cost > previous_cost, 'rising', 'falling') direction
FROM agg
ORDER BY abs(current_cost - previous_cost) DESC`, column, SourceView, w, bucket), a}, nil
}

func FiltersQuery(f Filters) Query {
	w, a := where(f)
	return Query{fmt.Sprintf("SELECT groupUniqArray(1000)(%s) environments, groupUniqArray(1000)(%s) cost_centers, groupUniqArray(1000)(%s) component_types, groupUniqArray(1000)(product_compartmentname) compartments, groupUniqArray(1000)(product_service) services, groupUniqArray(1000)(%s) resource_types, groupUniqArray(1000)(%s) resource_names FROM %s WHERE %s", envExpr, ccExpr, compExpr, rtypeExpr, rnameDisplayExpr, SourceView, w), a}
}
