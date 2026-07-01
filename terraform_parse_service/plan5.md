# Plan 5: Business Metrics

## Goal

Instrument the service with application-level metrics that answer operational questions:
- How many generation tasks completed, by provider and resource type, and with what outcome?
- How long did generation take?
- How often did HTTP requests fail and why?
- How busy is the server?

`github.com/prometheus/client_golang` is used directly — no OTel metrics SDK. The server exposes `/metrics` (Prometheus text format) on a dedicated port in both environments. Alloy scrapes that endpoint and remote-writes to Prometheus.

---

## Metric Definitions

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `terraform_generation_total` | Counter | `provider`, `resource`, `status` | Completed generation tasks. `status` = `success` \| `error` |
| `terraform_generation_duration_seconds` | Histogram | `provider`, `resource` | End-to-end generation latency (template render + storage write). Buckets: 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s |
| `http_requests_total` | Counter | `method`, `path`, `status_code` | All HTTP responses. Lets you compute error rate and request volume per endpoint. |
| `http_request_duration_seconds` | Histogram | `method`, `path` | HTTP handler latency. Same buckets as generation. |
| `http_requests_in_flight` | Gauge | `method`, `path` | Currently executing requests. Detects concurrency saturation. |

**Rationale for additions beyond the baseline request:**

- `http_requests_in_flight` — earliest signal of concurrency saturation; correlates directly with latency spikes.
- `http_request_duration_seconds` — HTTP latency is distinct from generation latency (JSON decode, validation, response encoding all add overhead). Separating them isolates where time is spent.
- `status` label on `terraform_generation_total` — a bare count is not actionable. Error rate = `rate(total{status="error"}) / rate(total)`.

---

## Architecture

Both environments run identical server code — expose `/metrics` on `:9091`, nothing else.

```
curl :9091/metrics              (local dev)
Alloy prometheus.scrape         (Docker Compose)
        ↑
  promhttp.Handler()
        ↑
  prometheus.Registry (in-process)
        ↑
  instrument calls in handler / service
```

In Docker Compose, Alloy scrapes `server:9091/metrics` on a 15s interval and forwards to the existing `prometheus.remote_write.default` → Prometheus pipeline.

---

## Implementation Phases

### Phase 1 — Metrics package ✓

**[x] Create `internal/metrics/metrics.go`**

A single `Metrics` struct registered once in `main.go` and injected where needed. Also owns the `/metrics` HTTP server via `Serve` — `main.go` has no metrics-related goroutine logic.

```go
package metrics

import (
    "net/http"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
    "github.com/prometheus/client_golang/prometheus/promhttp"
    "go.uber.org/zap"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}

type Metrics struct {
    reg                prometheus.Gatherer
    GenerationTotal    *prometheus.CounterVec
    GenerationDuration *prometheus.HistogramVec
    HTTPRequestsTotal  *prometheus.CounterVec
    HTTPDuration       *prometheus.HistogramVec
    HTTPInFlight       *prometheus.GaugeVec
}

func New(reg *prometheus.Registry) *Metrics {
    factory := promauto.With(reg)
    return &Metrics{
        reg: reg,
        GenerationTotal: factory.NewCounterVec(prometheus.CounterOpts{
            Name: "terraform_generation_total",
            Help: "Total number of terraform generation tasks.",
        }, []string{"provider", "resource", "status"}),

        GenerationDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
            Name:    "terraform_generation_duration_seconds",
            Help:    "Duration of terraform generation tasks.",
            Buckets: durationBuckets,
        }, []string{"provider", "resource"}),

        HTTPRequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
            Name: "http_requests_total",
            Help: "Total number of HTTP requests.",
        }, []string{"method", "path", "status_code"}),

        HTTPDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
            Name:    "http_request_duration_seconds",
            Help:    "HTTP request duration.",
            Buckets: durationBuckets,
        }, []string{"method", "path"}),

        HTTPInFlight: factory.NewGaugeVec(prometheus.GaugeOpts{
            Name: "http_requests_in_flight",
            Help: "Number of currently in-flight HTTP requests.",
        }, []string{"method", "path"}),
    }
}

// Serve starts the /metrics HTTP server on addr. Runs in a background goroutine;
// logs fatal on bind failure. addr defaults to ":9091" if empty.
func (m *Metrics) Serve(addr string, log *zap.Logger) {
    if addr == "" {
        addr = ":9091"
    }
    mux := http.NewServeMux()
    mux.Handle("/metrics", promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}))
    go func() {
        log.Info("metrics server starting", zap.String("addr", addr))
        if err := http.ListenAndServe(addr, mux); err != nil {
            log.Error("metrics server exited", zap.String("error", err.Error()))
        }
    }()
}
```

