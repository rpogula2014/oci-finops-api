package cost

import (
	"context"
	"time"
)

const SourceView = "oci_cost_report_attributed"

var RequiredTags = map[string]string{
	"env": "ATD-Billing.Environment", "cost_center": "ATD-Billing.CostCenter",
	"component_type": "ATD-Billing.ComponentType", "resource_type": "ATD-Ops.ResourceType",
	"resource_name": "ATD-Ops.ResourceName",
}

type Filters struct {
	Start, End                                                                                     time.Time
	Environment, CostCenter, ComponentType, Compartment, Service, ResourceType, ResourceName, OCID string
}

type Page struct {
	Limit, Offset   int
	Sort, Direction string
}
type Query struct {
	SQL  string
	Args []any
}
type QueryResult struct {
	Rows  []map[string]any `json:"rows"`
	Total uint64           `json:"total,omitempty"`
}
type Meta struct {
	DataThrough *time.Time `json:"data_through,omitempty"`
	LoadedAt    *time.Time `json:"loaded_at,omitempty"`
}

type Repository interface {
	Ping(ctx context.Context) error
	Execute(ctx context.Context, query Query) (QueryResult, error)
	Freshness(ctx context.Context) (Meta, error)
}
