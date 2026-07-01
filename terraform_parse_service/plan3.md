# Plan: OpenTelemetry Tracing

## Objective

Add distributed tracing via OpenTelemetry. Every inbound request produces a trace with child spans covering each logical operation: HTTP handling, template rendering, and filesystem write. Span attributes carry the same structured fields already present in logs, making traces the primary instrument for deep debugging — request body shape, which template was used, where the file landed, how long each step took.

---

## Packages

```bash
go get go.opentelemetry.io/otel
go get go.opentelemetry.io/otel/sdk
go get go.opentelemetry.io/otel/exporters/stdout/stdouttrace
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
go get go.opentelemetry.io/otel/trace
go get go.opentelemetry.io/otel/codes
```

**Exporter:** controlled by `tracing.exporter` in `configs/config.yaml`. Supported values:

| Value | Behaviour |
|---|---|
| `stdout` | Writes pretty-printed JSON spans to stdout — useful for local development |
| `otlp_grpc` | Sends spans to an OTLP-compatible collector (Jaeger, Tempo, etc.) over gRPC. Endpoint from `OTEL_EXPORTER_OTLP_ENDPOINT`; default `localhost:4317` |

`tracing.New` switches on `cfg.Exporter` and returns an error for unknown values — fail-fast at startup rather than silently dropping spans. For `otlp_grpc`, `cfg.Endpoint` sets the collector address (defaults to `localhost:4317` when empty). `cfg.SampleRatio` sets the sampling fraction: `1.0` uses `AlwaysSample`, any lower value uses `TraceIDRatioBased` — deterministic head sampling keyed on the trace ID.

---

## Trace structure

For a successful `POST /api/aws/v1/s3/buckets` request, the trace looks like:

```
[http.request] POST /api/aws/v1/s3/buckets                ← root span, started in BucketHandler
  [service.generate] s3/bucket.tf.tmpl                    ← child span, started in TerraformService.Generate
    [storage.write] s3/tripla-bucket                      ← child span, started in FSWriter.Write
```

On decode/validation failure the trace has only the root span (no service or storage work is done).

---

## Component Design

### Configuration

#### `configs/config.yaml` — add `tracing` block

```yaml
tracing:
  exporter: "otlp_grpc"    # stdout | otlp_grpc
  endpoint: "localhost:4317" # for otlp_grpc; ignored for stdout
  sample_ratio: 1.0          # 0.0–1.0; 1.0 = sample every trace
```

#### `internal/config/config.go` — add `TracingConfig`

```go
type TracingConfig struct {
    Exporter    string  `yaml:"exporter"`     // stdout | otlp_grpc
    Endpoint    string  `yaml:"endpoint"`     // otlp_grpc target; default localhost:4317
    SampleRatio float64 `yaml:"sample_ratio"` // 0.0–1.0; default 1.0
}

type Config struct {
    ListenAddr string                    `yaml:"listen_addr"`
    Logger     LoggerConfig              `yaml:"logger"`
    Tracing    TracingConfig             `yaml:"tracing"`
    Providers  map[string]ProviderConfig `yaml:"providers"`
}
```

---

### `internal/tracing/tracing.go` (new)

Encapsulates tracer provider construction, mirroring `internal/logger`. Accepts `config.TracingConfig` and builds the appropriate exporter via a switch. Returns a `*sdktrace.TracerProvider` and a shutdown function. `main.go` defers the shutdown.

```go
package tracing

import (
    "context"
    "fmt"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
    "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
)

func New(ctx context.Context, serviceName string, cfg config.TracingConfig) (*sdktrace.TracerProvider, func(context.Context) error, error) {
    var exp sdktrace.SpanExporter
    var err error
    switch cfg.Exporter {
    case "stdout":
        exp, err = stdouttrace.New()
    case "otlp_grpc":
        endpoint := cfg.Endpoint
        if endpoint == "" {
            endpoint = "localhost:4317"
        }
        exp, err = otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint))
    default:
        return nil, nil, fmt.Errorf("unknown trace exporter %q: supported values are stdout, otlp_grpc", cfg.Exporter)
    }
    if err != nil {
        return nil, nil, fmt.Errorf("build trace exporter: %w", err)
    }
    res, err := resource.New(ctx,
        resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)),
    )
    if err != nil {
        return nil, nil, fmt.Errorf("build trace resource: %w", err)
    }
    sampler := sdktrace.AlwaysSample()
    if cfg.SampleRatio < 1.0 {
        sampler = sdktrace.TraceIDRatioBased(cfg.SampleRatio)
    }
    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exp),
        sdktrace.WithResource(res),
        sdktrace.WithSampler(sampler),
    )
    otel.SetTracerProvider(tp)
    return tp, tp.Shutdown, nil
}
```