`New` accepts `*prometheus.Registry` (not the `Registerer` interface) so `Serve` can pass it as a `Gatherer` to `promhttp.HandlerFor` without a type assertion. `promauto.With` accepts `Registerer`, so both uses are satisfied.

---

### Phase 2 — Config ✓

**[x] Add `MetricsConfig` to `internal/config/config.go`**

```go
type MetricsConfig struct {
    Addr string `yaml:"addr"`
}

type Config struct {
    ListenAddr string                    `yaml:"listen_addr"`
    Logger     LoggerConfig              `yaml:"logger"`
    Tracing    TracingConfig             `yaml:"tracing"`
    Metrics    MetricsConfig             `yaml:"metrics"`
    Providers  map[string]ProviderConfig `yaml:"providers"`
}
```

**[x] Update `configs/config.yaml`** (local dev):

```yaml
metrics:
  addr: ":9091"
```

**[x] Update `deploy/config.yaml`** (Docker Compose):

```yaml
metrics:
  addr: ":9091"
```

Both configs are identical — the server always exposes `/metrics` on the configured address. No environment-specific branching in application code.

---

### Phase 3 — Wire into `main.go` ✓

**[x] Update `cmd/server/main.go`**

```go
import (
    // existing imports ...
    "github.com/prometheus/client_golang/prometheus"
    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
)

// After tracing init:
reg := prometheus.NewRegistry()
m := metrics.New(reg)
m.Serve(cfg.Metrics.Addr, zap.L())
```

Pass `m` to service and handler:

```go
tfSvc := service.NewTerraformService(writers, templates, m)
// ...
mux.Handle("POST /api/aws/v1/s3/buckets", s3handler.NewBucketHandler(tfSvc, l, m))
```

`prometheus.NewRegistry()` instead of `DefaultRegisterer` avoids pulling in Go runtime metrics by default and keeps the output clean.

---

### Phase 4 — Instrument `BucketHandler` ✓

**[x] Update `internal/handler/aws/s3/bucket.go`**

Add `m *metrics.Metrics` field and update constructor:

```go
type BucketHandler struct {
    svc    handler.Terraform
    logger *zap.Logger
    m      *metrics.Metrics
}

func NewBucketHandler(svc handler.Terraform, logger *zap.Logger, m *metrics.Metrics) *BucketHandler {
    return &BucketHandler{svc: svc, logger: logger, m: m}
}
```

In `ServeHTTP`:

```go
func (h *BucketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    start := time.Now()
    path := r.URL.Path

    h.m.HTTPInFlight.WithLabelValues(r.Method, path).Inc()
    defer func() {
        h.m.HTTPInFlight.WithLabelValues(r.Method, path).Dec()
        h.m.HTTPDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
    }()

    // ... existing span, log, and logic ...

    // At each return point, record status — example for a 400:
    h.m.HTTPRequestsTotal.WithLabelValues(r.Method, path, "400").Inc()

    // On success (201):
    h.m.HTTPRequestsTotal.WithLabelValues(r.Method, path, "201").Inc()
}
```

`start` is already defined in the existing handler — the deferred `HTTPDuration.Observe` reuses it. No duplicate variable.

---

### Phase 5 — Instrument `TerraformService.Generate` ✓

Generation metrics belong in the service layer — it is the authoritative boundary for the operation's outcome and duration.

**[x] Update `internal/service/terraform.go`**

Add `m *metrics.Metrics` field and update constructor:

```go
type TerraformService struct {
    writers   map[string]storage.Writer
    templates map[string]*template.Template
    m         *metrics.Metrics
}

func NewTerraformService(writers map[string]storage.Writer, templates map[string]*template.Template, m *metrics.Metrics) *TerraformService {
    return &TerraformService{writers: writers, templates: templates, m: m}
}
```

In `Generate`:

```go
func (s *TerraformService) Generate(g Generator) (string, error) {
    start := time.Now()
    resource := resourceLabel(g.TemplateName())

    // ... existing span and logic ...

    // on any error return:
    s.m.GenerationTotal.WithLabelValues(g.Provider(), resource, "error").Inc()
    s.m.GenerationDuration.WithLabelValues(g.Provider(), resource).Observe(time.Since(start).Seconds())
    return "", err

    // on success:
    s.m.GenerationTotal.WithLabelValues(g.Provider(), resource, "success").Inc()
    s.m.GenerationDuration.WithLabelValues(g.Provider(), resource).Observe(time.Since(start).Seconds())
    return path, nil
}

// "s3/bucket.tf.tmpl" → "s3_bucket"
func resourceLabel(templateName string) string {
    name := strings.TrimSuffix(templateName, ".tf.tmpl")
    return strings.ReplaceAll(name, "/", "_")
}
```

