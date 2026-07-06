# Plan 6: Observability Refactor (Honeycomb / Wide Events)

## Assessment

The current instrumentation creates spans correctly but produces **narrow events** —
only the mechanics of each layer are recorded, not the business context that makes them
investigable. During an incident, you can see that a request failed but cannot answer:
- Which provider/resource combination generates the most errors?
- Did a specific AWS region or ACL value cause failures?
- Where in the stack did time get spent (validation vs render vs write)?
- Are errors clustered — are they structured/typed, or bare strings?

The gaps, ordered by investigation value:

| Gap | Layer | Impact |
|-----|-------|--------|
| No `exception.slug` on any error path | all | Cannot GROUP BY error type; unhandled errors are invisible |
| No timing breakdown attributes | handler | Cannot see where request time is spent without child spans |
| No business attributes on root span | handler | BubbleUp has nothing to diff on except HTTP fields |
| No `error = true` boolean | all | Honeycomb error-rate queries require `status_code >= 400` workaround |
| No `service.version` / `service.environment`; `service.name` not config-driven | tracing | Cannot correlate errors with deployments; name silently empty if metadata key absent |
| `http.route` missing (only `url.path`) | handler | `url.path` contains bucket names — unbounded cardinality |
| Storage span does not set `error = true` | storage | Storage errors invisible to error-rate queries |
| `request_body_read` and `validation` not timed | handler | No visibility into where non-generation time goes |

| Raw `http.request.body` on span | handler | PII/data retention risk; unbounded cardinality; replaced by structured `aws.s3.*` attributes |

No complete refactor is required. The span structure is sound. Most changes are additive;
one change (body removal) is a deletion of a liability.

---

## Changes

### 1. Add `service.name`, `service.version`, and `service.environment` to the trace resource

The OTel `resource.Resource` is emitted on every span as fixed metadata. Adding
`service.version` (from a build-time variable or config) and `service.environment`
(from config) lets Honeycomb's BubbleUp instantly surface "errors are on version X" or
"this only happens in staging."

`service.name` currently comes from `cfg.Logger.Metadata["service"]` — a map lookup that
silently returns an empty string if the key is absent. Replace it with a dedicated
`ServiceName` config field that defaults to `"terraform-parse-service"` when not set.

**`internal/config/config.go`** — add fields:

```go
type Config struct {
    ListenAddr  string                    `yaml:"listen_addr"`
    ServiceName string                    `yaml:"service_name"`
    Environment string                    `yaml:"environment"`
    Version     string                    `yaml:"version"`
    Logger      LoggerConfig              `yaml:"logger"`
    Tracing     TracingConfig             `yaml:"tracing"`
    Metrics     MetricsConfig             `yaml:"metrics"`
    Providers   map[string]ProviderConfig `yaml:"providers"`
}
```

Apply the default in `config.Load()` after `provider.Get(...).Populate(&cfg)`:

```go
if cfg.ServiceName == "" {
    cfg.ServiceName = "terraform-parse-service"
}
```

**`internal/tracing/tracing.go`** — extend `New` signature to accept `cfg config.Config`
instead of just the tracing sub-config, and add the extra resource attributes:

```go
res, err := resource.New(ctx,
    resource.WithAttributes(
        semconv.ServiceNameKey.String(cfg.ServiceName),
        semconv.ServiceVersionKey.String(cfg.Version),
        attribute.String("service.environment", cfg.Environment),
    ),
)
```

`cfg.ServiceName` is guaranteed non-empty by the default in `Load()`, so no nil-guard is
needed at the call site.

**`configs/config.yaml`** and **`deploy/config.yaml`**:

```yaml
# service_name is optional — omit to use the default "terraform-parse-service"
environment: "development"   # or "production"
version: "dev"               # set to git SHA in CI: $(git rev-parse --short HEAD)
```

---

### 2. Add `exception.slug` to every error path

Every `return "", err` in the service and handler should carry a static slug. This makes
errors GROUP BY-able, greppable from dashboards to code, and exposes unhandled paths via:

```
VISUALIZE COUNT
WHERE error = true AND exception.slug does-not-exist
GROUP BY http.route
```

**Convention**: `err-<layer>-<reason>`, e.g. `err-storage-mkdir`, `err-handler-body-read`.

