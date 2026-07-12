//go:build clickhouse

package cost

import (
	"context"
	"crypto/tls"
	"reflect"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

type ClickHouseRepository struct{ conn clickhouse.Conn }

func NewClickHouseRepository(addr, database, username, password string, secure bool, dialTimeout time.Duration) (*ClickHouseRepository, error) {
	options := &clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Database: database, Username: username, Password: password}, DialTimeout: dialTimeout, Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4}}
	if secure {
		options.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	conn, err := clickhouse.Open(options)
	if err != nil {
		return nil, err
	}
	return &ClickHouseRepository{conn: conn}, nil
}

func (r *ClickHouseRepository) Ping(ctx context.Context) error { return r.conn.Ping(ctx) }

func (r *ClickHouseRepository) Execute(ctx context.Context, query Query) (QueryResult, error) {
	rows, err := r.conn.Query(ctx, query.SQL, query.Args...)
	if err != nil {
		return QueryResult{}, err
	}
	defer rows.Close()
	columns := rows.Columns()
	columnTypes := rows.ColumnTypes()
	result := QueryResult{}
	for rows.Next() {
		// native driver requires concrete typed pointers; *any is unsupported
		pointers := make([]any, len(columns))
		for i, ct := range columnTypes {
			pointers[i] = reflect.New(ct.ScanType()).Interface()
		}
		if err := rows.Scan(pointers...); err != nil {
			return QueryResult{}, err
		}
		row := make(map[string]any, len(columns))
		for i, name := range columns {
			row[name] = reflect.ValueOf(pointers[i]).Elem().Interface()
		}
		result.Rows = append(result.Rows, row)
		if total, ok := row["total"].(uint64); ok {
			result.Total = total
		}
	}
	return result, rows.Err()
}

func (r *ClickHouseRepository) Freshness(ctx context.Context) (Meta, error) {
	var dataThrough, loadedAt *time.Time
	err := r.conn.QueryRow(ctx, "SELECT max(lineitem_intervalusageend), max(created_at) FROM "+SourceView).Scan(&dataThrough, &loadedAt)
	return Meta{DataThrough: dataThrough, LoadedAt: loadedAt}, err
}
