# Terraform Parse Service

HTTP service that renders Terraform configuration from JSON requests and stores
the generated output. The current implementation provides CRUD operations for
AWS S3 buckets and can be extended through the provider, service, and resource
route hierarchy.

## Requirements

- Go 1.26+
- Docker Engine with Docker Compose v2 for the local observability stack

## Structure

| Path | Responsibility |
|---|---|
| `cmd/server` | Load configuration, initialize the service, and handle signals |
| `internal/server` | Wire providers, run servers, poll templates, and shut down |
| `internal/aws` | Register AWS provider routes |
| `internal/aws/s3` | Register S3 service routes |
| `internal/aws/s3/bucket` | Validate requests and serve the bucket API |
| `internal/terraform` | Load templates, render Terraform, and coordinate storage |
| `internal/storage` | Define the storage contract and provide the filesystem backend |
| `internal/metrics` | Expose domain-specific Prometheus metrics |
| `internal/config` | Load and validate YAML configuration |
| `internal/logger` | Build the JSON Zap logger and bridge records to OpenTelemetry |

Route ownership follows `server -> provider -> service -> resource`.

## Native Run

Build and run without a local telemetry collector:

```bash
OTEL_TRACES_EXPORTER=none \
OTEL_METRICS_EXPORTER=none \
OTEL_LOGS_EXPORTER=none \
make run-server
```

The API listens on `http://localhost:8080`, Prometheus metrics are available at
`http://localhost:9091/metrics`, and generated files are written below
`output/aws`.

To build without starting the service:

```bash
make build
```

Both targets compile with the pinned `otelc` tool, which injects OpenTelemetry
instrumentation into the server binary.

## Local Deployment

The Compose deployment starts the service and its complete local observability
stack: Grafana Alloy, Tempo, Loki, Prometheus, and Grafana.

```bash
cd deploy
GIT_SHA="$(git rev-parse --short HEAD)" docker compose up --build -d
docker compose ps
```

The Git SHA is used as the container tag, service version, and OpenTelemetry
resource version. It defaults to `dev` when `GIT_SHA` is not provided.

| URL | Purpose |
|---|---|
| `http://localhost:8080/health` | Service health |
| `http://localhost:9091/metrics` | Service domain metrics |
| `http://localhost:3000` | Grafana with provisioned datasources and dashboard |
| `http://localhost:9090` | Prometheus |
| `http://localhost:12345` | Grafana Alloy status |
| `localhost:4317` | OTLP gRPC receiver |

Create a bucket to produce Terraform and telemetry:

```bash
curl -sS -X POST http://localhost:8080/api/aws/v1/s3/buckets \
  -H 'Content-Type: application/json' \
  -d '{"payload":{"properties":{"aws-region":"us-east-1","acl":"private","bucket-name":"local-example"}}}'
```

Read the generated configuration:

```bash
curl -sS http://localhost:8080/api/aws/v1/s3/buckets/local-example
```

The service returns the container path
`/output/aws/s3/local-example/main.tf`. Because Compose bind-mounts `output/`,
the same file is available on the host at
`../output/aws/s3/local-example/main.tf` while running from `deploy/`.

Templates are bind-mounted from `templates/`. Editing an AWS template on the
host reloads it without restarting the service.

Follow service logs:

```bash
docker compose logs -f server
```

Stop the stack while retaining Grafana, Tempo, Loki, Prometheus, and Alloy
state:

```bash
docker compose down
```

Remove the telemetry state as well:

```bash
docker compose down -v
```

Generated Terraform is not removed by `down -v` because `output/` is a host
directory rather than a Docker volume.

### Telemetry Flow

- Auto-instrumented traces are sent through Alloy to Tempo.
- Auto-instrumented HTTP and runtime metrics are sent through Alloy to Prometheus.
- Tempo derives request rate, errors, and latency metrics from server spans.
- Domain metrics are scraped from the service directly by Prometheus.
- JSON logs written to stdout are collected from Docker by Alloy and sent to Loki.
- Grafana provisions Tempo, Loki, Prometheus, and the service dashboard at startup.

The local deployment disables the OTLP log exporter to avoid sending each Zap
record twice. The OpenTelemetry Zap bridge remains available in deployments
that configure an OTLP log pipeline.

## Container Only

Run only the application container without the observability stack:

```bash
mkdir -p output
docker build -t terraform-parse-service:dev .
docker run --rm \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -p 8080:8080 \
  -p 9091:9091 \
  -v "$PWD/output:/output" \
  -e OTEL_TRACES_EXPORTER=none \
  -e OTEL_METRICS_EXPORTER=none \
  -e OTEL_LOGS_EXPORTER=none \
  terraform-parse-service:dev
```

## Configuration

The service reads `configs/config.yaml` by default. Set `CONFIG_PATH` to use a
different file.

```yaml
listen_addr: ":8080"
service_name: "terraform-parse-service"
environment: "${ENVIRONMENT:development}"
version: "${VERSION:dev}"
logger:
  level: "info"
metrics:
  addr: ":9091"
providers:
  aws:
    templates_dir: "./templates/aws"
    templates_poll_interval: "5s"
    storage_dir: "./output/aws"
```

Configuration values support `${VARIABLE:default}` expansion.

Templates are loaded at startup and polled per provider. Adding, removing,
renaming, or changing a `.tmpl` file reloads that provider without restarting
the service. A failed reload leaves the last valid template set active.

OpenTelemetry is configured with standard `OTEL_*` environment variables rather
than service-specific YAML fields. Common settings are:

| Variable | Purpose |
|---|---|
| `OTEL_SERVICE_NAME` | Service name attached to telemetry |
| `OTEL_RESOURCE_ATTRIBUTES` | Resource attributes such as environment and version |
| `OTEL_TRACES_EXPORTER` | Trace exporter, such as `otlp` or `none` |
| `OTEL_METRICS_EXPORTER` | Metrics exporter, such as `otlp` or `none` |
| `OTEL_LOGS_EXPORTER` | Log exporter, such as `otlp` or `none` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP collector endpoint |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | OTLP transport, such as `grpc` |
| `OTEL_TRACES_SAMPLER` | Standard OpenTelemetry sampler |

## Testing

```bash
make lint
make vet
make test-unit
make test-integration
make test
```

Unit tests cover the storage contract. Integration tests protect the HTTP API,
rendered Terraform behavior, routing responses, and template hot reload.