Add `error = true` alongside every slug — Honeycomb uses this boolean for its built-in
error rate calculations.

**`internal/handler/aws/s3/bucket.go`** — at each error return:

```go
// body read failure
span.SetAttributes(
    attribute.String("exception.slug", "err-handler-body-read"),
    attribute.Bool("error", true),
)

// JSON decode failure
span.SetAttributes(
    attribute.String("exception.slug", "err-handler-json-decode"),
    attribute.Bool("error", true),
)

// validation failure
span.SetAttributes(
    attribute.String("exception.slug", "err-handler-validation"),
    attribute.Bool("error", true),
)

// generation failure (already has span.SetStatus — add slug)
span.SetAttributes(
    attribute.String("exception.slug", "err-handler-generate"),
    attribute.Bool("error", true),
)
```

**`internal/service/terraform.go`** — in the `recordErr` closure:

```go
recordErr := func(slug string, err error) (string, error) {
    span.SetAttributes(
        attribute.String("exception.slug", slug),
        attribute.Bool("error", true),
    )
    s.m.GenerationTotal.WithLabelValues(g.Provider(), resource, "error").Inc()
    s.m.GenerationDuration.WithLabelValues(g.Provider(), resource).Observe(time.Since(start).Seconds())
    return "", err
}

// usage:
return recordErr("err-service-no-template", err)
return recordErr("err-service-no-writer", err)
return recordErr("err-service-render-template", err)
return recordErr("err-service-storage-write", err)
```

**`internal/storage/filesystem.go`** — add slugs and `error = true`:

```go
// mkdir failure
span.SetAttributes(
    attribute.String("exception.slug", "err-storage-mkdir"),
    attribute.Bool("error", true),
)

// write failure
span.SetAttributes(
    attribute.String("exception.slug", "err-storage-write-file"),
    attribute.Bool("error", true),
)
```

---

### 3. Add business attributes to the root handler span

The handler span currently sets only HTTP mechanics. Adding business context from the
parsed request gives BubbleUp dimensions to diff on:

**`internal/handler/aws/s3/bucket.go`** — after successful validation, set on the root span:

```go
span.SetAttributes(
    attribute.String("aws.s3.region", p.Region),
    attribute.String("aws.s3.acl", p.ACL),
    attribute.String("aws.s3.bucket_name", p.BucketName),
    attribute.String("http.route", "POST /api/aws/v1/s3/buckets"),
)
```

`http.route` replaces `url.path` as the grouping key — it is a static template string
(no user values), making it safe for GROUP BY without cardinality explosion.

`aws.s3.bucket_name` is high-cardinality and useful for tracing specific requests, but
should **not** be used as a Prometheus label (already excluded — it only goes on the span).

---

### 4. Add timing breakdown attributes to the handler span

Rather than creating child spans for JSON decode and validation (which would require
JOINs for cross-request analysis), record durations on the root span. This lets BubbleUp
immediately surface "slow requests spent 200ms in JSON decode."

**`internal/handler/aws/s3/bucket.go`**:

```go
// Body read timing
readStart := time.Now()
body, err := io.ReadAll(r.Body)
span.SetAttributes(attribute.Float64("read_body.duration_ms", float64(time.Since(readStart).Milliseconds())))

// JSON decode timing
decodeStart := time.Now()
var req bucketRequest
err = json.Unmarshal(body, &req)
span.SetAttributes(attribute.Float64("json_decode.duration_ms", float64(time.Since(decodeStart).Milliseconds())))

// Validation timing
validateStart := time.Now()
err = p.Validate()
span.SetAttributes(attribute.Float64("validate.duration_ms", float64(time.Since(validateStart).Milliseconds())))
```

These three attributes come at zero span overhead and are directly queryable:

```
VISUALIZE P99(json_decode.duration_ms), P99(validate.duration_ms)
WHERE service.name = "terraform-parse-service"
```

---

### 5. Add `http.route` and remove `url.path` from base log fields

`url.path` in logs currently carries the literal `/api/aws/v1/s3/buckets` (no user data
here since there are no path parameters), so cardinality is not a problem today — but
`http.route` is the correct semantic field name per OTel conventions and makes logs
consistent with span attributes.

Replace `url.path` with `http.route` in `base` log fields in `ServeHTTP`:

