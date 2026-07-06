SHA := $(shell git rev-parse --short HEAD)
IMAGE := terraform-parse-service:$(SHA)
CLUSTER := tripla
NAMESPACE_APP := terraform-parse-service
NAMESPACE_MON := monitoring

.PHONY: cluster-up cluster-down \
        image-build image-load \
        istio-install istio-patch \
        metrics-server \
        obs-repos obs-tempo obs-loki obs-prometheus obs-alloy obs-grafana obs \
        app-dep app-install app-upgrade app \
        port-forward-grafana port-forward-app \
        teardown-app teardown-obs teardown-cluster teardown \
        test

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
	@echo "Loaded: $(IMAGE)"

## ── Istio ────────────────────────────────────────────────────────────────────

istio-install:
	istioctl install --set profile=minimal -y
	kubectl rollout status deployment/istiod -n istio-system

istio-patch:
	kubectl patch svc istio-ingressgateway -n istio-system \
	  --type=json \
	  -p '[{"op":"replace","path":"/spec/type","value":"NodePort"},{"op":"add","path":"/spec/ports/0/nodePort","value":30080}]'
	kubectl create namespace $(NAMESPACE_APP) --dry-run=client -o yaml | kubectl apply -f -
	kubectl label namespace $(NAMESPACE_APP) istio-injection=enabled --overwrite

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
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
	helm repo update

obs-tempo:
	helm upgrade --install tempo grafana/tempo \
	  --namespace $(NAMESPACE_MON) --create-namespace \
	  --set tempo.storage.trace.backend=local \
	  --set tempo.storage.trace.local.path=/var/tempo/traces \
	  --set persistence.enabled=false
	kubectl rollout status deployment/tempo -n $(NAMESPACE_MON)

obs-loki:
	helm upgrade --install loki grafana/loki \
	  --namespace $(NAMESPACE_MON) \
	  --set deploymentMode=SingleBinary \
	  --set loki.auth_enabled=false \
	  --set loki.commonConfig.replication_factor=1 \
	  --set loki.storage.type=filesystem \
	  --set singleBinary.replicas=1 \
	  --set read.replicas=0 \
	  --set write.replicas=0 \
	  --set backend.replicas=0 \
	  --set 'loki.schemaConfig.configs[0].from=2024-01-01' \
	  --set 'loki.schemaConfig.configs[0].store=tsdb' \
	  --set 'loki.schemaConfig.configs[0].object_store=filesystem' \
	  --set 'loki.schemaConfig.configs[0].schema=v13' \
	  --set 'loki.schemaConfig.configs[0].index.prefix=loki_index_' \
	  --set 'loki.schemaConfig.configs[0].index.period=24h'
	kubectl rollout status statefulset/loki -n $(NAMESPACE_MON)

obs-prometheus:
	helm upgrade --install prometheus prometheus-community/prometheus \
	  --namespace $(NAMESPACE_MON) \
	  --set server.persistentVolume.enabled=false \
	  --set alertmanager.enabled=false \
	  --set pushgateway.enabled=false \
	  --set server.service.type=ClusterIP
	kubectl rollout status deployment/prometheus-server -n $(NAMESPACE_MON)

obs-alloy:
	helm upgrade --install alloy grafana/alloy \
	  --namespace $(NAMESPACE_MON) \
	  -f deploy/alloy-values.yaml
	kubectl rollout status daemonset/alloy -n $(NAMESPACE_MON)

obs-grafana:
	helm upgrade --install grafana grafana/grafana \
	  --namespace $(NAMESPACE_MON) \
	  -f deploy/grafana-values.yaml
	kubectl rollout status deployment/grafana -n $(NAMESPACE_MON)

obs: obs-repos obs-tempo obs-loki obs-prometheus obs-alloy obs-grafana

## ── App ──────────────────────────────────────────────────────────────────────

app-dep:
	helm dependency update helm/terraform-parse-service

app-install: app-dep
	helm install terraform-parse-service helm/terraform-parse-service \
	  -f helm/terraform-parse-service/values.yaml \
	  -f helm/terraform-parse-service/values-prod.yaml \
	  -f deploy/values-kind.yaml \
	  --set app.image.tag=$(SHA) \
	  --namespace $(NAMESPACE_APP)
	kubectl rollout status deployment/terraform-parse-service -n $(NAMESPACE_APP)

app-upgrade: app-dep
	helm upgrade terraform-parse-service helm/terraform-parse-service \
	  -f helm/terraform-parse-service/values.yaml \
	  -f helm/terraform-parse-service/values-prod.yaml \
	  -f deploy/values-kind.yaml \
	  --set app.image.tag=$(SHA) \
	  --namespace $(NAMESPACE_APP)
	kubectl rollout status deployment/terraform-parse-service -n $(NAMESPACE_APP)

app: app-install

## ── Port-forward ─────────────────────────────────────────────────────────────

port-forward-grafana:
	kubectl port-forward svc/grafana 3000:80 -n $(NAMESPACE_MON)

port-forward-app:
	kubectl port-forward svc/terraform-parse-service 8080:8080 -n $(NAMESPACE_APP)

## ── Test ─────────────────────────────────────────────────────────────────────

test:
	curl -s -X POST http://localhost/api/aws/v1/s3/buckets \
	  -H "Host: terraform-parse-service.example.com" \
	  -H "release: terraform-parse-service" \
	  -H "Content-Type: application/json" \
	  -d '{"payload":{"properties":{"aws-region":"us-east-1","acl":"private","bucket-name":"test-bucket-01"}}}' \
	  | jq .

## ── Teardown ─────────────────────────────────────────────────────────────────

teardown-app:
	helm uninstall terraform-parse-service -n $(NAMESPACE_APP) || true

teardown-obs:
	helm uninstall grafana alloy prometheus loki tempo -n $(NAMESPACE_MON) || true

teardown-cluster:
	kind delete cluster --name $(CLUSTER)

teardown: teardown-app teardown-obs teardown-cluster
