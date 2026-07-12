//go:build !clickhouse

package cost

import (
	"context"
	"errors"
	"time"
)

// ClickHouseRepository is a compile-only stub used by dependency-free unit tests.
// Production builds must use `-tags clickhouse` to select the official driver implementation.
type ClickHouseRepository struct{}

func NewClickHouseRepository(string, string, string, string, bool, time.Duration) (*ClickHouseRepository, error) {
	return &ClickHouseRepository{}, nil
}
func (*ClickHouseRepository) Ping(context.Context) error {
	return errors.New("build with -tags clickhouse")
}
func (*ClickHouseRepository) Execute(context.Context, Query) (QueryResult, error) {
	return QueryResult{}, errors.New("build with -tags clickhouse")
}
func (*ClickHouseRepository) Freshness(context.Context) (Meta, error) {
	return Meta{}, errors.New("build with -tags clickhouse")
}