```go
base := []zap.Field{
    zap.String("http.request.method", method),
    zap.String("http.route", "POST /api/aws/v1/s3/buckets"),
    zap.String("network.peer.address", r.RemoteAddr),
    zap.String("trace_id", span.SpanContext().TraceID().String()),
}
```

---

### 6. Set `error = true` on storage span errors

`internal/storage/filesystem.go` currently calls `span.SetStatus(codes.Error, ...)` but
does not set the `error = true` boolean attribute. Honeycomb uses this boolean for its
built-in error queries. Add it alongside each `SetStatus(codes.Error, ...)` call (covered
by slug additions in change 2).

---

### 7. Config additions

**`configs/config.yaml`**:
```yaml
environment: "development"
version: "dev"
```

**`deploy/config.yaml`**:
```yaml
environment: "production"
version: "dev"
```

In CI, override `version` via environment variable:

```yaml
# docker-compose.yaml server environment:
environment:
  CONFIG_PATH: /etc/server/config.yaml
  VERSION: ${GIT_SHA:-dev}
```

Then reference it in config as `version: "${VERSION}"` (uber/config supports env expansion).

---

### 8. Remove request body from traces

`internal/handler/aws/s3/bucket.go` line 131 sets the raw JSON body as a span attribute
after a successful read:

```go
span.SetAttributes(attribute.String("http.request.body", string(body)))
```

This must be removed. Two reasons:

- **PII / sensitive data risk**: the body contains `bucket-name` and could contain
  credentials or other fields added in future. Raw body on every span is a data-retention
  liability.
- **Cardinality waste**: the full body string is unbounded and high-entropy. It cannot be
  used for GROUP BY or BubbleUp. It only adds noise and storage cost to traces.

The individual fields extracted from the body (`aws.s3.region`, `aws.s3.acl`,
`aws.s3.bucket_name`) are already set as structured attributes after validation (change 3).
Those are the correct form — structured, bounded cardinality, useful for querying.

**`internal/handler/aws/s3/bucket.go`** — remove this line:

```go
// DELETE:
span.SetAttributes(attribute.String("http.request.body", string(body)))
```

No replacement. The body content is represented through structured attributes added after
validation succeeds.

There is no body content in log fields today — the log lines emit only structured fields
(`region`, `acl`, `bucket_name`), never the raw body string. No log change is required.

---

## File Change Summary

| File | Change |
|------|--------|
| `internal/config/config.go` | Add `ServiceName`, `Environment`, `Version` fields to `Config`; apply `ServiceName` default in `Load()` |
| `internal/tracing/tracing.go` | Accept full `Config`; add `service.name`, `service.version`, `service.environment` to resource |
| `internal/handler/aws/s3/bucket.go` | Add `exception.slug` + `error = true` per error path; add `aws.s3.*` business attrs; add `http.route`; add timing breakdown attributes; replace `url.path` with `http.route` in logs; remove `http.request.body` span attribute |
| `internal/service/terraform.go` | Update `recordErr` closure to accept slug param; add `exception.slug` + `error = true` per error path |
| `internal/storage/filesystem.go` | Add `exception.slug` + `error = true` per error path |
| `configs/config.yaml` | Add `environment`, `version` |
| `deploy/config.yaml` | Add `environment`, `version` |
| `cmd/server/main.go` | Pass full `cfg` to `tracing.New` instead of `cfg.Tracing` |

---

## What This Enables

After these changes, the following Honeycomb queries become directly answerable without
re-instrumentation:

| Question | Query |
|----------|-------|
| Which error type fires most? | `COUNT GROUP BY exception.slug WHERE error = true` |
| Unhandled errors (missing slug) | `COUNT WHERE error = true AND exception.slug does-not-exist GROUP BY http.route` |
| Did a new deployment increase errors? | `COUNT WHERE error = true GROUP BY service.version` |
| Where does request time go? | `P99(json_decode.duration_ms), P99(validate.duration_ms), P99(http.server.request.duration) GROUP BY http.route` |
| Which ACL or region correlates with errors? | `COUNT WHERE error = true GROUP BY aws.s3.acl, aws.s3.region` |
| Storage vs service error split | `COUNT WHERE error = true GROUP BY exception.slug` (slug prefix identifies layer) |

---

## What Is Not Changed

