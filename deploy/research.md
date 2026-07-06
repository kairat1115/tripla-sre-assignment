# Research: terraform-parse-service + Helm Charts

## 1. terraform_parse_service (Go service)

### What it does

HTTP service that generates Terraform HCL files from Go templates. Takes a JSON request describing infrastructure properties, renders a `.tmpl` file with those properties, writes the output as `main.tf` to a configured storage directory, and returns the output path.

Single deployed endpoint: `POST /api/aws/v1/s3/buckets`.

### Startup sequence

```
load config → init zap logger → init OTel tracing → init Prometheus metrics
  → LoadTemplates per provider → register handlers → listen :8080
```

Config loaded from `CONFIG_PATH` env var (default `configs/config.yaml`). Supports `${VAR}` env interpolation via `uberconfig.Expand`.

### Package structure

```
cmd/server/main.go               — entrypoint
internal/config/config.go        — Config structs + Load()
internal/logger/logger.go        — Zap init
internal/tracing/tracing.go      — OTel TracerProvider init
internal/metrics/metrics.go      — Prometheus metrics + /metrics server
internal/handler/handler.go      — Terraform interface, Respond/WriteError, Middleware
internal/handler/aws/s3/bucket.go — S3 bucket request/validation/handler
internal/service/terraform.go    — TerraformService, Generator interface, LoadTemplates
internal/storage/storage.go      — Writer interface
internal/storage/filesystem.go   — FSWriter implementation
```

### Core abstractions

**Generator interface** (implemented by each handler):
```go
type Generator interface {
    Provider() string
    TemplateName() string
    StoragePath() string
    TemplateData() any
    Context() context.Context
}
```

**TerraformService.Generate(generator Generator)**:
1. Look up template by `generator.TemplateName()`
2. Render with `generator.TemplateData()`
3. Write via `Writer.Write(ctx, generator.StoragePath(), rendered)`
4. Record metrics + OTel span
5. Return output path

**LoadTemplates(dir string)**: walks `dir`, parses every `.tmpl` file with `text/template` + Sprig functions, stores by relative path (e.g. `aws/s3/bucket.tf.tmpl`).

**FSWriter.Write(ctx, name, content)**:
- Creates `baseDir/name/` directory tree
- Writes `content` to `baseDir/name/main.tf`
- Returns full path

### S3 bucket handler

`bucketProperties` struct: `region`, `acl`, `bucket-name`. All validated (non-empty). Handler flow:
1. Unmarshal `bucketRequest` from JSON body
2. Validate properties
3. Create OTel span with request attributes
4. Call `TerraformService.Generate(bucketGenerator)`
5. Respond with `{"path": "<output_path>"}` or error JSON

### Templates

Two `.tmpl` files exist, using Go `text/template` + Sprig:

**`aws/s3/bucket.tf.tmpl`** — S3 bucket with optional versioning:
- Variables: `name`, `bucket_name`, `environment`, `versioning`

**`aws/s3/rds.tf.tmpl`** — RDS instance:
- Variables: `name`, `identifier`, `engine`, `engine_version`, `instance_class`, `allocated_storage`, `db_name`, `username`, `password`, `environment`
- No HTTP handler exists for RDS. Template is present but dead.

### Observability

**Prometheus metrics** (served on `:9091/metrics`):
| Metric | Type | Purpose |
|--------|------|---------|
| `terraform_generation_total` | Counter | Per provider+template+status |
| `terraform_generation_duration_seconds` | Histogram | Generation latency |
| `http_requests_total` | Counter | Per method+path+status |
| `http_request_duration_seconds` | Histogram | HTTP handler latency |
| `http_requests_in_flight` | Gauge | Concurrent requests |

**OTel tracing**:
- Exporter: `stdout` (dev) or `otlp_grpc` (prod, to Alloy on `:4317`)
- Sampling: `AlwaysSample` when `sample_rate >= 1.0`, else `TraceIDRatioBased`
- Spans recorded in handler (request validation) and service (generation + storage write)

**Zap logging**:
- Structured JSON in production
- Level configurable (`debug`/`info`/`warn`/`error`)
- Fields: `service`, `environment`, `version` injected globally

### Observability stack (docker-compose)

```
server:9091 → Alloy (scrape) → Prometheus (store)
server (OTLP gRPC) → Alloy:4317 → Tempo (store)
server (stdout logs, Docker) → Alloy (Docker discovery) → Loki (store)
All → Grafana (UI)
```

Alloy config does Docker log discovery for the `server` container, extracts `trace_id` and `level` labels from JSON log lines.

Grafana provisioned with Tempo (default), Loki (TracesToLogs derivation from `trace_id`), Prometheus.

### Config schema

