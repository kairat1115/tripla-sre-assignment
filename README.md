# tripla-sre-assignment

Assignment reference: https://github.com/umami-dev/interview/tree/main/terraform-parse

## Overview

`terraform_parse_service` is an HTTP service that generates Terraform HCL from Go templates. This repo includes:

- `terraform_parse_service/` — Go service source
- `helm/` — Helm charts (`app/`, `gateway/`, `terraform-parse-service/`)
- `deploy/` — kind cluster config, Helm values overrides, RBAC, observability configs, automation scripts

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

Verify:
```bash
kind version && kubectl version --client && helm version && istioctl version --remote=false
```

---

## Quick start (automated)

All commands run from the repo root.

```bash
# 1. Create kind cluster
make cluster-up

# 2. Build image and load into kind node
make image-load

# 3. Install Istio + patch IngressGateway NodePort
make istio-install
make istio-patch

# 4. Install metrics-server (required for HPA)
make metrics-server

# 5. Install full observability stack
make obs

# 6. Install service
make app

# 7. Test via Istio ingress (port 80)
make test
```

Access Grafana:
```bash
make port-forward-grafana
# Open http://localhost:3000
```

Teardown everything:
```bash
make teardown
```

---

## Step-by-step guide

All paths and commands assume **repo root** as working directory.

### Step 1: kind cluster

Config file: `deploy/kind-config.yaml` — control-plane node with port mapping `30080 → host:80` for the Istio IngressGateway.

```bash
kind create cluster --config deploy/kind-config.yaml
kubectl cluster-info --context kind-tripla
```

### Step 2: Build image and load into kind

```bash
SHA=$(git rev-parse --short HEAD)
IMAGE="terraform-parse-service:${SHA}"

docker build -t "${IMAGE}" ./terraform_parse_service
kind load docker-image "${IMAGE}" --name tripla

echo "Loaded: ${IMAGE}"
```

Multi-stage build: `golang:1.25-alpine` builder → `scratch` runtime. `imagePullPolicy: IfNotPresent` in values prevents a registry pull for kind-loaded images.

### Step 3: Install Istio

```bash
istioctl install --set profile=minimal -y
kubectl rollout status deployment/istiod -n istio-system
```

Patch the IngressGateway Service type to NodePort on 30080 (matches the kind port mapping):

```bash
kubectl patch svc istio-ingressgateway -n istio-system \
  --type=json \
  -p '[
    {"op":"replace","path":"/spec/type","value":"NodePort"},
    {"op":"add","path":"/spec/ports/0/nodePort","value":30080}
  ]'
```

> If the patch conflicts with an existing nodePort assignment, inspect first:
> `kubectl get svc istio-ingressgateway -n istio-system -o jsonpath='{.spec.ports}'`
> Then patch only the HTTP (port 80) entry.

Create namespace and enable Istio sidecar injection:

```bash
kubectl create namespace terraform-parse-service
kubectl label namespace terraform-parse-service istio-injection=enabled
```

### Step 4: Install metrics-server

Required for HPA — kind does not ship it. Prod values enable HPA (min 2 / max 6); without metrics-server the HPA stays in `Unknown` state.

```bash
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml

# kind uses self-signed kubelet certs — skip TLS verification
kubectl patch deployment metrics-server -n kube-system \
  --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

kubectl rollout status deployment/metrics-server -n kube-system
```

### Step 5: Observability stack

All components in the `monitoring` namespace. Mirrors the docker-compose `obs` network.

```bash
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
```

**Tempo** (trace backend — OTLP gRPC :4317, HTTP query :3200):
```bash
helm upgrade --install tempo grafana/tempo \
  --namespace monitoring --create-namespace \
  --set tempo.storage.trace.backend=local \
  --set tempo.storage.trace.local.path=/var/tempo/traces \
  --set persistence.enabled=false
kubectl rollout status deployment/tempo -n monitoring
```

**Loki** (log backend — single-binary, filesystem storage):
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

**Prometheus** (metrics backend — auto-discovers pods annotated `prometheus.io/scrape: "true"`):
```bash
helm upgrade --install prometheus prometheus-community/prometheus \
  --namespace monitoring \
  --set server.persistentVolume.enabled=false \
  --set alertmanager.enabled=false \
  --set pushgateway.enabled=false \
  --set server.service.type=ClusterIP
kubectl rollout status deployment/prometheus-server -n monitoring
```

**Grafana Alloy** (log + metric collection — DaemonSet with k8s pod log discovery):

