# tripla-sre-assignment

Terraform Parse Service assignment.

The service accepts JSON payloads, renders Terraform HCL from provider templates, writes generated files to local pod storage, and exposes HTTP, traces, logs, and Prometheus metrics.

## Assignment reference

https://github.com/umami-dev/interview/tree/main/terraform-parse

## Layout

| Path | Purpose |
| --- | --- |
| `terraform_parse_service/` | Go service, Dockerfile, local compose observability files |
| `helm/terraform-parse-service/` | Service Helm chart values and template ConfigMaps |
| `deploy/` | kind config, observability Helm values, expected test output |
| `Makefile` | Linux automation for kind, Istio, observability, app deploy, smoke test |

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
kind version && kubectl version --client && helm version && istioctl version --remote=false && make --version
```

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

# 4. Create app namespace with Istio injection enabled
make namespace-setup

# 5. Install metrics-server (required for HPA)
make metrics-server

# 6. Install full observability stack
make obs

# 7. Install service
make app

# 8. Test via Istio ingress (port 80)
make test
```

Access Grafana:
```bash
make port-forward-grafana
# Open http://localhost:3000
```

Grafana is installed with anonymous admin access and the `Terraform Parse Service` dashboard.

Teardown everything:
```bash
make teardown
```

## Image Version

The Makefile uses the current Git SHA as image tag:

```make
SHA := $(shell git rev-parse --short HEAD)
IMAGE_TAG ?= $(SHA)
```

That same tag is passed to Helm as `app.image.tag`. The production config sets:

```yaml
version: '{{ .Values.image.tag }}'
```

So service logs and OpenTelemetry resource attributes carry the same Git SHA as the deployed image tag.

## Step-by-step guide

All paths and commands assume **repo root** as working directory.

### Step 1: kind cluster

Config file: `deploy/kind-config.yaml` - control-plane node with port mapping `30080 → host:80` for the Istio IngressGateway.

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
istioctl install --set profile=default -y
kubectl rollout status deployment/istiod -n istio-system
kubectl rollout status deployment/istio-ingressgateway -n istio-system
```

Patch the IngressGateway Service type to NodePort on 30080 (matches the kind port mapping):

```bash
kubectl patch svc istio-ingressgateway -n istio-system \
  --type=json \
  -p '[
    {"op":"replace","path":"/spec/type","value":"NodePort"},
    {"op":"replace","path":"/spec/ports/1/nodePort","value":30080}
  ]'
```

> If the patch conflicts with an existing nodePort assignment, inspect first:
> `kubectl get svc istio-ingressgateway -n istio-system -o jsonpath='{.spec.ports}'`
> Then patch only the HTTP (port 80) entry.

> **After every cluster restart**, the IngressGateway NodePort assignment resets. Re-run `make istio-patch` before testing. If requests return 404 from Envoy, this is the first thing to check.

### Step 4: Create app namespace

Create namespaces and enable Istio sidecar injection on the app namespace:

```bash
kubectl create namespace terraform-parse-service --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace terraform-parse-service istio-injection=enabled --overwrite
kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -
```

### Step 5: Install metrics-server

Required for HPA - kind does not ship it. Without metrics-server the HPA stays in `Unknown` state.

```bash
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml

# kind uses self-signed kubelet certs - skip TLS verification
kubectl patch deployment metrics-server -n kube-system \
  --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

kubectl rollout status deployment/metrics-server -n kube-system
```

### Step 6: Observability stack

All components in the `monitoring` namespace. Charts use pinned OCI versions - no `helm repo add` needed for Tempo, Loki, Grafana, or Prometheus.

```bash
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update
```

**Tempo** (`oci://ghcr.io/grafana-community/helm-charts/tempo:2.2.3` - OTLP gRPC :4317, HTTP query :3200):
```bash
helm upgrade --install tempo oci://ghcr.io/grafana-community/helm-charts/tempo \
  --version 2.2.3 \
  --namespace monitoring \
  --set tempo.storage.trace.backend=local \
  --set tempo.storage.trace.local.path=/var/tempo/traces \
  --set persistence.enabled=false \
  --set tempo.metricsGenerator.enabled=true \
  --set tempo.metricsGenerator.remoteWriteUrl=http://prometheus-server.monitoring.svc.cluster.local/api/v1/write \
  --set 'tempo.overrides.defaults.metrics_generator.processors[0]=service-graphs' \
  --set 'tempo.overrides.defaults.metrics_generator.processors[1]=span-metrics' \
  --set 'tempo.overrides.defaults.metrics_generator.processors[2]=local-blocks'
kubectl rollout status statefulset/tempo -n monitoring
```