---

### Phase 6 — Alloy scrape config (Docker Compose) ✓

**[x] Update `deploy/alloy/config.alloy`**

Add a `prometheus.scrape` component that targets the server's `/metrics` endpoint and forwards to the existing `prometheus.remote_write.default`:

```hcl
prometheus.scrape "server" {
  targets = [{
    __address__ = "server:9091",
  }]
  forward_to      = [prometheus.remote_write.default.receiver]
  scrape_interval = "15s"
}
```

No other Alloy changes needed. `prometheus.remote_write.default` already exists and forwards to Prometheus.

Port 9091 does not need to be published in `deploy/docker-compose.yaml` — Alloy and the server share the `obs` network. Publish it only for local debugging:

```yaml
server:
  ports:
    - "8080:8080"
    - "9091:9091"   # optional: expose /metrics to host
```

---

### Phase 7 — New dependency ✓

```
go get github.com/prometheus/client_golang@v1.20.0
```

No additional dependencies. No push-specific packages needed.

---

### Phase 8 — Grafana dashboard (Docker Compose only) ✓

Provision a dashboard under `deploy/grafana/provisioning/dashboards/`.

**[x] Add `deploy/grafana/provisioning/dashboards/dashboards.yaml`**:

```yaml
apiVersion: 1
providers:
  - name: default
    folder: Terraform Parse Service
    type: file
    options:
      path: /etc/grafana/provisioning/dashboards
```

**[x] Add `deploy/grafana/provisioning/dashboards/service.json`** with panels:

| Panel | Query |
|-------|-------|
| Generation rate (success) | `sum by (provider, resource) (rate(terraform_generation_total{status="success"}[5m]))` |
| Generation error rate | `sum by (provider, resource) (rate(terraform_generation_total{status="error"}[5m]))` |
| Generation p99 latency | `histogram_quantile(0.99, sum by (le, provider, resource) (rate(terraform_generation_duration_seconds_bucket[5m])))` |
| HTTP request rate | `sum by (path, status_code) (rate(http_requests_total[5m]))` |
| HTTP p99 latency | `histogram_quantile(0.99, sum by (le, path) (rate(http_request_duration_seconds_bucket[5m])))` |
| In-flight requests | `sum by (method, path) (http_requests_in_flight)` |

Mount the dashboards directory in `docker-compose.yaml` — it is already covered by `./grafana/provisioning:/etc/grafana/provisioning:ro`.

---

## File Change Summary

| File | Change |
|------|--------|
| `internal/metrics/metrics.go` | New — metric instruments using `prometheus/client_golang` |
| `internal/config/config.go` | Add `MetricsConfig` with `Addr` field |
| `configs/config.yaml` | Add `metrics.addr: ":9091"` |
| `deploy/config.yaml` | Add `metrics.addr: ":9091"` |
| `cmd/server/main.go` | Create registry, call `m.Serve`, pass `m` to service and handler |
| `internal/service/terraform.go` | Accept `*metrics.Metrics`, record generation counter and histogram |
| `internal/handler/aws/s3/bucket.go` | Accept `*metrics.Metrics`, record HTTP counter, histogram, in-flight gauge |
| `deploy/alloy/config.alloy` | Add `prometheus.scrape "server"` component |
| `deploy/docker-compose.yaml` | Optionally expose port 9091 on server |
| `go.mod` / `go.sum` | Add `github.com/prometheus/client_golang` |
| `deploy/grafana/provisioning/dashboards/dashboards.yaml` | New — dashboard provisioning config |
| `deploy/grafana/provisioning/dashboards/service.json` | New — Grafana dashboard JSON |

---

## Failure modes and constraints

- **Cardinality**: labels are `provider`, `resource`, `status`, `method`, `path`, `status_code` — all statically bounded. Never use request-specific values (bucket names, trace IDs) as label values.
- **`prometheus.NewRegistry()`**: does not include Go runtime or process metrics. If you want those, also register `prometheus.NewGoCollector()` and `prometheus.NewProcessCollector(...)` on the same registry.
- **Port 9091 and `read_only: true`**: the metrics server only binds a port; it does not write to the filesystem. `read_only: true` remains valid.
- **Scrape vs push reliability**: with pull, a missed scrape (Alloy restart) results in a gap, not lost data — the registry retains current values and the next scrape recovers. This is more resilient than push for this use case.
- **`start` reuse in handler**: `BucketHandler.ServeHTTP` already defines `start := time.Now()` at line 83. The deferred `HTTPDuration.Observe` uses that existing variable — no second `time.Now()` needed.