Config file: `deploy/alloy-values.yaml`. Uses Kubernetes pod discovery + file tailing from `/var/log/pods` — replaces the docker-compose Docker socket approach which does not work in k8s.

```bash
helm upgrade --install alloy grafana/alloy \
  --namespace monitoring \
  -f deploy/alloy-values.yaml
kubectl rollout status daemonset/alloy -n monitoring
```

**Grafana** (UI — anonymous admin, pre-provisioned datasources):

Config file: `deploy/grafana-values.yaml`. Datasources point at k8s cluster-local service FQDNs.

```bash
helm upgrade --install grafana grafana/grafana \
  --namespace monitoring \
  -f deploy/grafana-values.yaml
kubectl rollout status deployment/grafana -n monitoring
```

### Step 6: kind-specific values override

File: `deploy/values-kind.yaml`

Two prod settings must be overridden for kind:

1. `tracing.insecure: true` — Tempo in kind has no TLS cert; the Go service dials gRPC without TLS when this is `true`.
2. `image.tag` — set to the SHA built in Step 2 (passed via `--set app.image.tag`).

The `configMaps` block is an inline YAML struct that cannot be overridden via `--set`. `deploy/values-kind.yaml` replaces the entire block with the correct Tempo endpoint + `insecure: true`.

### Step 7: Install terraform-parse-service

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

Verify the k8s-sidecar copied templates into the emptyDir:

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
| ConfigMap `*-config` | service config.yaml |
| ConfigMap `*-aws-s3-bucket-tf-tmpl` | S3 bucket Terraform template |
| Istio Gateway | `terraform-parse-service.example.com:80` |
| Istio VirtualService | header match `release: terraform-parse-service` |
| Istio DestinationRule | enabled (values-prod.yaml) |

### Step 8: Test the service

**Via Istio IngressGateway (port 80 → kind NodePort 30080):**

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

**Direct port-forward (bypasses Istio):**

```bash
kubectl port-forward svc/terraform-parse-service 8080:8080 -n terraform-parse-service

curl -s -X POST http://localhost:8080/api/aws/v1/s3/buckets \
  -H "Content-Type: application/json" \
  -d '{"payload":{"properties":{"aws-region":"us-east-1","acl":"private","bucket-name":"test-bucket-02"}}}' \
  | jq .
```

**Inspect generated Terraform:**

```bash
kubectl exec -n terraform-parse-service \
  deployment/terraform-parse-service \
  -c app \
  -- cat /output/aws/s3/test-bucket-01/main.tf
```

> Output lives in an emptyDir — lost on pod restart. This is by design; the service is stateless.

### Step 9: Observe in Grafana

```bash
kubectl port-forward svc/grafana 3000:80 -n monitoring
# Open http://localhost:3000 — anonymous admin, no login required
```

**Metrics (Prometheus datasource):**
- `terraform_generation_total` — generation count by provider/template/status
- `http_request_duration_seconds_bucket` — request latency histogram
- `http_requests_in_flight` — current concurrency gauge

**Traces (Tempo datasource):**
- Search `service.name = terraform-parse-service`
- Span tree per request: `http.request` → `service.generate` → storage write
- 10% sampling in prod (`sample_ratio: 0.1`)

**Logs (Loki datasource):**
- Label filter: `{namespace="terraform-parse-service"}`
- Filter by `level`, `trace_id` labels
- Click `trace_id` value in Loki to jump to correlated trace in Tempo (TracesToLogsV2 derivation)

---

## Teardown

```bash
make teardown
```

Or individually:
```bash
helm uninstall terraform-parse-service -n terraform-parse-service
helm uninstall grafana alloy prometheus loki tempo -n monitoring
kind delete cluster --name tripla
```

---

## Known issues

**Templates not hot-reloaded.** `LoadTemplates()` runs once at startup. k8s-sidecar writes new templates to `/templates` but the service never re-reads the directory. Adding a template requires a pod restart:
```bash
kubectl rollout restart deployment/terraform-parse-service -n terraform-parse-service
```

**RDS template has no handler.** `rds.tf.tmpl` exists in `helm/terraform-parse-service/files/templates/aws/s3/` but no HTTP handler is registered. The template is unreachable through the API.

**HPA warmup.** HPA shows `<unknown>` for CPU/memory for ~60s after deploy while metrics-server aggregates first data points.

**No application-level health endpoint.** Probes use `tcpSocket` on port 8080 — confirms port open, not service ready. A failed `LoadTemplates()` after server start will pass the probe while the service returns 500s.