```yaml
listen_addr: ":8080"
service_name: "terraform-parse-service"
environment: "development"
version: "dev"
logger:
  level: "info"
tracing:
  exporter: "stdout"       # stdout | otlp_grpc
  endpoint: "alloy:4317"
  sample_rate: 1.0
metrics:
  addr: ":9091"
providers:
  aws:
    templates_dir: "/templates/aws"
    storage_dir: "/output/aws"
```

### Build

Multi-stage Dockerfile:
- `golang:1.25-alpine` builder: `go build ./cmd/server`
- `scratch` runtime: copies `/server` binary, `/configs`, `/templates`
- `CONFIG_PATH=/configs/config.yaml`
- Exposes 8080

Scratch base = minimal attack surface. Zero shell/tools available in running container.

---

## 2. Helm Charts

### Chart hierarchy

```
helm/
  app/                          — generic subchart (Deployment + Service + HPA + ConfigMap)
  gateway/                      — generic subchart (Istio Gateway + VirtualService + DestinationRule)
  terraform-parse-service/      — umbrella chart (composes app + gateway + template ConfigMaps)
  frontend/                     — example consumer of app/ (nginx)
  backend/                      — example consumer of app/ (hashicorp/http-echo)
```

`terraform-parse-service/` has `file://` dependencies on `app/` and `gateway/`. `frontend/` and `backend/` are standalone reference consumers showing how to layer environment overlays.

---

### app/ chart

**Resources rendered**: Deployment, Service, HPA (conditional), ConfigMap (per entry in `configMaps[]`).

**Key features:**

**HPA-aware replicas**: `replicas` field omitted from Deployment spec when `hpa.enabled: true`. Prevents Helm overwriting HPA's current count on every upgrade.

**Probe type switch**: `probes.type` accepts `httpGet` or `tcpSocket`. Template switches on this value — correct choice for services without a health endpoint.

**ConfigMap checksum annotation**: Deployment gets `checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}`. Forces rolling restart when any ConfigMap content changes.

**`tpl`-rendered fields**: `extraVolumes`, `env`, `sidecars` are rendered through `tpl` before being decoded. Allows values like `name: {{ .Release.Name }}-output` in the values file.

**Named port**: container port named `http`, service `targetPort: http`. Decouples service routing from port number.

**HPA v2 with optional memory**: memory metric only rendered when `targetMemoryUtilizationPercentage > 0`.

**Default values**:
```yaml
replicaCount: 1
resources:
  requests: { cpu: 100m, memory: 128Mi }
  limits:   { cpu: 250m, memory: 256Mi }
service: { type: ClusterIP, port: 80 }
probes:
  type: httpGet
  readiness: { path: /, port: 80, initialDelaySeconds: 5, periodSeconds: 10 }
  liveness:  { path: /, port: 80, initialDelaySeconds: 15, periodSeconds: 20 }
hpa: { enabled: false, minReplicas: 1, maxReplicas: 5, targetCPUUtilizationPercentage: 50 }
```

---

### gateway/ chart

**Resources rendered**: Gateway (conditional), VirtualService (conditional), DestinationRule (conditional).

**Key features:**

**Double guard on Gateway**: only created when `istio.enabled: true` AND `istio.gateway.create: true`. Prevents orphaned Gateway resources.

**`required` guard on VirtualService**: when `istio.gateway.create: false`, `istio.gateway.name` must be provided. Fails fast at render time with a clear error.

**`tpl`-rendered routes**: `istio.virtualService.routes` is rendered through `tpl` then decoded. Allows route destinations referencing `.Release.Name`.

**`tpl`-rendered host in DestinationRule**: same pattern.

**Default values**: all resources disabled by default — chart is safe to include as dependency without accidental resource creation.

---

### terraform-parse-service/ umbrella chart

**What it deploys**:
- Deployment with main container (terraform-parse-service) + k8s-sidecar container
- Service with app port (8080) + metrics port (9091)
- ConfigMap `app-config` — service config YAML
- One ConfigMap per Terraform template (from `tfTemplates[]` list)
- Istio Gateway + VirtualService + DestinationRule (when enabled)
- HPA (when enabled)

**Volume topology**:

| Volume | Type | Source | Mount |
|--------|------|--------|-------|
| `app-config` | ConfigMap | `app-config` CM | `/config` (main) |
| `templates` | emptyDir | k8s-sidecar writes | `/templates` (main + sidecar) |
| `output` | emptyDir | main writes | `/output` (main) |

**Template distribution pattern** (`configmap-templates.yaml`):
- Iterates `tfTemplates[]` list (relative paths like `aws/s3/bucket.tf.tmpl`)
- Reads file content via `$.Files.Get` from `files/templates/<path>`
- Creates one ConfigMap per template, labeled `terraform-parse-service/template: "true"`
- CM name slug: path with `/` and `.` replaced by `-`, suffixed with `-tmpl`

