# Unit Test Plan: Helm Charts

## Tool: helm-unittest

[helm-unittest](https://github.com/helm-unittest/helm-unittest) is the standard Helm unit testing plugin. Tests are YAML files colocated with the chart. Each test suite renders one or more templates with controlled values and asserts on the resulting YAML documents using a structured assertion DSL. No Kubernetes cluster is required.

**Why helm-unittest over alternatives:**
- Runs entirely offline — no cluster, no Tiller, no dry-run network call
- Assertions target parsed YAML paths, not regex on rendered text — survives whitespace and ordering changes
- `failedTemplate` assertion validates `required` guards without special tooling
- Plugin is maintained and widely adopted; integrates with CI with a single `helm unittest` command

---

## Prerequisites

Both tools are already installed:
- `helm-unittest` plugin — runs unit tests inside each chart's `tests/` directory
- `chart-testing` (`ct`) — the unified runner; orchestrates `helm lint`, `helm unittest`, and schema validation across all charts in the repo

---

## File Structure

Tests live in a `tests/` directory inside each chart. Complex value fixtures go in `tests/fixtures/`.

```
helm/
├── app/
│   └── tests/
│       ├── deployment_test.yaml
│       ├── service_test.yaml
│       ├── hpa_test.yaml
│       └── configmap_test.yaml
│
├── gateway/
│   └── tests/
│       ├── gateway_test.yaml
│       ├── virtualservice_test.yaml
│       └── destinationrule_test.yaml
│
└── terraform-parse-service/
    └── tests/
        ├── fixtures/
        │   └── configmaps.yaml          # complex configMaps value fixture
        ├── configmap_templates_test.yaml
        └── integration_test.yaml
```

---

## How to Run

`ct lint` is the primary runner. It discovers charts, runs `helm lint`, validates `Chart.yaml` schema, and executes `helm unittest` for any chart that has a `tests/` directory — all in one command.

```bash
# lint + unit test all changed charts (requires git context)
ct lint --config ct.yaml

# lint + unit test all charts unconditionally
ct lint --config ct.yaml --all

# single chart via ct
ct lint --config ct.yaml --charts app/
ct lint --config ct.yaml --charts gateway/
ct lint --config ct.yaml --charts terraform-parse-service/
```

`helm unittest` directly (useful for fast iteration on a single chart without lint overhead):

```bash
helm unittest app/
helm unittest gateway/
helm unittest terraform-parse-service/

# verbose
helm unittest --debug app/
```

---

## Test Suite Format

Each `_test.yaml` file is a YAML document with the following top-level keys:

```yaml
suite: <human-readable name>       # required — printed in test output
templates:                         # which template files to render for this suite
  - templates/deployment.yaml      # path relative to chart root
tests:
  - it: <test description>
    release:
      name: my-release             # override Helm release name (default: RELEASE-NAME)
    set:                           # scalar overrides — same as helm --set
      key: value
      nested.key: value
      "array[0].field": value
    values:                        # merge additional values files
      - tests/fixtures/example.yaml
    asserts:
      - <assertion>
```

**Key assertions:**

| Assertion | Purpose |
|---|---|
| `isKind: {of: Deployment}` | document is the expected kind |
| `equal: {path: x, value: y}` | exact value at YAML path |
| `matchRegex: {path: x, pattern: y}` | regex match at path |
| `isNotEmpty: {path: x}` | value at path is non-null and non-empty string |
| `isNull: {path: x}` | path is absent or null |
| `contains: {path: x, content: {...}}` | list at path contains the given object |
| `hasDocuments: {count: n}` | suite renders exactly n documents |
| `containsDocument: {kind: x, apiVersion: y}` | at least one document of this kind exists |
| `failedTemplate: {errorMessage: x}` | template rendering fails with this error |
| `documentSelector: {path: x, value: y}` | (test-level) select which document to assert on |

**Path syntax:** dot-notation for maps, bracket-notation for arrays and keys with special characters.
```
spec.template.spec.containers[0].image
metadata.labels["app.kubernetes.io/name"]
spec.metrics[1].resource.name
```

---

## `app/` Chart Tests

### `deployment_test.yaml`

Covers: default rendering, HPA-conditional replicas, probe type switch, args/env/ports, extraVolumes/Mounts, sidecars, annotations, nameOverride, fullnameOverride.

```yaml
suite: app deployment
templates:
  - templates/deployment.yaml
tests:
  - it: renders a Deployment with default values
    asserts:
      - isKind:
          of: Deployment
      - equal:
          path: metadata.name
          value: RELEASE-NAME
      - equal:
          path: spec.replicas
          value: 1
      - equal:
          path: spec.template.spec.containers[0].image
          value: ":"
      - equal:
          path: spec.template.spec.containers[0].readinessProbe.httpGet.path
          value: /
      - equal:
          path: spec.template.spec.containers[0].readinessProbe.httpGet.port
          value: 80
      - equal:
          path: spec.template.spec.containers[0].livenessProbe.httpGet.path
          value: /
      - isNull:
          path: spec.template.metadata.annotations
      - isNull:
          path: spec.template.spec.volumes
      - isNull:
          path: spec.template.spec.containers[0].args
      - isNull:
          path: spec.template.spec.containers[0].env
      - isNull:
          path: spec.template.spec.containers[0].volumeMounts

  - it: omits spec.replicas when hpa.enabled is true
    set:
      hpa.enabled: true
    asserts:
      - isNull:
          path: spec.replicas

  - it: uses tcpSocket readiness probe
    set:
      probes.type: tcpSocket
      probes.readiness.port: 8080
      probes.liveness.port: 8080
    asserts:
      - equal:
          path: spec.template.spec.containers[0].readinessProbe.tcpSocket.port
          value: 8080
      - isNull:
          path: spec.template.spec.containers[0].readinessProbe.httpGet
      - equal:
          path: spec.template.spec.containers[0].livenessProbe.tcpSocket.port
          value: 8080
      - isNull:
          path: spec.template.spec.containers[0].livenessProbe.httpGet

  - it: renders args
    set:
      args[0]: "-text=hello"
    asserts:
      - equal:
          path: spec.template.spec.containers[0].args[0]
          value: "-text=hello"

  - it: renders env vars
    set:
      env[0].name: FOO
      env[0].value: bar
    asserts:
      - equal:
          path: spec.template.spec.containers[0].env[0].name
          value: FOO
      - equal:
          path: spec.template.spec.containers[0].env[0].value
          value: bar

  - it: renders extraPorts alongside http port
    set:
      extraPorts[0].name: metrics
      extraPorts[0].containerPort: 9091
      extraPorts[0].protocol: TCP
    asserts:
      - equal:
          path: spec.template.spec.containers[0].ports[0].name
          value: http
      - equal:
          path: spec.template.spec.containers[0].ports[1].name
          value: metrics
      - equal:
          path: spec.template.spec.containers[0].ports[1].containerPort
          value: 9091

  - it: renders extraVolumes and extraVolumeMounts
    set:
      extraVolumes[0].name: my-vol
      extraVolumes[0].emptyDir: {}
      extraVolumeMounts[0].name: my-vol
      extraVolumeMounts[0].mountPath: /data
    asserts:
      - contains:
          path: spec.template.spec.volumes
          content:
            name: my-vol
            emptyDir: {}
      - contains:
          path: spec.template.spec.containers[0].volumeMounts
          content:
            name: my-vol
            mountPath: /data

  - it: appends sidecars after the main container
    set:
      sidecars[0].name: my-sidecar
      sidecars[0].image: busybox:latest
    asserts:
      - equal:
          path: spec.template.spec.containers[0].name
          value: app
      - equal:
          path: spec.template.spec.containers[1].name
          value: my-sidecar

  - it: renders podAnnotations
    set:
      podAnnotations["prometheus.io/scrape"]: "true"
      podAnnotations["prometheus.io/port"]: "9091"
    asserts:
      - equal:
          path: spec.template.metadata.annotations["prometheus.io/scrape"]
          value: "true"
      - equal:
          path: spec.template.metadata.annotations["prometheus.io/port"]
          value: "9091"

  - it: renders configMap checksum annotation
    values:
      - tests/fixtures/configmaps.yaml
    asserts:
      - matchRegex:
          path: spec.template.metadata.annotations["checksum/config"]
          pattern: "^[a-f0-9]{64}$"

  - it: uses nameOverride in selectorLabels
    set:
      nameOverride: my-service
    asserts:
      - equal:
          path: spec.selector.matchLabels["app.kubernetes.io/name"]
          value: my-service
      - equal:
          path: spec.template.metadata.labels["app.kubernetes.io/name"]
          value: my-service

  - it: uses fullnameOverride for resource name
    set:
      fullnameOverride: custom-name
    asserts:
      - equal:
          path: metadata.name
          value: custom-name

  - it: uses release name as resource name
    release:
      name: myapp
    asserts:
      - equal:
          path: metadata.name
          value: myapp

  - it: selectorLabels are consistent between matchLabels and pod template
    release:
      name: myapp
    asserts:
      - equal:
          path: spec.selector.matchLabels["app.kubernetes.io/instance"]
          value: myapp
      - equal:
          path: spec.template.metadata.labels["app.kubernetes.io/instance"]
          value: myapp
```

### `service_test.yaml`

Covers: resource rendering, port config, targetPort name, selector consistency with deployment.

```yaml
suite: app service
templates:
  - templates/service.yaml
tests:
  - it: renders a Service with default values
    asserts:
      - isKind:
          of: Service
      - equal:
          path: metadata.name
          value: RELEASE-NAME
      - equal:
          path: spec.type
          value: ClusterIP
      - equal:
          path: spec.ports[0].port
          value: 80
      - equal:
          path: spec.ports[0].targetPort
          value: http
      - equal:
          path: spec.ports[0].protocol
          value: TCP
      - equal:
          path: spec.ports[0].name
          value: http

  - it: uses custom service port
    set:
      service.port: 8080
    asserts:
      - equal:
          path: spec.ports[0].port
          value: 8080

  - it: uses custom service type
    set:
      service.type: NodePort
    asserts:
      - equal:
          path: spec.type
          value: NodePort

  - it: selector includes instance label matching release name
    release:
      name: myapp
    asserts:
      - equal:
          path: spec.selector["app.kubernetes.io/instance"]
          value: myapp

  - it: selector app name matches nameOverride
    set:
      nameOverride: my-service
    asserts:
      - equal:
          path: spec.selector["app.kubernetes.io/name"]
          value: my-service
```

### `hpa_test.yaml`

Covers: disabled by default, CPU-only scaling, memory metric opt-in, scaleTargetRef name.

```yaml
suite: app hpa
templates:
  - templates/hpa.yaml
tests:
  - it: does not render when hpa.enabled is false
    asserts:
      - hasDocuments:
          count: 0

  - it: renders an HPA when enabled
    set:
      hpa.enabled: true
    asserts:
      - hasDocuments:
          count: 1
      - isKind:
          of: HorizontalPodAutoscaler
      - equal:
          path: apiVersion
          value: autoscaling/v2
      - equal:
          path: metadata.name
          value: RELEASE-NAME

  - it: sets minReplicas and maxReplicas from values
    set:
      hpa.enabled: true
      hpa.minReplicas: 2
      hpa.maxReplicas: 8
    asserts:
      - equal:
          path: spec.minReplicas
          value: 2
      - equal:
          path: spec.maxReplicas
          value: 8

  - it: includes CPU metric with correct utilization
    set:
      hpa.enabled: true
      hpa.targetCPUUtilizationPercentage: 60
    asserts:
      - equal:
          path: spec.metrics[0].resource.name
          value: cpu
      - equal:
          path: spec.metrics[0].resource.target.averageUtilization
          value: 60

  - it: does not include memory metric when targetMemoryUtilizationPercentage is 0
    set:
      hpa.enabled: true
      hpa.targetMemoryUtilizationPercentage: 0
    asserts:
      - isNull:
          path: spec.metrics[1]

  - it: includes memory metric when targetMemoryUtilizationPercentage is non-zero
    set:
      hpa.enabled: true
      hpa.targetMemoryUtilizationPercentage: 80
    asserts:
      - equal:
          path: spec.metrics[1].resource.name
          value: memory
      - equal:
          path: spec.metrics[1].resource.target.type
          value: Utilization
      - equal:
          path: spec.metrics[1].resource.target.averageUtilization
          value: 80

  - it: scaleTargetRef points to the Deployment by release name
    set:
      hpa.enabled: true
    release:
      name: myapp
    asserts:
      - equal:
          path: spec.scaleTargetRef.apiVersion
          value: apps/v1
      - equal:
          path: spec.scaleTargetRef.kind
          value: Deployment
      - equal:
          path: spec.scaleTargetRef.name
          value: myapp
```

### `configmap_test.yaml`

Covers: empty list renders nothing, single CM with correct name and data, multiple CMs produce multiple documents.

The fixture `tests/fixtures/configmaps.yaml` is used for complex nested `configMaps` values:

```yaml
# app/tests/fixtures/configmaps.yaml
configMaps:
  - name: config
    data:
      config.yaml:
        key: value
        nested:
          field: example
```

Test file:

```yaml
suite: app configmap
templates:
  - templates/configmap.yaml
tests:
  - it: renders nothing when configMaps is empty
    asserts:
      - hasDocuments:
          count: 0

  - it: renders one ConfigMap
    values:
      - tests/fixtures/configmaps.yaml
    asserts:
      - hasDocuments:
          count: 1
      - isKind:
          of: ConfigMap
      - equal:
          path: metadata.name
          value: RELEASE-NAME-config
      - isNotEmpty:
          path: data["config.yaml"]

  - it: includes standard labels
    values:
      - tests/fixtures/configmaps.yaml
    asserts:
      - isNotEmpty:
          path: metadata.labels["helm.sh/chart"]
      - equal:
          path: metadata.labels["app.kubernetes.io/managed-by"]
          value: Helm

  - it: renders two ConfigMaps for two entries
    values:
      - tests/fixtures/two-configmaps.yaml
    asserts:
      - hasDocuments:
          count: 2
```

Add a second fixture `tests/fixtures/two-configmaps.yaml`:

```yaml
# app/tests/fixtures/two-configmaps.yaml
configMaps:
  - name: config
    data:
      config.yaml:
        key: value
  - name: extra
    data:
      extra.yaml: |
        plain: text
```

---

## `gateway/` Chart Tests

### `gateway_test.yaml`

Covers: double-guard (enabled AND create), port/hosts/selector rendering.

```yaml
suite: gateway istio gateway resource
templates:
  - templates/gateway.yaml
tests:
  - it: does not render when istio.enabled is false
    asserts:
      - hasDocuments:
          count: 0

  - it: does not render when istio.enabled is true but gateway.create is false
    set:
      istio.enabled: true
      istio.gateway.create: false
    asserts:
      - hasDocuments:
          count: 0

  - it: renders a Gateway when both enabled and create are true
    set:
      istio.enabled: true
      istio.gateway.create: true
      istio.gateway.port: 80
      istio.gateway.hosts[0]: "example.com"
    asserts:
      - hasDocuments:
          count: 1
      - isKind:
          of: Gateway
      - equal:
          path: metadata.name
          value: RELEASE-NAME
      - equal:
          path: spec.selector.istio
          value: ingressgateway
      - equal:
          path: spec.servers[0].port.number
          value: 80
      - equal:
          path: spec.servers[0].port.name
          value: http
      - equal:
          path: spec.servers[0].port.protocol
          value: HTTP
      - equal:
          path: spec.servers[0].hosts[0]
          value: "example.com"

  - it: uses custom port
    set:
      istio.enabled: true
      istio.gateway.create: true
      istio.gateway.port: 443
      istio.gateway.hosts[0]: "example.com"
    asserts:
      - equal:
          path: spec.servers[0].port.number
          value: 443

  - it: uses release name as Gateway name
    release:
      name: my-release
    set:
      istio.enabled: true
      istio.gateway.create: true
      istio.gateway.hosts[0]: "example.com"
    asserts:
      - equal:
          path: metadata.name
          value: my-release
```

### `virtualservice_test.yaml`

Covers: double-guard, gateway reference (create vs existing), routes rendered, required guard failure.

```yaml
suite: gateway virtualservice
templates:
  - templates/virtualservice.yaml
tests:
  - it: does not render when istio.enabled is false
    asserts:
      - hasDocuments:
          count: 0

  - it: does not render when virtualService.enabled is false
    set:
      istio.enabled: true
      istio.virtualService.enabled: false
    asserts:
      - hasDocuments:
          count: 0

  - it: renders a VirtualService when both flags are true
    set:
      istio.enabled: true
      istio.gateway.create: true
      istio.virtualService.enabled: true
      istio.virtualService.hosts[0]: "example.com"
      istio.virtualService.routes[0].route[0].destination.host: my-service
      istio.virtualService.routes[0].route[0].destination.port.number: 8080
    asserts:
      - hasDocuments:
          count: 1
      - isKind:
          of: VirtualService
      - equal:
          path: metadata.name
          value: RELEASE-NAME
      - equal:
          path: spec.hosts[0]
          value: "example.com"
      - isNotEmpty:
          path: spec.http

  - it: references the release-owned gateway when gateway.create is true
    release:
      name: my-release
    set:
      istio.enabled: true
      istio.gateway.create: true
      istio.virtualService.enabled: true
      istio.virtualService.hosts[0]: "example.com"
      istio.virtualService.routes[0].route[0].destination.host: my-service
      istio.virtualService.routes[0].route[0].destination.port.number: 80
    asserts:
      - equal:
          path: spec.gateways[0]
          value: my-release

  - it: references an existing gateway when gateway.create is false
    set:
      istio.enabled: true
      istio.gateway.create: false
      istio.gateway.name: "my-existing-gateway"
      istio.virtualService.enabled: true
      istio.virtualService.hosts[0]: "example.com"
      istio.virtualService.routes[0].route[0].destination.host: my-service
      istio.virtualService.routes[0].route[0].destination.port.number: 80
    asserts:
      - equal:
          path: spec.gateways[0]
          value: my-existing-gateway

  - it: fails when gateway.create is false and gateway.name is not set
    set:
      istio.enabled: true
      istio.gateway.create: false
      istio.gateway.name: ""
      istio.virtualService.enabled: true
      istio.virtualService.hosts[0]: "example.com"
      istio.virtualService.routes[0].route[0].destination.host: my-service
      istio.virtualService.routes[0].route[0].destination.port.number: 80
    asserts:
      - failedTemplate:
          errorMessage: "istio.gateway.name is required when gateway.create is false"
```

### `destinationrule_test.yaml`

Covers: double-guard, host rendering, trafficPolicy opt-in, required guard for missing host.

```yaml
suite: gateway destinationrule
templates:
  - templates/destinationrule.yaml
tests:
  - it: does not render when istio.enabled is false
    asserts:
      - hasDocuments:
          count: 0

  - it: does not render when destinationRule.enabled is false
    set:
      istio.enabled: true
      istio.destinationRule.enabled: false
    asserts:
      - hasDocuments:
          count: 0

  - it: renders a DestinationRule with host
    set:
      istio.enabled: true
      istio.destinationRule.enabled: true
      istio.destinationRule.host: my-service
    asserts:
      - hasDocuments:
          count: 1
      - isKind:
          of: DestinationRule
      - equal:
          path: metadata.name
          value: RELEASE-NAME
      - equal:
          path: spec.host
          value: my-service

  - it: omits trafficPolicy when empty
    set:
      istio.enabled: true
      istio.destinationRule.enabled: true
      istio.destinationRule.host: my-service
    asserts:
      - isNull:
          path: spec.trafficPolicy

  - it: renders trafficPolicy when set
    set:
      istio.enabled: true
      istio.destinationRule.enabled: true
      istio.destinationRule.host: my-service
      istio.destinationRule.trafficPolicy.connectionPool.http.http1MaxPendingRequests: 1024
    asserts:
      - isNotEmpty:
          path: spec.trafficPolicy
      - equal:
          path: spec.trafficPolicy.connectionPool.http.http1MaxPendingRequests
          value: 1024

  - it: fails when host is not provided
    set:
      istio.enabled: true
      istio.destinationRule.enabled: true
      istio.destinationRule.host: ""
    asserts:
      - failedTemplate:
          errorMessage: "istio.destinationRule.host is required"
```

---

## `terraform-parse-service/` Tests

The parent chart's own template is `templates/configmap-templates.yaml`. It uses `include "app.fullname"` and `include "app.labels"` from the packed `app` subchart — these named templates are available globally during render.

Integration tests exercise the full render (app + gateway subcharts + parent templates) and assert on specific document properties using `documentSelector`.

### `configmap_templates_test.yaml`

Covers: empty `tfTemplates`, one CM per entry, slug derivation from path, label presence, data key equals relative path, content populated via `.Files.Get`.

```yaml
suite: terraform-parse-service configmap templates
templates:
  - templates/configmap-templates.yaml
tests:
  - it: renders nothing when tfTemplates is empty
    set:
      tfTemplates: []
    asserts:
      - hasDocuments:
          count: 0

  - it: renders one ConfigMap for a single tfTemplates entry
    set:
      tfTemplates[0]: aws/s3/bucket.tf.tmpl
    asserts:
      - hasDocuments:
          count: 1
      - isKind:
          of: ConfigMap

  - it: CM name ends with the path-derived slug
    set:
      tfTemplates[0]: aws/s3/bucket.tf.tmpl
    release:
      name: terraform-parse-service
    asserts:
      - equal:
          path: metadata.name
          value: terraform-parse-service-aws-s3-bucket-tf-tmpl-tmpl

  - it: CM carries the terraform-parse-service/template label
    set:
      tfTemplates[0]: aws/s3/bucket.tf.tmpl
    asserts:
      - equal:
          path: metadata.labels["terraform-parse-service/template"]
          value: "true"

  - it: CM data key equals the relative path
    set:
      tfTemplates[0]: aws/s3/bucket.tf.tmpl
    asserts:
      - isNotEmpty:
          path: data["aws/s3/bucket.tf.tmpl"]

  - it: CM data content is non-empty (file read via .Files.Get)
    set:
      tfTemplates[0]: aws/s3/bucket.tf.tmpl
    asserts:
      - isNotEmpty:
          path: data["aws/s3/bucket.tf.tmpl"]

  - it: renders two ConfigMaps for two tfTemplates entries
    set:
      tfTemplates[0]: aws/s3/bucket.tf.tmpl
      tfTemplates[1]: aws/s3/rds.tf.tmpl
    asserts:
      - hasDocuments:
          count: 2

  - it: derives correct slug for rds template
    set:
      tfTemplates[0]: aws/s3/rds.tf.tmpl
    release:
      name: terraform-parse-service
    asserts:
      - equal:
          path: metadata.name
          value: terraform-parse-service-aws-s3-rds-tf-tmpl-tmpl
```

### `integration_test.yaml`

Covers: full render with base values produces all expected resource kinds, Deployment has the k8s-sidecar container, emptyDir volumes are mounted, tcpSocket probes are used, HPA is absent by default.

`documentSelector` is used to target specific documents from the full render without specifying `templates:`.

```yaml
suite: terraform-parse-service integration
values:
  - values.yaml
tests:
  - it: renders Deployment, Service, ConfigMap, Gateway, and VirtualService
    asserts:
      - containsDocument:
          kind: Deployment
          apiVersion: apps/v1
      - containsDocument:
          kind: Service
          apiVersion: v1
      - containsDocument:
          kind: ConfigMap
          apiVersion: v1
      - containsDocument:
          kind: Gateway
          apiVersion: networking.istio.io/v1beta1
      - containsDocument:
          kind: VirtualService
          apiVersion: networking.istio.io/v1beta1

  - it: does not render an HPA with default values
    asserts:
      - notContainsDocument:
          kind: HorizontalPodAutoscaler

  - it: deployment has k8s-sidecar as a second container
    documentSelector:
      path: kind
      value: Deployment
    asserts:
      - equal:
          path: spec.template.spec.containers[1].name
          value: k8s-sidecar
      - equal:
          path: spec.template.spec.containers[1].image
          value: kiwigrid/k8s-sidecar:1.27.5

  - it: deployment has templates emptyDir volume
    documentSelector:
      path: kind
      value: Deployment
    asserts:
      - contains:
          path: spec.template.spec.volumes
          content:
            name: templates
            emptyDir: {}

  - it: deployment has output emptyDir volume
    documentSelector:
      path: kind
      value: Deployment
    asserts:
      - contains:
          path: spec.template.spec.volumes
          content:
            name: output
            emptyDir: {}

  - it: deployment uses tcpSocket readiness probe
    documentSelector:
      path: kind
      value: Deployment
    asserts:
      - isNotNull:
          path: spec.template.spec.containers[0].readinessProbe.tcpSocket
      - isNull:
          path: spec.template.spec.containers[0].readinessProbe.httpGet
      - equal:
          path: spec.template.spec.containers[0].readinessProbe.tcpSocket.port
          value: 8080

  - it: deployment uses tcpSocket liveness probe
    documentSelector:
      path: kind
      value: Deployment
    asserts:
      - isNotNull:
          path: spec.template.spec.containers[0].livenessProbe.tcpSocket
      - isNull:
          path: spec.template.spec.containers[0].livenessProbe.httpGet

  - it: app-config volume mount is read-only
    documentSelector:
      path: kind
      value: Deployment
    asserts:
      - contains:
          path: spec.template.spec.containers[0].volumeMounts
          content:
            name: app-config
            mountPath: /configs
            readOnly: true

  - it: k8s-sidecar watches the correct label
    documentSelector:
      path: kind
      value: Deployment
    asserts:
      - equal:
          path: spec.template.spec.containers[1].env[0].name
          value: LABEL
      - equal:
          path: spec.template.spec.containers[1].env[0].value
          value: "terraform-parse-service/template"
      - equal:
          path: spec.template.spec.containers[1].env[2].name
          value: FOLDER
      - equal:
          path: spec.template.spec.containers[1].env[2].value
          value: /templates

  - it: config ConfigMap carries checksum annotation in pod template
    documentSelector:
      path: kind
      value: Deployment
    asserts:
      - matchRegex:
          path: spec.template.metadata.annotations["checksum/config"]
          pattern: "^[a-f0-9]{64}$"

  - it: renders HPA when enabled via overlay
    values:
      - values.yaml
      - values-staging.yaml
    asserts:
      - containsDocument:
          kind: HorizontalPodAutoscaler
          apiVersion: autoscaling/v2
```

---

## Fixture File: `app/tests/fixtures/configmaps.yaml`

Referenced by `configmap_test.yaml` to avoid complex `set` notation for nested map values.

```yaml
# app/tests/fixtures/configmaps.yaml
configMaps:
  - name: config
    data:
      config.yaml:
        key: value
        nested:
          field: example
```

```yaml
# app/tests/fixtures/two-configmaps.yaml
configMaps:
  - name: config
    data:
      config.yaml:
        key: value
  - name: extra
    data:
      extra.yaml: |
        plain: text
```

---

## chart-testing (`ct`) Configuration

`ct` requires a config file and a yamllint config. Both live in `helm/`. Run `ct` from the `helm/` directory.

### `helm/ct.yaml`

```yaml
# See https://github.com/helm/chart-testing#configuration
remote: origin
target-branch: main
chart-dirs:
  - .
helm-extra-args: --timeout 600s
lint-conf: .yamllint
validate-maintainers: false
validate-chart-schema: true
check-version-increment: false
additional-commands:
  - sh -ec "if [[ -d '{{ .Path }}/tests' ]]; then helm unittest --strict --debug --file 'tests/*.yaml' --file 'tests/**/*.yaml' {{ .Path }}; else true; fi"
```

**Key settings:**

- `target-branch: main` — `ct lint` (without `--all`) only tests charts that differ from this branch; matches the repo's main branch name.
- `chart-dirs: [.]` — discovers charts by finding directories with `Chart.yaml`. Scans the entire `helm/` tree, picks up `app/`, `gateway/`, `terraform-parse-service/`; ignores `frontend/` and `backend/` (no `Chart.yaml`).
- `lint-conf: .yamllint` — path to yamllint config, relative to the directory `ct` is run from (`helm/`).
- `validate-maintainers: false` — `Chart.yaml` files don't declare maintainers; `true` would fail all charts.
- `validate-chart-schema: true` — enforces `Chart.yaml` fields against the Helm JSON schema.
- `check-version-increment: false` — chart version bumping is not enforced in this repo's workflow.
- `additional-commands` — runs `helm unittest --strict --debug` for charts that have a `tests/` directory; exits `0` cleanly for charts that don't yet have tests. The `--strict` flag makes any warning a failure. `--file` globs cover both flat and nested fixture files.

### `helm/.yamllint`

yamllint is run by `ct` against all values files and templates. The config enables only the rules that catch real bugs; cosmetic rules are disabled to avoid noise on Helm template files.

```yaml
extends: default
rules:
  key-duplicates: enable
  truthy:
    check-keys: false

  braces: disable
  brackets: disable
  comments: disable
  comments-indentation: disable
  document-start: disable
  empty-lines: disable
  empty-values: disable
  float-values: disable
  indentation: disable
  line-length: disable
  new-line-at-end-of-file: disable
  new-lines: disable
  trailing-spaces: disable
```

**Active rules:**
- `key-duplicates` — catches duplicate YAML keys, which silently overwrite each other and cause hard-to-debug value issues.
- `truthy: check-keys: false` — prevents false positives on Helm boolean values (`enabled: true`) which yamllint would otherwise flag as non-boolean strings.

All other rules are disabled: indentation, line length, braces, and comment style vary across Helm templates and values files without indicating bugs.

**Running from the `helm/` directory:**

```bash
# test only charts changed vs main branch
ct lint --config ct.yaml

# test all charts unconditionally
ct lint --config ct.yaml --all

# single chart
ct lint --config ct.yaml --charts app/
```

**`ct lint` execution order per chart:**

1. `helm lint <chart>` with the chart's base `values.yaml`
2. `helm lint <chart>` with each `ci/` or per-environment values file (`values-*.yaml`) found in the chart directory
3. `additional-commands` block — runs `helm unittest --strict --debug` if `tests/` exists

**File tree addition:**

```
helm/
├── ct.yaml
└── .yamllint
```

---

## `.gitignore` Addition

Add to `helm/.gitignore` (already ignores chart tarballs):

```
# helm-unittest snapshots (if snapshot testing is added later)
**/__snapshot__/
```

---

## TODO

### Phase 0 — Setup

- [x] Create `helm/ct.yaml` — `remote: origin`, `target-branch: main`, `chart-dirs: [.]`, `helm-extra-args: --timeout 600s`, `lint-conf: .yamllint`, `validate-maintainers: false`, `validate-chart-schema: true`, `check-version-increment: false`, `additional-commands` with `helm unittest --strict --debug --file 'tests/*.yaml' --file 'tests/**/*.yaml'` and no-tests-dir fallback (`else true`)
- [x] Create `helm/.yamllint` — `extends: default`, `key-duplicates: enable`, `truthy: check-keys: false`, all cosmetic rules disabled (`braces`, `brackets`, `comments`, `comments-indentation`, `document-start`, `empty-lines`, `empty-values`, `float-values`, `indentation`, `line-length`, `new-line-at-end-of-file`, `new-lines`, `trailing-spaces`)
- [x] Add `**/__snapshot__/` to `helm/.gitignore`

### Phase 1 — `app/` test fixtures

- [x] Create `app/tests/fixtures/configmaps.yaml` — single `configMaps` entry with `name: config`, `data.config.yaml` as a nested map
- [x] Create `app/tests/fixtures/two-configmaps.yaml` — two `configMaps` entries (`config` and `extra`) to exercise multi-document rendering

### Phase 2 — `app/` test suites

- [x] Create `app/tests/deployment_test.yaml` with the following cases:
  - [x] Default values render a Deployment with `replicas: 1`, no annotations, no volumes, no args, no env, no volumeMounts
  - [x] `hpa.enabled: true` omits `spec.replicas`
  - [x] `probes.type: tcpSocket` renders `tcpSocket` probes and omits `httpGet`
  - [x] `args` renders in container args
  - [x] `env` renders in container env
  - [x] `extraPorts` renders a second port entry after the `http` port
  - [x] `extraVolumes` and `extraVolumeMounts` render in pod spec
  - [x] `sidecars` appends a second container after the main container
  - [x] `podAnnotations` renders in pod template annotations
  - [x] `configMaps` entry produces a `checksum/<name>` annotation matching `[a-f0-9]{64}`
  - [x] `nameOverride` is reflected in `matchLabels` and pod template labels
  - [x] `fullnameOverride` is used as the Deployment name
  - [x] Release name is used as Deployment name when no overrides are set
  - [x] `matchLabels` and pod template `labels` share the same `instance` value
- [x] Create `app/tests/service_test.yaml` with the following cases:
  - [x] Default values render a Service with `ClusterIP`, port `80`, `targetPort: http`
  - [x] `service.port` override is reflected in `spec.ports[0].port`
  - [x] `service.type` override is reflected in `spec.type`
  - [x] `spec.selector` contains release name as `instance` label
  - [x] `nameOverride` is reflected in `spec.selector["app.kubernetes.io/name"]`
- [x] Create `app/tests/hpa_test.yaml` with the following cases:
  - [x] `hpa.enabled: false` (default) renders zero documents
  - [x] `hpa.enabled: true` renders one HPA document with `apiVersion: autoscaling/v2`
  - [x] `hpa.minReplicas` and `hpa.maxReplicas` values appear in `spec`
  - [x] CPU metric appears with correct `averageUtilization`
  - [x] `targetMemoryUtilizationPercentage: 0` results in no second metric entry
  - [x] `targetMemoryUtilizationPercentage: 80` adds a `memory` resource metric
  - [x] `scaleTargetRef` references the correct Deployment name, kind, and apiVersion
- [x] Create `app/tests/configmap_test.yaml` with the following cases:
  - [x] Empty `configMaps` list renders zero documents
  - [x] Single entry (via fixture) renders one ConfigMap with name `<release>-config`
  - [x] ConfigMap carries standard `helm.sh/chart` and `app.kubernetes.io/managed-by` labels
  - [x] Two entries (via fixture) render two documents
- [x] Run `helm unittest app/` — all cases pass

### Phase 3 — `gateway/` test suites

- [x] Create `gateway/tests/gateway_test.yaml` with the following cases:
  - [x] `istio.enabled: false` (default) renders zero documents
  - [x] `istio.enabled: true` + `gateway.create: false` renders zero documents
  - [x] Both flags `true` renders one `Gateway` document
  - [x] `spec.selector.istio: ingressgateway` is present
  - [x] `spec.servers[0].port.number` matches `istio.gateway.port`
  - [x] `spec.servers[0].hosts` matches `istio.gateway.hosts`
  - [x] `metadata.name` equals the release name
- [x] Create `gateway/tests/virtualservice_test.yaml` with the following cases:
  - [x] `istio.enabled: false` renders zero documents
  - [x] `istio.enabled: true` + `virtualService.enabled: false` renders zero documents
  - [x] Both flags `true` renders one `VirtualService` document
  - [x] `spec.hosts` matches `istio.virtualService.hosts`
  - [x] `spec.gateways[0]` equals release name when `gateway.create: true`
  - [x] `spec.gateways[0]` equals `istio.gateway.name` when `gateway.create: false`
  - [x] Rendering fails with `"istio.gateway.name is required"` when `gateway.create: false` and `gateway.name: ""`
- [x] Create `gateway/tests/destinationrule_test.yaml` with the following cases:
  - [x] `istio.enabled: false` renders zero documents
  - [x] `istio.enabled: true` + `destinationRule.enabled: false` renders zero documents
  - [x] Both flags `true` renders one `DestinationRule` document
  - [x] `spec.host` equals `istio.destinationRule.host`
  - [x] Empty `trafficPolicy` results in `spec.trafficPolicy` being absent
  - [x] Non-empty `trafficPolicy` value is present at `spec.trafficPolicy`
  - [x] Rendering fails with `"istio.destinationRule.host is required"` when `host: ""`
- [x] Run `helm unittest gateway/` — all cases pass

### Phase 4 — `terraform-parse-service/` test suites

- [x] Create `terraform-parse-service/tests/configmap_templates_test.yaml` with the following cases:
  - [x] `tfTemplates: []` renders zero documents
  - [x] Single entry `aws/s3/bucket.tf.tmpl` renders one ConfigMap
  - [x] CM name equals `<release>-aws-s3-bucket-tf-tmpl-tmpl`
  - [x] CM carries label `terraform-parse-service/template: "true"`
  - [x] CM data key equals the relative path `aws/s3/bucket.tf.tmpl`
  - [x] CM data value is non-empty (file content read via `.Files.Get`)
  - [x] Two entries render two documents
  - [x] `aws/s3/rds.tf.tmpl` produces CM name `<release>-aws-s3-rds-tf-tmpl-tmpl`
- [x] Create `terraform-parse-service/tests/integration_test.yaml` with the following cases:
  - [x] Base `values.yaml` renders Deployment, Service, ConfigMap, Gateway, VirtualService
  - [x] No HPA rendered with base values (`hpa.enabled: false`)
  - [x] Deployment `spec.containers[1].name` equals `k8s-sidecar`
  - [x] Deployment `spec.containers[1].image` equals `kiwigrid/k8s-sidecar:1.27.5`
  - [x] Pod spec includes `templates` `emptyDir` volume
  - [x] Pod spec includes `output` `emptyDir` volume
  - [x] `readinessProbe` is `tcpSocket` on port `8080` with no `httpGet`
  - [x] `livenessProbe` is `tcpSocket` with no `httpGet`
  - [x] `app-config` volumeMount has `readOnly: true` at `/configs`
  - [x] k8s-sidecar `LABEL` env var equals `terraform-parse-service/template`
  - [x] k8s-sidecar `FOLDER` env var equals `/templates`
  - [x] Pod template has `checksum/config` annotation matching `[a-f0-9]{64}`
  - [x] HPA rendered when `values-staging.yaml` overlay is applied
- [x] Run `helm unittest terraform-parse-service/` — all cases pass

### Phase 5 — Full suite via `ct`

- [x] Run `ct lint --config ct.yaml --all` from `helm/` — yamllint, helm lint (base + all env overlays), and helm unittest pass for all three charts with 0 failures
