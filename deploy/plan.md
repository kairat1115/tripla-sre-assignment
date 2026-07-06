# Deployment Plan: terraform-parse-service on kind

Full local Kubernetes deployment mirroring production config: kind cluster, Istio, full observability stack (Alloy + Tempo + Loki + Prometheus + Grafana), terraform-parse-service via Helm with prod values.

All commands run from **repo root**. Config files live in `deploy/`. Automation is in `Makefile` at repo root.

---

## Prerequisites

| Tool | Minimum version | Purpose |
|------|----------------|---------|
| `kind` | v0.20+ | local k8s cluster |
| `kubectl` | v1.28+ | cluster management |
| `helm` | v3.14+ | chart install |
| `istioctl` | v1.20+ | Istio install |
| `docker` | any recent | image build + kind load |
| `make` | any | automation |
| `jq` | any | test output formatting |

```bash
kind version && kubectl version --client && helm version && istioctl version --remote=false
```

---

## Step 1: kind cluster

Config file: `deploy/kind-config.yaml` — control-plane node with `containerPort: 30080 → hostPort: 80`. This is the entry point for Istio IngressGateway.

```bash
kind create cluster --config deploy/kind-config.yaml
kubectl cluster-info --context kind-tripla
```

Makefile: `make cluster-up`

---

## Step 2: Build image and load into kind

```bash
SHA=$(git rev-parse --short HEAD)
IMAGE="terraform-parse-service:${SHA}"

docker build -t "${IMAGE}" ./terraform_parse_service
kind load docker-image "${IMAGE}" --name tripla

echo "Loaded: ${IMAGE}"
```

Multi-stage Dockerfile (`golang:1.25-alpine` → `scratch`). `imagePullPolicy: IfNotPresent` in `helm/terraform-parse-service/values.yaml` means the kind-loaded image is used without a registry pull.

Makefile: `make image-load` (runs `image-build` first)

---

## Step 3: Install Istio

Use the `default` profile — includes Istiod + IngressGateway. The `minimal` profile omits the IngressGateway.

```bash
istioctl install --set profile=default -y
kubectl rollout status deployment/istiod -n istio-system
kubectl rollout status deployment/istio-ingressgateway -n istio-system
```

Patch the IngressGateway Service to NodePort 30080 (matches the kind port mapping):

```bash
kubectl patch svc istio-ingressgateway -n istio-system \
  --type=json \
  -p '[
    {"op":"replace","path":"/spec/type","value":"NodePort"},
    {"op":"replace","path":"/spec/ports/1/nodePort","value":30080}
  ]'
```

> If the patch conflicts with an existing nodePort assignment:
> `kubectl get svc istio-ingressgateway -n istio-system -o jsonpath='{.spec.ports}'`
> Then patch only the HTTP (port 80) entry.

Create namespace and enable sidecar injection:

```bash
kubectl create namespace terraform-parse-service
kubectl label namespace terraform-parse-service istio-injection=enabled
```

Makefile: `make istio-install istio-patch`

---

## Step 4: Install metrics-server

kind does not ship metrics-server. Prod values enable HPA (min 2 / max 6) — without it the HPA stays in `Unknown` state indefinitely.

```bash
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml

kubectl patch deployment metrics-server -n kube-system \
  --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

kubectl rollout status deployment/metrics-server -n kube-system
```

The `--kubelet-insecure-tls` patch is required because kind uses self-signed kubelet certificates.

Makefile: `make metrics-server`

---

## Step 5: Observability stack

All components go into the `monitoring` namespace. Mirrors the `obs` network in the docker-compose stack.

```bash
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
```

### 5.1 Tempo (trace backend)

Receives OTLP gRPC from the service on `:4317`. Exposes HTTP query API on `:3200` (Grafana datasource URL).

```bash
helm upgrade --install tempo grafana/tempo \
  --namespace monitoring --create-namespace \
  --set tempo.storage.trace.backend=local \
  --set tempo.storage.trace.local.path=/var/tempo/traces \
  --set persistence.enabled=false
kubectl rollout status statefulset/tempo -n monitoring
```

Service DNS: `tempo.monitoring.svc.cluster.local`

### 5.2 Loki (log backend)

Single-binary mode with filesystem storage — correct for a single-node kind cluster.

```bash
helm upgrade --install loki grafana/loki \
  --namespace monitoring \
  --set deploymentMode=SingleBinary \
  --set loki.auth_enabled=false \
  --set loki.commonConfig.replication_factor=1 \
  --set loki.storage.type=filesystem \
  --set singleBinary.replicas=1 \
  --set read.replicas=0 --set write.replicas=0 --set backend.replicas=0 \
  --set 'loki.schemaConfig.configs[0].from=2024-01-01' \
  --set 'loki.schemaConfig.configs[0].store=tsdb' \
  --set 'loki.schemaConfig.configs[0].object_store=filesystem' \
  --set 'loki.schemaConfig.configs[0].schema=v13' \
  --set 'loki.schemaConfig.configs[0].index.prefix=loki_index_' \
  --set 'loki.schemaConfig.configs[0].index.period=24h'
kubectl rollout status statefulset/loki -n monitoring
```