**k8s-sidecar**:
- Image: `kiwigrid/k8s-sidecar`
- Watches CMs with label `terraform-parse-service/template: "true"` (cluster-wide or namespace-scoped)
- Copies CM data into `/templates` emptyDir
- Enables adding/updating templates by applying new CMs — sidecar reacts without pod restart

**Istio routing** (values.yaml):
- VirtualService routes traffic based on header `x-env: dev` or `x-env: staging`
- Weighted routing defined in routes array
- Gateway created in chart (when `istio.gateway.create: true`)

**Prometheus scrape**: pod annotated with `prometheus.io/scrape`, `prometheus.io/path: /metrics`, `prometheus.io/port: "9091"`. Separate `extraPort` exposes metrics on Service.

**Probes**: `tcpSocket` on port 8080. Correct — service has no `/health` HTTP endpoint.

**Environment overlays**:

| Setting | dev | staging | prod |
|---------|-----|---------|------|
| APP_ENV | dev | staging | production |
| Log level | debug | info | info |
| Tracing | stdout | otlp_grpc → Tempo | otlp_grpc → Tempo |
| Replicas | 1 | default (1) | 2 |
| HPA | disabled | enabled 1-2 | enabled 2-6 |
| CPU request/limit | 100m/500m | 100m/500m | 200m/1000m |
| Memory request/limit | 128Mi/256Mi | 128Mi/256Mi | 256Mi/512Mi |
| Templates | bucket only | bucket + rds | bucket only |
| DestinationRule | no | no | yes |

---

## 3. Critical findings and gaps

### G1: Template hot-reload is broken at the service level

`LoadTemplates()` runs once at startup in `main.go`. The k8s-sidecar pattern is designed for hot-reload — CM changes propagate to `/templates` without pod restart. But the service never re-reads the directory after init. New or updated templates require a pod restart to take effect. The sidecar infra is correct; the service doesn't leverage it.

### G2: output emptyDir — generated files not persistent

`/output` is an emptyDir. Any generated `main.tf` files are lost on pod restart or eviction. For a generation service this may be intentional (stateless, caller fetches and stores), but there's no documentation of this constraint and no object storage backend implemented.

### G3: RDS template with no handler

`rds.tf.tmpl` exists and is deployed via `values-staging.yaml`, but `internal/handler/aws/s3/bucket.go` is the only handler. No `POST /api/aws/v1/rds/...` endpoint. The template is unreachable through the API.

### G4: k8s-sidecar requires cluster RBAC

`kiwigrid/k8s-sidecar` needs `list`/`watch`/`get` on ConfigMaps, minimally in the deployment namespace. No ServiceAccount, Role, or RoleBinding is defined in the chart. Deployers must create these out-of-band. Chart README mentions RBAC as a prerequisite but doesn't provide manifests.

### G5: No `/health` or `/ready` HTTP endpoint

tcpSocket probe on 8080 only confirms the port is open, not that the service is initialized (templates loaded, config valid). A failing `LoadTemplates()` would still pass the probe if the server started listening before the error. The Middleware in `handler.go` wraps handlers but no dedicated health route is registered.

### G6: Prometheus config doesn't scrape the service

`deploy/prometheus/prometheus.yaml` only has a self-scrape job (`localhost:9090`). The terraform-parse-service metrics on `:9091` are scraped via Alloy's explicit scrape job, not Prometheus directly. This is correct for the docker-compose stack, but the prometheus.yaml file is misleading if used standalone.

### G7: `tpl` rendering on user-controlled arrays

`extraVolumes`, `env`, `sidecars` are passed through `tpl`. If values contain unintended `{{` (e.g. from a template string in an env var value), Helm fails at render time with an opaque error. No escaping mechanism is documented.

### G8: Single provider, single resource type

The Generator interface and TerraformService are designed to be multi-provider (providers map in config, Provider() method on Generator), but only AWS S3 is implemented. The architecture is correct for extension; the current coverage is narrow.

### G9: scratch base image — zero debuggability

Correct security choice. Side effect: no shell, no curl, no netcat in the container. Debugging a live pod requires `kubectl debug` with an ephemeral container or exec into the sidecar.

### G10: Alloy Docker log discovery is docker-compose-specific

`deploy/alloy/config.alloy` uses Docker socket discovery (`unix:///var/run/docker.sock`) to find the server container. This works in docker-compose but is not applicable to the Kubernetes deployment. In k8s, log forwarding requires a DaemonSet-based approach (Alloy DaemonSet, Fluentbit, etc.) not represented in the Helm charts.