`otel.SetTracerProvider(tp)` installs it as the global — components retrieve their tracer via `otel.Tracer("component-name")` rather than accepting a `*TracerProvider` argument. This keeps component constructors simple.

---

### `cmd/server/main.go` — init tracing after logger

```go
tp, shutdown, err := tracing.New(context.Background(), cfg.Logger.Metadata["service"], cfg.Tracing)
if err != nil {
    zap.L().Error("tracer init failed", zap.String("error", err.Error()))
    os.Exit(1)
}
defer func() { _ = shutdown(context.Background()) }()
_ = tp
```

`cfg.Logger.Metadata["service"]` reuses the service name from `configs/config.yaml`. `cfg.Tracing.Exporter` selects the output; `cfg.Tracing.Endpoint` sets the collector address for `otlp_grpc`; `cfg.Tracing.SampleRatio` controls sampling — all sourced from `configs/config.yaml`.

---

### `internal/handler/aws/s3/bucket.go` — root span

The handler starts the root span immediately after reading the base fields. The span covers the full handler lifetime — it ends just before `ServeHTTP` returns on every code path.

**Attribute naming conventions:** HTTP attributes follow [OTel semantic conventions v1.26](https://opentelemetry.io/docs/specs/semconv/) (`http.request.method`, `url.path`, `network.peer.address`, `http.response.status_code`, `http.server.request.duration` in seconds). Exception detail uses `exception.message`. Custom AWS/S3 business attributes use the `service.aws.s3.*` namespace (`service.aws.s3.region`, `service.aws.s3.acl`, `service.aws.s3.bucket_name`). The same key names are used in zap log fields — queries against logs and traces use identical field names.

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/codes"
    "go.opentelemetry.io/otel/trace"
)

const tracerName = "handler.aws.s3"

