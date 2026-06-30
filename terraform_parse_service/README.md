# Terraform Parse Service

HTTP service that renders Terraform HCL configurations from a JSON payload and writes them to the storage.

## Requirements

- Go 1.22+

## Build and run

```bash
go build -o server ./cmd/server
CONFIG_PATH=configs/config.yaml ./server
```

The server listens on `:8080` by default. Override with `listen_addr` in the config file (see [Configuration](#configuration)).

## Configuration

Config is read from `configs/config.yaml` at startup. Set `CONFIG_PATH` to use a different file.

```yaml
listen_addr: ":8080"
logger:
  level: "info"           # debug | info | warn | error
  metadata:
    service: "terraform-parse-service"
    env: "${APP_ENV}"     # resolved from environment
providers:
  aws:
    templates_dir: "./templates/aws"
    storage_dir: "./output/aws"
```

Values support `${VAR}` interpolation — any unset variable falls back to the literal string in the file.

| Environment variable | Effect |
|---|---|
| `CONFIG_PATH` | Path to the YAML config file (default: `configs/config.yaml`) |
| `APP_ENV` | Injected into the `env` metadata field on every log record |

## Testing

```bash
# unit tests
go test ./internal/...

# integration tests (require templates on disk)
go test ./test/...

# all
go test ./...
```
