# CostScope Cost API

Standalone, read-only Go API for the CostScope dashboard. It queries the existing ClickHouse view `oci_cost_report_attributed` directly; it does not create or mutate ClickHouse objects.

## Architecture assumption

Because only the API structure was requested, this scaffold intentionally uses the Go standard library (`net/http`) and manual constructor injection. That keeps a small read-only service easy to operate and test. Replace the placeholder `github.com/example/costscope-api` module path before adopting it in your repository.

The source view is assumed to already implement correction reconciliation and latest-tags-per-OCID backfill. The service does not duplicate or reinterpret that accounting logic.

## Required cost dimensions

The view is expected to expose these normalized columns in addition to the OCI report fields:

| API filter | View column | Source tag |
|---|---|---|
| `env` | `env` | `ATD-Billing.Environment` |
| `cost_center` | `cc` | `ATD-Billing.CostCenter` |
| `component_type` | `comp` | `ATD-Billing.ComponentType` |
| `resource_type` | `rtype` | `ATD-Ops.ResourceType` |
| `resource_name` | `rname` | `ATD-Ops.ResourceName` |

When `rname` is blank, resource responses use `untagged · product_description`.

## Routes

- `GET /healthz`
- `GET /v1/costs/summary`
- `GET /v1/costs/exec-summary?dimension=cost_center&top=7` — aggregate payload for the executive summary page in one round trip: `data` object with `summary`, `monthly`, `cost_centers`, `environments`, `top_breakdown` (limit 20), `top_series` (one monthly series per top-N `dimension` value, `top` 1–20 default 7), and `freshness` (best-effort, `null` if the freshness query fails). Sub-queries run concurrently server-side; accepts all shared filters.
- `GET /v1/costs/timeseries?granularity=hour|day|month`
- `GET /v1/costs/breakdown?dimension=service|compartment|environment|cost_center|component_type|resource_type|resource_name`
- `GET /v1/costs/resources?page=1&limit=50&sort=cost&direction=desc`
- `GET /v1/costs/resources/{ocid}`
- `GET /v1/costs/lineitems?resource_name=X&granularity=day|week|month` (or `ocid=X`) — bucketed cost detail by resource name or OCID; one of the two is required, granularity defaults to day
- `GET /v1/costs/filters`
- `GET /v1/costs/freshness`
- `GET /openapi.yaml` — embedded OpenAPI 3.0 spec
- `GET /docs` — Swagger UI (assets pinned to swagger-ui-dist@5.17.14 via CDN with SRI)

## Logging

Structured JSON via `log/slog` on stdout. `LOG_LEVEL` env: debug|info|warn|error (default info). Every request is logged with method, path, status, duration_ms, and request_id.

Shared filters: `start`, `end`, `env`, `cost_center`, `component_type`, `compartment`, `service`, `resource_type`, `resource_name`, and `ocid`. Timestamps are RFC3339. The default range is one month, the maximum is 400 days, breakdowns are limited to 100 rows, and resource pages to 500 rows.

All values are bound with ClickHouse placeholders. The only interpolated SQL fragments are service-owned allowlisted dimensions, sort columns/directions, and time buckets. Cost is always `cost_attributedcost` from the reconciled attributed view. Currency remains a grouping dimension so unlike currencies are never silently summed together.

Success and error responses share an envelope:

```json
{"data":[],"meta":{"freshness":{"data_through":"...","loaded_at":"..."}},"error":null}
```

```json
{"data":null,"meta":{},"error":{"code":"VALIDATION_ERROR","message":"..."}}
```

## Run locally

```bash
cp .env.example .env
# Export the values using your preferred environment loader.
go mod download
go run -tags clickhouse ./cmd/cost-api
```

Use a ClickHouse principal with `SELECT` permission only on `oci_cost_report_attributed`. No credentials should be embedded in a client application.

## Test

```bash
go test ./...
```

The focused tests exercise parameter binding, identifier allowlists, resource-name fallback, request validation, envelopes, and routing without requiring ClickHouse.

The official `clickhouse-go/v2` implementation is selected with the `clickhouse` build tag. The default build uses a compile-only stub so unit tests remain independent of the driver and a running database; do not use the untagged binary in production.
