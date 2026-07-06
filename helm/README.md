# Helm Charts

Three charts in this directory:

| Chart | Purpose |
|---|---|
| [`app/`](app/) | Generic reusable chart — Deployment, Service, HPA, ConfigMap |
| [`gateway/`](gateway/) | Istio routing resources — Gateway, VirtualService, DestinationRule |
| [`terraform-parse-service/`](terraform-parse-service/) | Production chart — composes `app` + `gateway`, adds k8s-sidecar and Terraform template ConfigMaps |

`app` and `gateway` are library-style charts used as local file dependencies by `terraform-parse-service`. They can also be deployed independently.

## Prerequisites

- Helm 3.x
- `helm-unittest` plugin (for tests)
- `ct` (chart-testing) and `yamllint` (for full lint suite)

```sh
helm plugin install https://github.com/helm-unittest/helm-unittest
```

## Running tests

```sh
# Single chart
helm unittest app/
helm unittest gateway/
helm unittest --with-subchart=false terraform-parse-service/

# All charts via ct (yamllint + helm lint + unittest)
ct lint --config ct.yaml --all
```

## Directory layout

```
helm/
├── app/                        # Generic app chart
├── gateway/                    # Istio routing chart
├── terraform-parse-service/    # Service chart (uses app + gateway)
├── ct.yaml                     # chart-testing config
└── .yamllint                   # yamllint ruleset
```