Service DNS: `loki.monitoring.svc.cluster.local:3100`

### 5.3 Prometheus

The `prometheus-community/prometheus` chart includes a `kubernetes-pods` scrape job that auto-discovers pods annotated with `prometheus.io/scrape: "true"`. The terraform-parse-service Deployment has this annotation (along with `prometheus.io/port: "9091"`), so metrics are scraped automatically without extra config.

```bash
helm upgrade --install prometheus prometheus-community/prometheus \
  --namespace monitoring \
  --set server.persistentVolume.enabled=false \
  --set alertmanager.enabled=false \
  --set pushgateway.enabled=false \
  --set server.service.type=ClusterIP
kubectl rollout status deployment/prometheus-server -n monitoring
```

Service DNS: `prometheus-server.monitoring.svc.cluster.local:80` (proxies to 9090)

### 5.4 Grafana Alloy (log collection)

The docker-compose stack used Docker socket log discovery — that approach is not applicable in Kubernetes. In k8s, Alloy runs as a DaemonSet, mounts `/var/log/pods` from the host node, and uses Kubernetes pod metadata discovery to locate log files and attach labels.

Config file: `deploy/alloy-values.yaml`

The Alloy config:
- Discovers pods in the `terraform-parse-service` namespace
- Keeps only pods with label `app.kubernetes.io/name=terraform-parse-service`
- Maps each pod container to its log file under `/var/log/pods/*<uid>/<container>/*.log`
- Parses JSON log lines, extracts `trace_id` and `level` as Loki labels
- Ships to `loki.monitoring.svc.cluster.local:3100`

```bash
helm upgrade --install alloy grafana/alloy \
  --namespace monitoring \
  -f deploy/alloy-values.yaml
kubectl rollout status daemonset/alloy -n monitoring
```

The chart creates the necessary ClusterRole for pod and node log access by default (`rbac.create: true`).

### 5.5 Grafana

Config file: `deploy/grafana-values.yaml`

Anonymous admin access enabled. Three pre-provisioned datasources pointing at cluster-local service FQDNs:
- Prometheus: default datasource
- Tempo: with TracesToLogs (Loki) and service map (Prometheus) linked
- Loki: with `trace_id` label → Tempo derivation

```bash
helm upgrade --install grafana grafana/grafana \
  --namespace monitoring \
  -f deploy/grafana-values.yaml
kubectl rollout status deployment/grafana -n monitoring
```

Makefile: `make obs` (runs all five steps)

---

## Step 6: kind-specific values override

File: `deploy/values-kind.yaml`

Two settings in `values-prod.yaml` break in kind and must be overridden:

**1. `tracing.insecure: true`**

`values-prod.yaml` sets `tracing.insecure: false`. The Go service (`internal/tracing/tracing.go`) appends `otlptracegrpc.WithInsecure()` only when `cfg.Tracing.Insecure == true`. Tempo in kind has no TLS certificate — the gRPC dial will fail with a TLS handshake error unless `insecure: true` is set.

**2. `image.tag`**

Set to the SHA from Step 2 at install time via `--set app.image.tag="${SHA}"`. The `values-kind.yaml` file sets `pullPolicy: IfNotPresent` to prevent any pull attempt.

**Why a values file instead of `--set` for the configMaps block:**

`configMaps` is an array of objects with nested YAML — `--set` cannot express this without escaping issues. `deploy/values-kind.yaml` overrides the entire `configMaps` block. All other prod settings (replicas, HPA, resources, Istio routing) remain from `values-prod.yaml`.

---

## Step 7: Install terraform-parse-service

```bash
helm dependency update helm/terraform-parse-service

SHA=$(git rev-parse --short HEAD)

helm install terraform-parse-service helm/terraform-parse-service \
  -f helm/terraform-parse-service/values.yaml \
  -f helm/terraform-parse-service/values-prod.yaml \
  -f deploy/values-kind.yaml \
  --set app.image.tag="${SHA}" \
  --namespace terraform-parse-service

kubectl rollout status deployment/terraform-parse-service -n terraform-parse-service
```

Values merge order (last wins): `values.yaml` → `values-prod.yaml` → `values-kind.yaml` → `--set`.

Verify k8s-sidecar copied templates:

