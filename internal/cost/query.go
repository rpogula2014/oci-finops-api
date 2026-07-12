package cost

import (
	"fmt"
	"strings"
)

// The source view exposes raw OCI tags as Map(String, String); normalized
// dimension columns do not exist, so every tag dimension is a map lookup.
const (
	envExpr   = "tags['ATD-Billing.Environment']"
	ccExpr    = "tags['ATD-Billing.CostCenter']"
	compExpr  = "tags['ATD-Billing.ComponentType']"
	rtypeExpr = "tags['ATD-Ops.ResourceType']"
	rnameExpr = "tags['ATD-Ops.ResourceName']"
)

var dimensions = map[string]string{
	"service": "product_service", "compartment": "product_compartmentname", "environment": envExpr,
	"cost_center": ccExpr, "component_type": compExpr, "resource_type": rtypeExpr, "resource_name": rnameExpr,
}
var sorts = map[string]string{"cost": "cost", "resource_name": "resource_name", "service": "service", "compartment": "compartment"}
var buckets = map[string]string{"hour": "toStartOfHour", "day": "toStartOfDay", "week": "toMonday", "month": "toStartOfMonth"}

func where(f Filters) (string, []any) {
	parts := []string{"lineitem_intervalusagestart >= ?", "lineitem_intervalusagestart < ?"}
	args := []any{f.Start, f.End}
	for _, item := range []struct{ column, value string }{
		{envExpr, f.Environment}, {ccExpr, f.CostCenter}, {compExpr, f.ComponentType}, {"product_compartmentname", f.Compartment},
		{"product_service", f.Service}, {rtypeExpr, f.ResourceType}, {rnameExpr, f.ResourceName}, {"product_resourceid", f.OCID},
	} {
		// "__untagged__" selects rows where the dimension is empty; "" still means unfiltered
		if item.value == "__untagged__" {
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
	name := fmt.Sprintf("if(empty(%s), concat('untagged · ', product_description), %s)", rnameExpr, rnameExpr)
	return Query{fmt.Sprintf("SELECT product_resourceid ocid, any(%s) resource_name, any(product_service) service, any(product_compartmentname) compartment, any(product_region) region, any(%s) environment, any(%s) cost_center, any(%s) component_type, any(%s) resource_type, cost_currencycode currency, round(sum(cost_attributedcost), 2) cost, count() OVER () total FROM %s WHERE %s GROUP BY ocid, currency ORDER BY %s %s LIMIT ? OFFSET ?", name, envExpr, ccExpr, compExpr, rtypeExpr, SourceView, w, sort, direction), a}, nil
}

func ResourceQuery(f Filters, ocid string) Query {
	f.OCID = ocid
	w, a := where(f)
	name := fmt.Sprintf("if(empty(%s), concat('untagged · ', product_description), %s)", rnameExpr, rnameExpr)
	return Query{fmt.Sprintf("SELECT product_resourceid ocid, any(%s) resource_name, any(product_description) description, any(product_service) service, any(product_compartmentid) compartment_id, any(product_compartmentname) compartment, any(product_region) region, any(product_availabilitydomain) availability_domain, any(%s) environment, any(%s) cost_center, any(%s) component_type, any(%s) resource_type, cost_currencycode currency, round(sum(cost_attributedcost), 2) cost, min(lineitem_intervalusagestart) first_seen, max(lineitem_intervalusageend) last_seen FROM %s WHERE %s GROUP BY ocid, currency", name, envExpr, ccExpr, compExpr, rtypeExpr, SourceView, w), a}
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

func FiltersQuery(f Filters) Query {
	w, a := where(f)
	return Query{fmt.Sprintf("SELECT groupUniqArray(1000)(%s) environments, groupUniqArray(1000)(%s) cost_centers, groupUniqArray(1000)(%s) component_types, groupUniqArray(1000)(product_compartmentname) compartments, groupUniqArray(1000)(product_service) services, groupUniqArray(1000)(%s) resource_types, groupUniqArray(1000)(%s) resource_names FROM %s WHERE %s", envExpr, ccExpr, compExpr, rtypeExpr, rnameExpr, SourceView, w), a}
}
