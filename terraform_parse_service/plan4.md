# Plan: Docker Compose Observability Stack

## Objective

Run the full observability stack locally via `docker compose up`. The stack includes:

| Component | Role |
|---|---|
| `server` | The Terraform Parse Service |
| `alloy` | Receives OTLP traces/metrics from the server; scrapes container logs; fans out to backends |
| `tempo` | Trace storage and query backend |
| `loki` | Log storage and query backend |
| `prometheus` | Metrics storage and query backend |
| `grafana` | Unified UI — datasources wired to Tempo, Loki, Prometheus |

Grafana Alloy replaces both the OTel Collector and Promtail in a single container. It receives OTLP from the server, scrapes Docker container stdout for logs, and fans everything out to the three backends.

Data flow:

```
server
  └── OTLP/gRPC :4317 ──► alloy
                               ├── traces  ──► tempo      :4317 (OTLP/gRPC)
                               ├── metrics ──► prometheus :9090 (remote write)
                               └── logs (OTLP + Docker stdout scrape) ──► loki :3100

grafana ──► tempo      (datasource)
        ──► loki       (datasource)
        ──► prometheus (datasource)
```

---

## Service versions

| Service | Image | Version |
|---|---|---|
| server | local build via `Dockerfile` | — |
| alloy | `grafana/alloy` | `v1.9.2` |
| tempo | `grafana/tempo` | `2.7.2` |
| loki | `grafana/loki` | `3.5.0` |
| prometheus | `prom/prometheus` | `v3.4.1` |
| grafana | `grafana/grafana` | `12.0.1` |

---

## Directory layout

All config files live under `deploy/`:

```
deploy/
  docker-compose.yaml
  config.yaml
  alloy/
    config.alloy
  tempo/
    config.yaml
  loki/
    config.yaml
  prometheus/
    prometheus.yaml
  grafana/
    provisioning/
      datasources/
        datasources.yaml
```

`deploy/config.yaml` is the server config used inside the compose stack. It differs from `configs/config.yaml` (used for local dev) in three fields: `tracing.exporter`, `tracing.endpoint`, and `storage_dir`.

---

## Component Design

### `deploy/docker-compose.yaml`

```yaml
name: terraform-parse-service

services:
  server:
    build:
      context: ..
      dockerfile: Dockerfile
    ports:
      - "8080:8080"
    volumes:
      - ./config.yaml:/etc/server/config.yaml:ro
      - ../templates:/templates:ro
      - output:/output
    environment:
      CONFIG_PATH: /etc/server/config.yaml
    read_only: true
    depends_on:
      alloy:
        condition: service_started
    networks:
      - obs

  alloy:
    image: grafana/alloy:v1.9.2
    command: ["run", "--server.http.listen-addr=0.0.0.0:12345", "/etc/alloy/config.alloy"]
    volumes:
      - ./alloy/config.alloy:/etc/alloy/config.alloy:ro
      - /var/run/docker.sock:/var/run/docker.sock:ro
    ports:
      - "4317:4317"   # OTLP gRPC
    depends_on:
      - tempo
      - loki
      - prometheus
    networks:
      - obs

  tempo:
    image: grafana/tempo:2.7.2
    command: ["-config.file=/etc/tempo.yaml"]
    volumes:
      - ./tempo/config.yaml:/etc/tempo.yaml:ro
      - tempo-data:/var/tempo
    networks:
      - obs

  loki:
    image: grafana/loki:3.5.0
    command: ["-config.file=/etc/loki/config.yaml"]
    volumes:
      - ./loki/config.yaml:/etc/loki/config.yaml:ro
      - loki-data:/loki
    networks:
      - obs

  prometheus:
    image: prom/prometheus:v3.4.1
    command:
      - --config.file=/etc/prometheus/prometheus.yaml
      - --web.enable-remote-write-receiver
    volumes:
      - ./prometheus/prometheus.yaml:/etc/prometheus/prometheus.yaml:ro
      - prometheus-data:/prometheus
    networks:
      - obs

  grafana:
    image: grafana/grafana:12.0.1
    ports:
      - "3000:3000"
    environment:
      GF_AUTH_ANONYMOUS_ENABLED: "true"
      GF_AUTH_ANONYMOUS_ORG_ROLE: "Admin"
    volumes:
      - ./grafana/provisioning:/etc/grafana/provisioning:ro
      - grafana-data:/var/lib/grafana
    depends_on:
      - tempo
      - loki
      - prometheus
    networks:
      - obs

volumes:
  output:
  tempo-data:
  loki-data:
  prometheus-data:
  grafana-data:

networks:
  obs:
    driver: bridge
```

