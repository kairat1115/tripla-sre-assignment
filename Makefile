SHA              := $(shell git rev-parse --short HEAD)
IMAGE_REPOSITORY ?= terraform-parse-service
IMAGE_TAG        ?= $(SHA)
IMAGE            := $(IMAGE_REPOSITORY):$(IMAGE_TAG)

CLUSTER       := tripla
NAMESPACE_APP := terraform-parse-service
NAMESPACE_MON := monitoring
APP_RELEASE   := terraform-parse-service

DASHBOARD_CONFIGMAP := grafana-dashboard-terraform-parse-service
DASHBOARD_FILE      := terraform_parse_service/deploy/grafana/provisioning/dashboards/service.json

CHART_TEMPO   := oci://ghcr.io/grafana-community/helm-charts/tempo
CHART_LOKI    := oci://ghcr.io/grafana-community/helm-charts/loki
CHART_GRAFANA := oci://ghcr.io/grafana-community/helm-charts/grafana
CHART_ALLOY   := grafana/alloy
CHART_PROM    := oci://ghcr.io/prometheus-community/charts/prometheus

VERSION_TEMPO   := 2.2.3
VERSION_LOKI    := 18.4.0
VERSION_GRAFANA := 12.7.2
VERSION_ALLOY   := 1.10.0
VERSION_PROM    := 29.14.0

.PHONY: cluster-up cluster-down \
	image-build image-load \
	istio-install istio-patch namespace-setup metrics-server \
	obs-repos obs-tempo obs-loki obs-prometheus obs-alloy grafana-dashboard obs-grafana obs \
	app-dep app-install app-upgrade app app-render \
	port-forward-grafana port-forward-app \
	test teardown-app teardown-obs teardown-cluster teardown

## ── Cluster ──────────────────────────────────────────────────────────────────

cluster-up:
	kind create cluster --config deploy/kind-config.yaml
	kubectl cluster-info --context kind-$(CLUSTER)

cluster-down:
	kind delete cluster --name $(CLUSTER)

## ── Image ────────────────────────────────────────────────────────────────────

image-build:
	docker build -t $(IMAGE) ./terraform_parse_service

image-load: image-build
	kind load docker-image $(IMAGE) --name $(CLUSTER)
	@echo "Loaded $(IMAGE)"

## ── Istio ────────────────────────────────────────────────────────────────────

istio-install:
	istioctl install --set profile=default -y
	kubectl rollout status deployment/istiod -n istio-system
	kubectl rollout status deployment/istio-ingressgateway -n istio-system

istio-patch:
	kubectl patch svc istio-ingressgateway -n istio-system \
	  --type=json \
	  -p '[{"op":"replace","path":"/spec/type","value":"NodePort"},{"op":"replace","path":"/spec/ports/1/nodePort","value":30080}]'

namespace-setup:
	kubectl create namespace $(NAMESPACE_APP) --dry-run=client -o yaml | kubectl apply -f -
	kubectl label namespace $(NAMESPACE_APP) istio-injection=enabled --overwrite
	kubectl create namespace $(NAMESPACE_MON) --dry-run=client -o yaml | kubectl apply -f -

## ── metrics-server ───────────────────────────────────────────────────────────

metrics-server:
	kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
	kubectl patch deployment metrics-server -n kube-system \
	  --type=json \
	  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
	kubectl rollout status deployment/metrics-server -n kube-system

## ── Observability ────────────────────────────────────────────────────────────

obs-repos:
	helm repo add grafana https://grafana.github.io/helm-charts
	helm repo update

obs-tempo:
	helm upgrade --install tempo $(CHART_TEMPO) \
	  --version $(VERSION_TEMPO) \
	  --namespace $(NAMESPACE_MON) \
	  --set tempo.storage.trace.backend=local \
	  --set tempo.storage.trace.local.path=/var/tempo/traces \
	  --set persistence.enabled=false \
	  --set tempo.metricsGenerator.enabled=true \
	  --set tempo.metricsGenerator.remoteWriteUrl=http://prometheus-server.monitoring.svc.cluster.local/api/v1/write \
	  --set 'tempo.overrides.defaults.metrics_generator.processors[0]=service-graphs' \
	  --set 'tempo.overrides.defaults.metrics_generator.processors[1]=span-metrics' \
	  --set 'tempo.overrides.defaults.metrics_generator.processors[2]=local-blocks'
	kubectl rollout status statefulset/tempo -n $(NAMESPACE_MON)

obs-loki:
	helm upgrade --install loki $(CHART_LOKI) \
	  --version $(VERSION_LOKI) \
	  --namespace $(NAMESPACE_MON) \
	  --set deploymentMode=SingleBinary \
	  --set loki.auth_enabled=false \
	  --set loki.commonConfig.replication_factor=1 \
	  --set loki.storage.type=filesystem \
	  --set singleBinary.replicas=1 \
	  --set read.replicas=0 \
	  --set write.replicas=0 \
	  --set backend.replicas=0 \
	  --set chunksCache.enabled=false \
	  --set resultsCache.enabled=false \
	  --set 'loki.schemaConfig.configs[0].from=2024-01-01' \
	  --set 'loki.schemaConfig.configs[0].store=tsdb' \
	  --set 'loki.schemaConfig.configs[0].object_store=filesystem' \
	  --set 'loki.schemaConfig.configs[0].schema=v13' \
	  --set 'loki.schemaConfig.configs[0].index.prefix=loki_index_' \
	  --set 'loki.schemaConfig.configs[0].index.period=24h'
	kubectl rollout status statefulset/loki -n $(NAMESPACE_MON)