**Loki** (`oci://ghcr.io/grafana-community/helm-charts/loki:18.4.0` - single-binary, filesystem storage, memcached caches disabled):
```bash
helm upgrade --install loki oci://ghcr.io/grafana-community/helm-charts/loki \
  --version 18.4.0 \
  --namespace monitoring \
  --set deploymentMode=SingleBinary \
  --set loki.auth_enabled=false \
  --set loki.commonConfig.replication_factor=1 \
  --set loki.storage.type=filesystem \
  --set singleBinary.replicas=1 \
  --set read.replicas=0 --set write.replicas=0 --set backend.replicas=0 \
  --set chunksCache.enabled=false \
  --set resultsCache.enabled=false \
  --set 'loki.schemaConfig.configs[0].from=2024-01-01' \
  --set 'loki.schemaConfig.configs[0].store=tsdb' \
  --set 'loki.schemaConfig.configs[0].object_store=filesystem' \
  --set 'loki.schemaConfig.configs[0].schema=v13' \
  --set 'loki.schemaConfig.configs[0].index.prefix=loki_index_' \
  --set 'loki.schemaConfig.configs[0].index.period=24h'
kubectl rollout status statefulset/loki -n monitoring
```

**Prometheus** (`oci://ghcr.io/prometheus-community/charts/prometheus:29.14.0` - remote-write receiver enabled for Alloy):
```bash
helm upgrade --install prometheus oci://ghcr.io/prometheus-community/charts/prometheus \
  --version 29.14.0 \
  --namespace monitoring \
  --set server.persistentVolume.enabled=false \
  --set alertmanager.enabled=false \
  --set pushgateway.enabled=false \
  --set server.service.type=ClusterIP \
  --set 'server.extraFlags[0]=web.enable-remote-write-receiver'
kubectl rollout status deployment/prometheus-server -n monitoring
```

> `--web.enable-remote-write-receiver` is required. Alloy uses `prometheus.remote_write` to push metrics - without this flag Prometheus returns 404 on every write.

**Grafana Alloy** (`grafana/alloy:1.10.0` - DaemonSet, OTLP gRPC receiver on :4317):

Config file: `deploy/alloy-values.yaml`. Handles logs (`loki.source.kubernetes` API-based streaming), metrics (direct scrape of app pod :9091 - bypasses Istio annotation override), and traces (OTLP receiver → Tempo).

> Istio sidecar injection overwrites `prometheus.io/*` pod annotations with Envoy stats. Alloy scrapes the app metrics port directly instead of relying on those annotations.

```bash
helm upgrade --install alloy grafana/alloy \
  --version 1.10.0 \
  --namespace monitoring \
  -f deploy/alloy-values.yaml
kubectl rollout status daemonset/alloy -n monitoring
```

**Grafana** (`oci://ghcr.io/grafana-community/helm-charts/grafana:12.7.2` - anonymous admin, pre-provisioned datasources):

Dashboard for the service metrics at `terraform_parse_service/deploy/grafana/provisioning/dashboards/service.json`.

Config file: `deploy/grafana-values.yaml`. Datasources point at cluster-local service FQDNs.

```bash
kubectl create configmap grafana-dashboard-terraform-parse-service \
	  --from-file=service.json=terraform_parse_service/deploy/grafana/provisioning/dashboards/service.json \
	  --namespace monitoring \
	  --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install grafana oci://ghcr.io/grafana-community/helm-charts/grafana \
  --version 12.7.2 \
  --namespace monitoring \
  -f deploy/grafana-values.yaml
kubectl rollout status deployment/grafana -n monitoring
```

Grafana dashboard provisioning:

1. `make grafana-dashboard` creates ConfigMap `grafana-dashboard-terraform-parse-service` from `terraform_parse_service/deploy/grafana/provisioning/dashboards/service.json`.
2. `deploy/grafana-values.yaml` mounts that ConfigMap through the Grafana Helm chart.
3. `make obs-grafana` installs Grafana with datasources and dashboard ready.

Dashboard includes:

- HTTP request rate, 5xx ratio, p95 latency
- Terraform resource operation rate, errors, p95 latency
- Terraform generation rate, errors, p95 latency, rendered size
- storage operation rate and p95 latency
- template reloads and loaded template count

### Step 7: Install terraform-parse-service