**Key points:**
- `server` mounts `./config.yaml` (i.e. `deploy/config.yaml`) directly at `/etc/server/config.yaml`. No `configs/` mount needed — templates are mounted separately from `../templates`.
- `server` uses `read_only: true`. Output writes go to the named `output` volume.
- `alloy` mounts the Docker socket read-only for container log discovery.
- `alloy` port `4317` is published so you can also send spans directly from outside the compose network.
- `prometheus` starts with `--web.enable-remote-write-receiver` so Alloy can push metrics via remote write.

---

### `deploy/config.yaml`

Server config for use inside the compose stack. Differs from `configs/config.yaml` in `tracing.exporter`, `tracing.endpoint`, and `storage_dir`:

```yaml
listen_addr: ":8080"
logger:
  level: "info"
  metadata:
    service: "terraform-parse-service"
tracing:
  exporter: "otlp_grpc"
  endpoint: "alloy:4317"
  sample_ratio: 1.0
providers:
  aws:
    templates_dir: "/templates/aws"
    storage_dir: "/output/aws"
```

`templates_dir` and `storage_dir` are absolute paths matching the volume mounts. `tracing.endpoint` points to the `alloy` service by its compose hostname.

---

### `deploy/alloy/config.alloy`

Alloy uses River syntax (`.alloy` files). A single Alloy instance handles all three pipelines:

1. **OTLP receiver** — accepts traces and metrics from the server
2. **OTel pipeline** — batch → Tempo (traces), Prometheus remote write (metrics)
3. **Docker log scraping** — discovers the server container via Docker socket, parses JSON logs, promotes `trace_id` and `level` as Loki labels, pushes to Loki

```alloy
// ── OTLP receive ──────────────────────────────────────────────────────────────

otelcol.receiver.otlp "default" {
  grpc {
    endpoint = "0.0.0.0:4317"
  }

  output {
    traces  = [otelcol.processor.batch.default.input]
    metrics = [otelcol.processor.batch.default.input]
  }
}

otelcol.processor.batch "default" {
  output {
    traces  = [otelcol.exporter.otlp.tempo.input]
    metrics = [otelcol.exporter.prometheus.default.input]
  }
}

// ── Traces → Tempo ────────────────────────────────────────────────────────────

otelcol.exporter.otlp "tempo" {
  client {
    endpoint = "tempo:4317"
    tls {
      insecure = true
    }
  }
}

// ── Metrics → Prometheus ──────────────────────────────────────────────────────

otelcol.exporter.prometheus "default" {
  forward_to = [prometheus.remote_write.default.receiver]
}

prometheus.remote_write "default" {
  endpoint {
    url = "http://prometheus:9090/api/v1/write"
  }
}

// ── Logs: Docker stdout scrape → Loki ─────────────────────────────────────────

discovery.docker "server" {
  host = "unix:///var/run/docker.sock"

  filter {
    name   = "name"
    values = ["terraform-parse-service-server-1"]
  }
}

loki.source.docker "server" {
  host       = "unix:///var/run/docker.sock"
  targets    = discovery.docker.server.targets
  forward_to = [loki.process.server.receiver]

  relabel_rules {
    rule {
      source_labels = ["__meta_docker_container_label_com_docker_compose_service"]
      target_label  = "service"
    }
  }
}

loki.process "server" {
  forward_to = [loki.write.default.receiver]

  stage.json {
    expressions = {
      trace_id = "trace_id",
      level    = "level",
    }
  }

  stage.labels {
    values = {
      trace_id = null,
      level    = null,
    }
  }
}

loki.write "default" {
  endpoint {
    url = "http://loki:3100/loki/api/v1/push"
  }
}
```

**Key points:**
- The OTLP receiver handles traces and metrics only. Logs come from Docker stdout scraping — no OTel log bridge needed in the server.
- `otelcol.processor.batch` fans out to separate exporters per signal type after batching.
- `discovery.docker` filters by container name `terraform-parse-service-server-1` — the default name Docker Compose assigns to the first replica of the `server` service in a project named `terraform-parse-service`.
- `loki.process` parses the zap JSON log lines and promotes `trace_id` and `level` to Loki labels, enabling trace↔log correlation in Grafana.

---

### `deploy/tempo/config.yaml`

Minimal single-binary Tempo config:

```yaml
server:
  http_listen_port: 3200

distributor:
  receivers:
    otlp:
      protocols:
        grpc:
          endpoint: 0.0.0.0:4317

storage:
  trace:
    backend: local
    local:
      path: /var/tempo/blocks
    wal:
      path: /var/tempo/wal

compactor:
  compaction:
    block_retention: 48h
```