obs-prometheus:
	helm upgrade --install prometheus $(CHART_PROM) \
	  --version $(VERSION_PROM) \
	  --namespace $(NAMESPACE_MON) \
	  --set server.persistentVolume.enabled=false \
	  --set alertmanager.enabled=false \
	  --set pushgateway.enabled=false \
	  --set server.service.type=ClusterIP \
	  --set 'server.extraFlags[0]=web.enable-remote-write-receiver'
	kubectl rollout status deployment/prometheus-server -n $(NAMESPACE_MON)

obs-alloy:
	helm upgrade --install alloy $(CHART_ALLOY) \
	  --version $(VERSION_ALLOY) \
	  --namespace $(NAMESPACE_MON) \
	  -f deploy/alloy-values.yaml
	kubectl rollout status daemonset/alloy -n $(NAMESPACE_MON)

grafana-dashboard:
	kubectl create configmap $(DASHBOARD_CONFIGMAP) \
	  --from-file=service.json=$(DASHBOARD_FILE) \
	  --namespace $(NAMESPACE_MON) \
	  --dry-run=client -o yaml | kubectl apply -f -

obs-grafana: grafana-dashboard
	helm upgrade --install grafana $(CHART_GRAFANA) \
	  --version $(VERSION_GRAFANA) \
	  --namespace $(NAMESPACE_MON) \
	  -f deploy/grafana-values.yaml
	kubectl rollout status deployment/grafana -n $(NAMESPACE_MON)

obs: obs-repos obs-tempo obs-loki obs-prometheus obs-alloy obs-grafana

## ── App ──────────────────────────────────────────────────────────────────────

app-dep:
	helm dependency update helm/terraform-parse-service

app-install: app-dep
	helm install $(APP_RELEASE) helm/terraform-parse-service \
	  -f helm/terraform-parse-service/values.yaml \
	  -f helm/terraform-parse-service/values-prod.yaml \
	  --set app.image.repository=$(IMAGE_REPOSITORY) \
	  --set app.image.tag=$(IMAGE_TAG) \
	  --namespace $(NAMESPACE_APP)
	kubectl rollout status deployment/$(APP_RELEASE) -n $(NAMESPACE_APP)

app-upgrade: app-dep
	helm upgrade --install $(APP_RELEASE) helm/terraform-parse-service \
	  -f helm/terraform-parse-service/values.yaml \
	  -f helm/terraform-parse-service/values-prod.yaml \
	  -f deploy/values-kind.yaml \
	  --set app.image.repository=$(IMAGE_REPOSITORY) \
	  --set app.image.tag=$(IMAGE_TAG) \
	  --namespace $(NAMESPACE_APP)
	kubectl rollout status deployment/$(APP_RELEASE) -n $(NAMESPACE_APP)

app: app-install

app-render: app-dep
	helm template $(APP_RELEASE) helm/terraform-parse-service \
	  -f helm/terraform-parse-service/values.yaml \
	  -f helm/terraform-parse-service/values-prod.yaml \
	  -f deploy/values-kind.yaml \
	  --set app.image.repository=$(IMAGE_REPOSITORY) \
	  --set app.image.tag=$(IMAGE_TAG) \
	  --namespace $(NAMESPACE_APP)

## ── Port-forward ─────────────────────────────────────────────────────────────

port-forward-grafana:
	kubectl port-forward svc/grafana 3000:80 -n $(NAMESPACE_MON)

port-forward-app:
	kubectl port-forward svc/$(APP_RELEASE) 8080:8080 -n $(NAMESPACE_APP)

## ── Test ─────────────────────────────────────────────────────────────────────

CURL := curl -s \
  -H "Host: terraform-parse-service.example.com" \
  -H "release: $(APP_RELEASE)" \
  -H "Content-Type: application/json"

test:
	@echo "=== POST: create bucket ==="
	@$(CURL) -X POST http://localhost/api/aws/v1/s3/buckets \
	  -d '{"payload":{"properties":{"aws-region":"us-east-1","acl":"private","bucket-name":"test-bucket-01"}}}' \
	  | jq .
	@echo "=== GET: list buckets ==="
	@$(CURL) http://localhost/api/aws/v1/s3/buckets | jq .
	@echo "=== GET: bucket contents ==="
	@$(CURL) http://localhost/api/aws/v1/s3/buckets/test-bucket-01 | tee /tmp/actual.tf
	@echo "=== DIFF: actual vs expected ==="
	@diff deploy/expected-bucket.tf /tmp/actual.tf \
	  && echo "PASS: output matches expected" \
	  || (echo "FAIL: output differs" && exit 1)

## ── Teardown ─────────────────────────────────────────────────────────────────

teardown-app:
	helm uninstall $(APP_RELEASE) -n $(NAMESPACE_APP) || true

teardown-obs:
	helm uninstall grafana alloy prometheus loki tempo -n $(NAMESPACE_MON) || true
	kubectl delete configmap $(DASHBOARD_CONFIGMAP) -n $(NAMESPACE_MON) --ignore-not-found

teardown-cluster:
	kind delete cluster --name $(CLUSTER)

teardown: teardown-app teardown-obs teardown-cluster