- **Span structure**: handler → service → storage hierarchy is correct.
- **Tracing exporter config**: `stdout` / `otlp_grpc` switch works correctly.
- **Prometheus metrics**: already separate concerns, no overlap.
- **Log format**: zap JSON structured logs remain; only the `url.path` → `http.route` rename.
- **Sampling**: `TraceIDRatioBased` is appropriate for this service. No change.

---

## TODO

### Phase 1 — Config

- [x] Add `ServiceName string \`yaml:"service_name"\`` to `Config` in `internal/config/config.go`
- [x] Add `Environment string \`yaml:"environment"\`` to `Config` in `internal/config/config.go`
- [x] Add `Version string \`yaml:"version"\`` to `Config` in `internal/config/config.go`
- [x] Apply `ServiceName` default in `config.Load()`: `if cfg.ServiceName == "" { cfg.ServiceName = "terraform-parse-service" }`
- [x] Add `environment: "development"` and `version: "dev"` to `configs/config.yaml`
- [x] Add `environment: "production"` and `version: "dev"` to `deploy/config.yaml`

### Phase 2 — Tracing resource

- [x] Change `tracing.New` signature from `(ctx, serviceName string, cfg TracingConfig)` to `(ctx, cfg config.Config)` in `internal/tracing/tracing.go`
- [x] Replace `semconv.ServiceNameKey.String(serviceName)` with `cfg.ServiceName`, and add `semconv.ServiceVersionKey.String(cfg.Version)` and `attribute.String("service.environment", cfg.Environment)` to `resource.WithAttributes`
- [x] Update call site in `cmd/server/main.go`: pass `cfg` instead of `cfg.Logger.Metadata["service"], cfg.Tracing`

### Phase 3 — Handler: remove body, add slugs and business attributes

- [x] Remove `span.SetAttributes(attribute.String("http.request.body", string(body)))` from `internal/handler/aws/s3/bucket.go`
- [x] Add `exception.slug = "err-handler-body-read"` and `error = true` on body read failure
- [x] Add `exception.slug = "err-handler-json-decode"` and `error = true` on JSON decode failure
- [x] Add `exception.slug = "err-handler-validation"` and `error = true` on validation failure
- [x] Add `exception.slug = "err-handler-generate"` and `error = true` on generation failure
- [x] After successful validation, set `aws.s3.region`, `aws.s3.acl`, `aws.s3.bucket_name`, `http.route` on root span
- [x] Replace `attribute.String("url.path", path)` with `attribute.String("http.route", "POST /api/aws/v1/s3/buckets")` in the span start attributes
- [x] Replace `zap.String("url.path", path)` with `zap.String("http.route", "POST /api/aws/v1/s3/buckets")` in `base` log fields

### Phase 4 — Handler: timing breakdown attributes

- [x] Capture `readStart := time.Now()` before `io.ReadAll`; set `read_body.duration_ms` on span after the call
- [x] Capture `decodeStart := time.Now()` before `json.Unmarshal`; set `json_decode.duration_ms` on span after the call
- [x] Capture `validateStart := time.Now()` before `p.Validate()`; set `validate.duration_ms` on span after the call

### Phase 5 — Service: exception slugs

- [x] Change `recordErr` closure signature from `func(error)` to `func(slug string, err error) (string, error)` in `internal/service/terraform.go`
- [x] Add `span.SetAttributes(attribute.String("exception.slug", slug), attribute.Bool("error", true))` inside `recordErr`
- [x] Update each `recordErr` call site with its slug: `"err-service-no-template"`, `"err-service-no-writer"`, `"err-service-render-template"`, `"err-service-storage-write"`

### Phase 6 — Storage: exception slugs and `error = true`

- [x] Add `exception.slug = "err-storage-mkdir"` and `error = true` on mkdir failure in `internal/storage/filesystem.go`
- [x] Add `exception.slug = "err-storage-write-file"` and `error = true` on file write failure in `internal/storage/filesystem.go`

### Phase 7 — Typecheck and verify

- [x] Run `go build ./...` — no compile errors
- [x] Run `go vet ./...` — no vet issues
- [x] Run `go test ./...` — all tests pass
- [x] Confirm `tracing.New` call in `main.go` compiles with new signature
- [x] Confirm `recordErr` call sites in `terraform.go` compile with slug argument
