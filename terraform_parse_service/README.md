# Terraform Parse Service

HTTP service that renders Terraform HCL configurations from a JSON payload and writes them to filesystem storage. Supports full CRUD over generated configs per resource type.

## Requirements

- Go 1.25+

## Build and run

```bash
go build -o server ./cmd/server
CONFIG_PATH=configs/config.yaml ./server
```

### Docker

```bash
docker build -t terraform-parse-service .
docker run --rm \
  -p 8080:8080 \
  --read-only \
  -v $(pwd)/output:/output \
  terraform-parse-service
```

### Observability stack

Runs the server together with Grafana Alloy, Tempo, Loki, Prometheus, and Grafana:

```bash
cd deploy
docker compose up --build
```

| Endpoint | Service |
|---|---|
| `http://localhost:8080` | Server |
| `http://localhost:3000` | Grafana (anonymous admin) |

Traces, logs, and metrics are available in Grafana under Explore. Tempo, Loki, and Prometheus datasources are provisioned automatically.

The server listens on `:8080` by default. Override with `listen_addr` in the config file (see [Configuration](#configuration)).

## Configuration

Config is read from `configs/config.yaml` at startup. Set `CONFIG_PATH` to use a different file.

```yaml
listen_addr: ":8080"
environment: "development"
version: "dev"
logger:
  level: "info"           # debug | info | warn | error
tracing:
  exporter: "stdout"      # stdout | otlp
  endpoint: "localhost:4317"
  insecure: false
  sample_ratio: 1.0
metrics:
  addr: ":9091"           # Prometheus metrics endpoint
providers:
  aws:
    templates_dir: "./templates/aws"
    storage_dir: "./output/aws"
```

Values support `${VAR}` interpolation — any unset variable falls back to the literal string in the file.

| Environment variable | Effect |
|---|---|
| `CONFIG_PATH` | Path to the YAML config file (default: `configs/config.yaml`) |

## Testing

```bash
# unit tests
go test ./internal/...

# integration tests (require templates on disk)
go test ./test/...

# all
go test ./...
```