func (h *BucketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    start := time.Now()
    ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
        trace.WithSpanKind(trace.SpanKindServer),
        trace.WithAttributes(
            attribute.String("http.request.method", r.Method),
            attribute.String("url.path", r.URL.Path),
            attribute.String("network.peer.address", r.RemoteAddr),
        ),
    )
    defer span.End()

    base := []zap.Field{
        zap.String("http.request.method", r.Method),
        zap.String("url.path", r.URL.Path),
        zap.String("network.peer.address", r.RemoteAddr),
    }

    body, err := io.ReadAll(r.Body)
    if err != nil {
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        span.SetAttributes(attribute.Int("http.response.status_code", http.StatusBadRequest))
        h.logger.Info("request body read failed", append(base,
            zap.Int("http.response.status_code", http.StatusBadRequest),
            zap.String("exception.message", err.Error()),
            zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
        )...)
        handler.Respond(w, handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
        return
    }

    var req bucketRequest
    if err := json.Unmarshal(body, &req); err != nil {
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        span.SetAttributes(attribute.Int("http.response.status_code", http.StatusBadRequest))
        h.logger.Info("request body decode failed", append(base,
            zap.Int("http.response.status_code", http.StatusBadRequest),
            zap.String("exception.message", err.Error()),
            zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
        )...)
        handler.Respond(w, handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
        return
    }

    p := req.Payload.Properties
    if err := p.Validate(); err != nil {
        msg := err.Error()
        span.SetStatus(codes.Error, msg)
        span.SetAttributes(
            attribute.Int("http.response.status_code", http.StatusUnprocessableEntity),
            attribute.String("service.aws.s3.region", p.Region),
            attribute.String("service.aws.s3.acl", p.ACL),
            attribute.String("service.aws.s3.bucket_name", p.BucketName),
        )
        h.logger.Info(msg, append(base,
            zap.Int("http.response.status_code", http.StatusUnprocessableEntity),
            zap.String("service.aws.s3.region", p.Region),
            zap.String("service.aws.s3.acl", p.ACL),
            zap.String("service.aws.s3.bucket_name", p.BucketName),
            zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
        )...)
        handler.Respond(w, handler.Result{Code: http.StatusUnprocessableEntity, Msg: msg, Err: err})
        return
    }

    gen := &bucketGenerator{props: p, ctx: ctx}
    outputPath, err := h.svc.Generate(gen)
    if err != nil {
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        span.SetAttributes(
            attribute.Int("http.response.status_code", http.StatusInternalServerError),
            attribute.String("service.aws.s3.region", p.Region),
            attribute.String("service.aws.s3.acl", p.ACL),
            attribute.String("service.aws.s3.bucket_name", p.BucketName),
        )
        h.logger.Error("generation failed", append(base,
            zap.Int("http.response.status_code", http.StatusInternalServerError),
            zap.String("service.aws.s3.region", p.Region),
            zap.String("service.aws.s3.acl", p.ACL),
            zap.String("service.aws.s3.bucket_name", p.BucketName),
            zap.String("exception.message", err.Error()),
            zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
        )...)
        handler.Respond(w, handler.Result{Code: http.StatusInternalServerError, Msg: "generation failed", Err: err})
        return
    }

    span.SetStatus(codes.Ok, "")
    span.SetAttributes(
        attribute.Int("http.response.status_code", http.StatusCreated),
        attribute.String("service.aws.s3.region", p.Region),
        attribute.String("service.aws.s3.acl", p.ACL),
        attribute.String("service.aws.s3.bucket_name", p.BucketName),
        attribute.String("output.path", outputPath),
    )
    h.logger.Info("terraform config generated", append(base,
        zap.Int("http.response.status_code", http.StatusCreated),
        zap.String("service.aws.s3.region", p.Region),
        zap.String("service.aws.s3.acl", p.ACL),
        zap.String("service.aws.s3.bucket_name", p.BucketName),
        zap.String("output.path", outputPath),
        zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
    )...)
    handler.Respond(w, handler.Result{Code: http.StatusCreated, Data: bucketResponse{OutputPath: outputPath}})
}
```

**Context threading:** `bucketGenerator` gains a `ctx context.Context` field so the service layer can create child spans parented to the handler span.

```go
type bucketGenerator struct {
    props bucketProperties
    ctx   context.Context
}
```

`Generator` interface gains a `Context() context.Context` method so `TerraformService.Generate` can retrieve it without knowing about the concrete type:

```go
type Generator interface {
    Provider() string
    TemplateName() string
    StoragePath() string
    TemplateData() any
    Context() context.Context
}

func (g *bucketGenerator) Context() context.Context { return g.ctx }
```

---

### `internal/service/terraform.go` — service.generate span

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/codes"
)

const tracerName = "service.terraform"

func (s *TerraformService) Generate(g Generator) (string, error) {
    ctx, span := otel.Tracer(tracerName).Start(g.Context(), "service.generate",
        trace.WithAttributes(
            attribute.String("template.name", g.TemplateName()),
            attribute.String("provider", g.Provider()),
            attribute.String("storage.path", g.StoragePath()),
        ),
    )
    defer span.End()

    tmpl, ok := s.templates[g.Provider()]
    if !ok {
        err := fmt.Errorf("no templates registered for provider %s", g.Provider())
        span.SetStatus(codes.Error, err.Error())
        return "", err
    }
    writer, ok := s.writers[g.Provider()]
    if !ok {
        err := fmt.Errorf("no writer registered for provider %s", g.Provider())
        span.SetStatus(codes.Error, err.Error())
        return "", err
    }
    var buf bytes.Buffer
    if err := tmpl.ExecuteTemplate(&buf, g.TemplateName(), g.TemplateData()); err != nil {
        err = fmt.Errorf("render template %s: %w", g.TemplateName(), err)
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        return "", err
    }
    path, err := writer.Write(ctx, g.StoragePath(), buf.Bytes())
    if err != nil {
        err = fmt.Errorf("write storage: %w", err)
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        return "", err
    }
    span.SetStatus(codes.Ok, "")
    span.SetAttributes(attribute.String("output.path", path))
    return path, nil
}
```

Note: `writer.Write` gains a `context.Context` as its first argument (see Storage section below). The `ctx` here is the child span context, so the storage span is correctly nested under the service span.

---

### `internal/storage/storage.go` — update Writer interface

```go
package storage

import "context"

type Writer interface {
    Write(ctx context.Context, name string, content []byte) (string, error)
}
```

---

### `internal/storage/filesystem.go` — storage.write span

```go
import (
    "context"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/codes"
)

const tracerName = "storage.filesystem"

func (w *FSWriter) Write(ctx context.Context, name string, content []byte) (string, error) {
    _, span := otel.Tracer(tracerName).Start(ctx, "storage.write",
        trace.WithAttributes(
            attribute.String("storage.name", name),
            attribute.String("storage.base_dir", w.BaseDir),
        ),
    )
    defer span.End()

    dir := filepath.Join(w.BaseDir, name)
    if err := os.MkdirAll(dir, 0o755); err != nil {
        err = fmt.Errorf("mkdir %s: %w", dir, err)
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        return "", err
    }
    path := filepath.Join(dir, "main.tf")
    if err := os.WriteFile(path, content, 0o644); err != nil {
        err = fmt.Errorf("write %s: %w", path, err)
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        return "", err
    }
    span.SetStatus(codes.Ok, "")
    span.SetAttributes(attribute.String("output.path", path))
    return path, nil
}
```

---

## Span attribute reference

Standard HTTP attributes follow [OTel semconv v1.26](https://opentelemetry.io/docs/specs/semconv/). Custom AWS/S3 fields use `service.aws.s3.*`. Log field names are identical to span attribute names.

| Span | Attribute | Convention | Value |
|---|---|---|---|
| `http.request` | `http.request.method` | OTel semconv | `POST` |
| `http.request` | `url.path` | OTel semconv | `/api/aws/v1/s3/buckets` |
| `http.request` | `network.peer.address` | OTel semconv | client address |
| `http.request` | `http.response.status_code` | OTel semconv | final HTTP status |
| `http.request` | `service.aws.s3.region` | custom `service.aws.s3.*` | decoded `aws-region` |
| `http.request` | `service.aws.s3.acl` | custom `service.aws.s3.*` | decoded `acl` |
| `http.request` | `service.aws.s3.bucket_name` | custom `service.aws.s3.*` | decoded `bucket-name` |
| `http.request` | `output.path` | custom | filesystem path (success only) |
| `http.request` | `exception.message` | OTel semconv | error string (error paths only) |
| `http.request` | `http.server.request.duration` | OTel semconv | elapsed seconds (all paths, log only) |
| `service.generate` | `template.name` | custom | `s3/bucket.tf.tmpl` |
| `service.generate` | `provider` | custom | `aws` |
| `service.generate` | `storage.path` | custom | `s3/tripla-bucket` |
| `service.generate` | `output.path` | custom | filesystem path (success only) |
| `storage.write` | `storage.name` | custom | `s3/tripla-bucket` |
| `storage.write` | `storage.base_dir` | custom | `./output/aws` |
| `storage.write` | `output.path` | custom | filesystem path (success only) |

---

## Files changed

| File | Change |
|---|---|
| `go.mod` / `go.sum` | add OTel packages (`stdouttrace` + `otlptracegrpc`) |
| `configs/config.yaml` | add `tracing` block (`exporter`, `endpoint`, `sample_ratio`) |
| `internal/config/config.go` | add `TracingConfig` struct (`Exporter`, `Endpoint`, `SampleRatio`); add `Tracing` field to `Config` |
| `internal/tracing/tracing.go` | new — exporter switch, endpoint config for `otlp_grpc`, sampler from `sample_ratio`, provider construction, shutdown |
| `cmd/server/main.go` | init tracing after logger; pass `cfg.Tracing`; defer shutdown |
| `internal/storage/storage.go` | `Write` signature gains `context.Context` first arg |
| `internal/storage/filesystem.go` | implement new signature; add `storage.write` span |
| `internal/service/terraform.go` | `Generator` gains `Context()`; `Generate` adds `service.generate` span; passes `ctx` to `writer.Write` |
| `internal/handler/aws/s3/bucket.go` | `bucketGenerator` gains `ctx` field and `Context()` method; `ServeHTTP` adds `http.request` span; attribute and log field names follow OTel semconv (`http.request.method`, `url.path`, `network.peer.address`, `http.response.status_code`, `exception.message`, `http.server.request.duration`) |

---

## Tests to update

`Writer.Write` signature change (`ctx context.Context` added as first arg) propagates to all call sites:

- `internal/storage/filesystem.go` — implementation, updated above
- `internal/handler/aws/s3/bucket_test.go` — uses `stubTerraform`, never calls `Write` directly; no change
- `test/helpers_test.go` and `test/aws/s3/helpers_test.go` — use `storage.NewFSWriter` whose `Write` is called via `TerraformService.Generate`; the `context.Context` flows through from `bucketGenerator.ctx`, which is set to `r.Context()` in `ServeHTTP`; no change to test setup

The `Generator` interface gains `Context() context.Context`. `stubTerraform.Generate` accepts a `Generator` but never calls `Context()` — no change to the stub. The concrete `bucketGenerator` implements the new method.

---

## TODO

### Phase 1 — Install dependencies ✅

- [x] `go get go.opentelemetry.io/otel`
- [x] `go get go.opentelemetry.io/otel/sdk`
- [x] `go get go.opentelemetry.io/otel/exporters/stdout/stdouttrace`
- [x] `go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`
- [x] `go get go.opentelemetry.io/otel/trace`
- [x] `go get go.opentelemetry.io/otel/codes`

### Phase 2 — Configuration + `internal/tracing/tracing.go` ✅

- [x] Add `tracing` block to `configs/config.yaml`: `exporter`, `endpoint`, `sample_ratio`
- [x] Add `TracingConfig` struct (`Exporter`, `Endpoint`, `SampleRatio`) to `internal/config/config.go`; add `Tracing TracingConfig` field to `Config`
- [x] Create `internal/tracing/` directory
- [x] Write `tracing.go`: `New(ctx context.Context, serviceName string, cfg config.TracingConfig) (*sdktrace.TracerProvider, func(context.Context) error, error)` — switch on `cfg.Exporter` (`stdout` → `stdouttrace`, `otlp_grpc` → `otlptracegrpc`), resource with `service.name`, batcher, `otel.SetTracerProvider`

### Phase 3 — `internal/storage/storage.go` + `filesystem.go` ✅

- [x] Add `context.Context` as first argument to `Writer.Write`
- [x] Update `FSWriter.Write` signature to match
- [x] Add `storage.write` span in `FSWriter.Write`

### Phase 4 — `internal/service/terraform.go` ✅

- [x] Add `Context() context.Context` to `Generator` interface
- [x] Add `ctx context.Context` field to `bucketGenerator`; implement `Context()` method (done in Phase 5)
- [x] Add `service.generate` span in `TerraformService.Generate`
- [x] Pass `ctx` (child span context) to `writer.Write`

### Phase 5 — `internal/handler/aws/s3/bucket.go` ✅

- [x] Add OTel imports (`otel`, `attribute`, `codes`, `trace`)
- [x] Start `http.request` span at top of `ServeHTTP` using `r.Context()`
- [x] Set span status and attributes on each terminal path using OTel semconv names (`http.request.method`, `url.path`, `network.peer.address`, `http.response.status_code`) and `service.aws.s3.*` namespace for business fields
- [x] Align zap log field names to same keys as span attributes: `exception.message` for error strings, `http.server.request.duration` (seconds) for latency
- [x] Pass `ctx` to `bucketGenerator`

### Phase 6 — `cmd/server/main.go` ✅

- [x] Import `internal/tracing` and `context`
- [x] Call `tracing.New(context.Background(), cfg.Logger.Metadata["service"], cfg.Tracing)` after logger init
- [x] Defer `shutdown(context.Background())`

### Phase 7 — Verification

- [x] `go vet ./...` — no issues
- [x] `go test ./internal/...` — all unit tests pass
- [x] `go test ./test/...` — all integration tests pass
- [ ] Manual smoke test: valid request → collector (e.g. Jaeger UI at `localhost:16686`) shows trace with three nested spans (`http.request` → `service.generate` → `storage.write`)
- [ ] Manual smoke test: malformed JSON → collector shows single `http.request` span with status `Error`