`local` backend writes to the `tempo-data` volume. Block retention is 48 hours — sufficient for local development.

---

### `deploy/loki/config.yaml`

Minimal single-binary Loki config:

```yaml
auth_enabled: false

server:
  http_listen_port: 3100

common:
  path_prefix: /loki
  storage:
    filesystem:
      chunks_directory: /loki/chunks
      rules_directory: /loki/rules
  replication_factor: 1
  ring:
    instance_addr: 127.0.0.1
    kvstore:
      store: inmemory

schema_config:
  configs:
    - from: 2020-10-24
      store: tsdb
      object_store: filesystem
      schema: v13
      index:
        prefix: index_
        period: 24h
```

`auth_enabled: false` disables multi-tenancy — required when Alloy pushes without an `X-Scope-OrgID` header.

---

### `deploy/prometheus/prometheus.yaml`

```yaml
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: prometheus
    static_configs:
      - targets: ["localhost:9090"]
```

Prometheus primarily receives data via remote write from Alloy. Self-scrape keeps the Prometheus UI functional.

---

### `deploy/grafana/provisioning/datasources/datasources.yaml`

Provisions all three datasources at startup — no manual UI setup required:

```yaml
apiVersion: 1

datasources:
  - name: Tempo
    type: tempo
    access: proxy
    url: http://tempo:3200
    isDefault: false
    jsonData:
      tracesToLogsV2:
        datasourceUid: loki
        filterByTraceID: true
      serviceMap:
        datasourceUid: prometheus

  - name: Loki
    type: loki
    access: proxy
    url: http://loki:3100
    isDefault: false
    jsonData:
      derivedFields:
        - name: TraceID
          matcherRegex: '"trace_id":"(\w+)"'
          url: "$${__value.raw}"
          datasourceUid: tempo

  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://prometheus:9090
    isDefault: true
```

**Key points:**
- `tracesToLogsV2` wires Tempo → Loki trace-to-logs correlation: clicking a trace in Grafana opens the matching logs filtered by `traceID`.
- `derivedFields` in the Loki datasource extracts `trace_id` from the JSON log line and creates a link to the corresponding trace in Tempo.
- `serviceMap` connects Tempo to Prometheus for service graph rendering in Tempo's search UI.

---

## Files to create

| File | Description |
|---|---|
| `deploy/docker-compose.yaml` | Full stack definition |
| `deploy/config.yaml` | Server config for compose stack — `otlp_grpc` exporter pointing to `alloy`, absolute storage/template paths |
| `deploy/alloy/config.alloy` | Alloy River config: OTLP receive, traces→Tempo, metrics→Prometheus, Docker log scrape→Loki |
| `deploy/tempo/config.yaml` | Tempo single-binary, local storage |
| `deploy/loki/config.yaml` | Loki single-binary, local storage, no auth |
| `deploy/prometheus/prometheus.yaml` | Minimal Prometheus config with remote write receiver |
| `deploy/grafana/provisioning/datasources/datasources.yaml` | Auto-provisioned datasources with trace↔log correlation |

---

## TODO

### Phase 1 — Directory structure and config files ✅

- [x] Create `deploy/` directory structure
- [x] Write `deploy/config.yaml`
- [x] Write `deploy/alloy/config.alloy`
- [x] Write `deploy/tempo/config.yaml`
- [x] Write `deploy/loki/config.yaml`
- [x] Write `deploy/prometheus/prometheus.yaml`
- [x] Write `deploy/grafana/provisioning/datasources/datasources.yaml`

### Phase 2 — `deploy/docker-compose.yaml` ✅

- [x] Define `server` service: build from `Dockerfile`, `read_only`, volume mounts, `CONFIG_PATH` pointing to `deploy/config.yaml`
- [x] Define `alloy` service with Docker socket mount and OTLP port
- [x] Define `tempo` service
- [x] Define `loki` service
- [x] Define `prometheus` service with `--web.enable-remote-write-receiver`
- [x] Define `grafana` service with anonymous admin auth
- [x] Define named volumes: `output`, `tempo-data`, `loki-data`, `prometheus-data`, `grafana-data`
- [x] Define `obs` bridge network

### Phase 3 — Verification

- [ ] `docker compose up --build` — all services reach healthy/running state
- [ ] Send a request: `curl -X POST http://localhost:8080/api/aws/v1/s3/buckets -d '{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":"test"}}}'`
- [ ] Grafana at `http://localhost:3000` — Explore → Tempo: trace visible with three nested spans
- [ ] Grafana → Loki: log line visible with `trace_id` label; clicking trace link opens span in Tempo
- [ ] Grafana → Prometheus: metrics visible (OTel SDK default runtime metrics)