```bash
helm dependency update helm/terraform-parse-service

SHA=$(git rev-parse --short HEAD)

helm install terraform-parse-service helm/terraform-parse-service \
  -f helm/terraform-parse-service/values.yaml \
  -f helm/terraform-parse-service/values-prod.yaml \
  --set app.image.repository=terraform-parse-service \
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
| Deployment | 1 replica, main + k8s-sidecar containers |
| Service | ClusterIP, port 8080 + 9091 metrics |
| HPA | disabled in prod values (emptyDir output volume prevents safe multi-replica scaling) |
| ConfigMap `*-config` | service config.yaml |
| ConfigMap `*-aws-s3-bucket-tf-tmpl` | S3 bucket Terraform template |
| Istio Gateway | `terraform-parse-service.example.com:80` |
| Istio VirtualService | header match `release: terraform-parse-service` |
| Istio DestinationRule | enabled (values-prod.yaml) |

### Step 8: Test the service

`make test` automates all three calls below — POST, GET list, GET contents — then diffs the bucket output against `deploy/expected-bucket.tf` and exits 1 on mismatch.

#### Create bucket

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

> Output lives in an emptyDir - lost on pod restart. This is by design; the service is stateless.

#### List buckets

**Via Istio IngressGateway (port 80 → kind NodePort 30080):**

```bash
curl -s http://localhost/api/aws/v1/s3/buckets \
  -H "Host: terraform-parse-service.example.com" \
  -H "release: terraform-parse-service" \
  -H "Content-Type: application/json" | jq .
```

Expected (HTTP 200):
```json
{"buckets": ["test-bucket-01"]}
```

**Direct port-forward (bypasses Istio):**

```bash
kubectl port-forward svc/terraform-parse-service 8080:8080 -n terraform-parse-service

curl -s http://localhost:8080/api/aws/v1/s3/buckets \
  -H "Content-Type: application/json" | jq .
```

#### Get created bucket contents

**Via Istio IngressGateway (port 80 → kind NodePort 30080):**

```bash
curl -s http://localhost/api/aws/v1/s3/buckets/test-bucket-01 \
  -H "Host: terraform-parse-service.example.com" \
  -H "release: terraform-parse-service" \
  -H "Content-Type: application/json" | jq .
```

Expected (HTTP 200): raw HCL matching `deploy/expected-bucket.tf`.

**Direct port-forward (bypasses Istio):**

```bash
kubectl port-forward svc/terraform-parse-service 8080:8080 -n terraform-parse-service

curl -s http://localhost:8080/api/aws/v1/s3/buckets/test-bucket-01 \
  -H "Content-Type: application/json" | jq .
```

### Step 9: Observe in Grafana

```bash
kubectl port-forward svc/grafana 3000:80 -n monitoring
# Open http://localhost:3000 - anonymous admin, no login required
```

**Metrics (Prometheus datasource):**
- `http_requests_total` - HTTP request rate
- `http_request_duration_seconds_bucket` - Request latency histogram
- `terraform_resource_operations_total` - Resource operation rate
- `terraform_resource_operation_duration_seconds_bucket` - Resource operation latency
- `terraform_generation_duration_seconds_bucket` - Terraform generation latency
- `terraform_generation_total` - Terraform generation by provider/template/status
- `terraform_rendered_bytes_bucket` - Rendered Terraform size
- `terraform_storage_operation_duration_seconds_bucket` - Storage operation latency
- `terraform_storage_operations_total` - Storage operations
- `terraform_template_reloads_total` - Template reloads
- `terraform_templates_loaded` - Loaded templates
- `terraform_template_reload_duration_seconds_bucket` - Template reload latency

**Traces (Tempo datasource):**
- Search `service.name = terraform-parse-service`
- Span tree per request: `http.request` → `service.generate` → storage write
- 100% sampling (`sample_ratio: 1.0`)

**Logs (Loki datasource):**
- Label filter: `{namespace="terraform-parse-service"}`
- Filter by `level`, `trace_id` labels
- Click `trace_id` value in Loki to jump to correlated trace in Tempo (TracesToLogsV2 derivation)

## CI/CD

GitHub Actions workflows live under `.github/workflows/`.

- `ci.yml` runs on pull requests and pushes to `main`.
- CI checks Go formatting, `go vet`, `go test ./...`, chart-testing (`ct lint` with Helm lint, yamllint, and helm-unittest), and a Docker image build.
- `release.yml` runs on release tags matching `v*` and release-candidate branches matching `rc-*`; both paths gate CD on the CI workflow.
- CD pushes the container image to `ghcr.io/<owner>/terraform-parse-service`.
- CD uploads these GitHub Release assets: Linux server tarballs for `amd64` and `arm64`, Helm chart `.tgz` packages for `app`, `gateway`, and `terraform-parse-service`, Terraform templates `.zip`, and `SHA256SUMS`.
- Release-candidate branches upload the same bundle as workflow artifacts instead of creating a GitHub Release.

Create a release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Create a release candidate:

```bash
git checkout -b rc-0.1.0
git push origin rc-0.1.0
```

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

## AI usage

I used Claude Code and Codex as an implementation assistant for the Go service, Terraform refactor, and Helm chart work. I guided the design, reviewed generated changes, tested the result, and made corrections where needed. See NOTES.md for details.