```bash
kubectl exec -n terraform-parse-service \
  deployment/terraform-parse-service \
  -c k8s-sidecar -- ls /templates/aws/s3/
# Expected: bucket.tf.tmpl
```

**Resources deployed:**

| Resource | Details |
|----------|---------|
| Deployment | 2 replicas, main + k8s-sidecar containers |
| Service | ClusterIP, port 8080 + 9091 metrics |
| HPA | min 2 / max 6, CPU 50% + memory 80% |
| ConfigMap `*-config` | service config from `deploy/values-kind.yaml` |
| ConfigMap `*-aws-s3-bucket-tf-tmpl` | S3 template from `helm/terraform-parse-service/files/templates/aws/s3/bucket.tf.tmpl` |
| Istio Gateway | `terraform-parse-service.example.com:80` |
| Istio VirtualService | header match `release: terraform-parse-service` |
| Istio DestinationRule | enabled via `values-prod.yaml` |

Makefile: `make app` (runs dep update + install)

---

## Step 8: Test the service

### Via Istio IngressGateway (port 80 → kind NodePort 30080)

VirtualService routes based on two conditions: `Host` header matches `terraform-parse-service.example.com`, and `release` header matches the Helm release name.

```bash
curl -s -X POST http://localhost/api/aws/v1/s3/buckets \
  -H "Host: terraform-parse-service.example.com" \
  -H "release: terraform-parse-service" \
  -H "Content-Type: application/json" \
  -d '{
    "payload": {
      "properties": {
        "aws-region": "us-east-1",
        "acl": "private",
        "bucket-name": "test-bucket-01"
      }
    }
  }' | jq .
```

Expected (HTTP 201):
```json
{"output_path": "/output/aws/s3/test-bucket-01/main.tf"}
```

Makefile: `make test`

### Direct port-forward (bypasses Istio — useful for debugging)

```bash
kubectl port-forward svc/terraform-parse-service 8080:8080 -n terraform-parse-service

curl -s -X POST http://localhost:8080/api/aws/v1/s3/buckets \
  -H "Content-Type: application/json" \
  -d '{"payload":{"properties":{"aws-region":"us-east-1","acl":"private","bucket-name":"test-bucket-02"}}}' \
  | jq .
```

Makefile: `make port-forward-app`

### Inspect generated Terraform

```bash
kubectl exec -n terraform-parse-service \
  deployment/terraform-parse-service \
  -c app \
  -- cat /output/aws/s3/test-bucket-01/main.tf
```

Output lives in an `emptyDir` volume — lost on pod restart. The service is stateless by design.

---

## Step 9: Observe in Grafana

```bash
kubectl port-forward svc/grafana 3000:80 -n monitoring
# Open http://localhost:3000 — anonymous admin, no login
```

Makefile: `make port-forward-grafana`

### Metrics (Prometheus datasource)
- `terraform_generation_total{provider,resource,status}` — generation count
- `terraform_generation_duration_seconds` — generation latency histogram
- `http_request_duration_seconds_bucket{method,path}` — HTTP latency
- `http_requests_in_flight{method,path}` — current concurrency

### Traces (Tempo datasource)
- Search: `service.name = terraform-parse-service`
- Span tree per request: `http.request` → `service.generate` → storage write span
- `sample_ratio: 0.1` — 10% of requests are traced in prod

### Logs (Loki datasource)
- Label filter: `{namespace="terraform-parse-service"}`
- Refine with `level`, `trace_id` label filters
- Click a `trace_id` value to jump to the correlated Tempo trace (TracesToLogsV2 derivation)

---

## Teardown

```bash
make teardown
```

Individual steps:
```bash
helm uninstall terraform-parse-service -n terraform-parse-service   # make teardown-app
helm uninstall grafana alloy prometheus loki tempo -n monitoring     # make teardown-obs
kind delete cluster --name tripla                                    # make teardown-cluster
```

---

## Known issues and workarounds

### Templates not hot-reloaded
`LoadTemplates()` (`internal/service/terraform.go:43`) runs once at startup. The k8s-sidecar writes new CM data to `/templates` but the service never re-reads. Adding a template requires a pod restart:
```bash
kubectl rollout restart deployment/terraform-parse-service -n terraform-parse-service
```

### RDS template has no handler
`helm/terraform-parse-service/files/templates/aws/s3/rds.tf.tmpl` exists and is deployed in `values-staging.yaml`, but no HTTP handler is registered for it. The template is unreachable through the API.

### HPA warmup
HPA shows `<unknown>` for CPU/memory for ~60s after deploy while metrics-server aggregates its first data points. This is cosmetic.

### No application-level health endpoint
Probes use `tcpSocket` on port 8080 — confirms the port is open, not that templates loaded successfully. If `LoadTemplates()` errors after the server starts listening, the pod passes the probe while returning 500s on every request.
