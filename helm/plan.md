# Refactor Plan: `tripla-apps` Helm Chart

## Problem Statement

The chart has three classes of problems that must be addressed together:

1. **Correctness bugs** — the chart cannot be installed as-is and will not route traffic even if the parse errors are ignored.
2. **Design flaws** — `values.yaml` is inert (no template interpolation), labels are literal strings with drift, no shared helpers.
3. **Operational gaps** — no resource limits, no probes, no per-environment overrides, mutable image tags.

The refactor goal: a chart that installs cleanly, routes correctly, supports `dev`/`staging`/`prod` environments via value overrides, and is safe to operate (no silent traffic drops, no unbounded resource consumption, HPA that actually works).

---

## Architecture

`app/` is a self-contained Helm application chart with real rendered templates (Deployment, Service, HPA, ConfigMap). It is generic enough to deploy any HTTP service by configuration alone. It has no Istio resources — Istio routing is handled by the separate `gateway/` chart.

`gateway/` is an optional standalone chart that owns all Istio resources (Gateway, VirtualService, DestinationRule). Services declare it as a Helm dependency in their own `Chart.yaml` when they need Istio routing. This decoupling means `app/` works identically with or without a service mesh. Weighted canary and A/B traffic splitting live here — `app/` has no concept of them. Label/selector matching fields in `gateway/` are evaluated via `tpl` so callers can inject values from a `global:` block or shared parent chart without duplicating strings.

`frontend/` and `backend/` are **values-only directories** — no `Chart.yaml`, no templates. Each contains a base `values.yaml` plus per-environment overlay files. They are installed by pointing Helm at the `app/` chart.

`terraform-parse-service/` is a **proper Helm chart** with its own `Chart.yaml` and `templates/`. It declares `app` and `gateway` as dependencies (Helm subcharts). Service config is written directly as a literal YAML string inside `app.configMaps[].data` — rendered into a ConfigMap by `app/templates/configmap.yaml`. Each env overlay replaces the full data block. Terraform template files live in `files/templates/` on disk (source of truth); the single custom template (`configmap-templates.yaml`) reads each file via `.Files.Get` for filenames explicitly listed in `tfTemplates`.

**Install pattern:**
```bash
# frontend/backend — point at the app chart, layer service values on top
helm upgrade --install frontend app/ \
  -f frontend/values.yaml -f frontend/values-dev.yaml \
  -n dev --create-namespace

# terraform-parse-service — install its own chart (which pulls in app + gateway as subcharts)
helm dep update terraform-parse-service/
helm upgrade --install terraform-parse-service terraform-parse-service/ \
  -f terraform-parse-service/values-dev.yaml \
  -n dev --create-namespace

# frontend with Istio — install its own chart (app + gateway as subcharts)
helm dep update frontend-chart/
helm upgrade --install frontend frontend-chart/ \
  -f frontend-chart/values.yaml -f frontend-chart/values-dev.yaml \
  -n dev --create-namespace

# OR: if using gateway as a top-level standalone install, pointed at the app chart output
# (gateway/ chart is a dependency; services pull it in, not standalone in most cases)
```

**Subchart values scoping for terraform-parse-service:**
When `app` is a Helm dependency of `terraform-parse-service`, Helm routes values to the subchart using a top-level key matching the dependency name. All `app` chart configuration lives under `app:` in `terraform-parse-service/values.yaml`. This is standard Helm behavior — there is no workaround.

**Final file tree:**
```
helm/
├── app/                                   # self-contained generic chart — no Istio
│   ├── Chart.yaml
│   ├── values.yaml                        # schema + safe defaults
│   └── templates/
│       ├── _helpers.tpl                   # fullname, labels, selectorLabels (tpl-rendered)
│       ├── deployment.yaml                # extraVolumes/Mounts via tpl
│       ├── service.yaml
│       ├── hpa.yaml                       # cpu + memory metrics
│       └── configmap.yaml                 # generic CM rendered via tpl
│
├── gateway/                               # optional Istio chart — declared as dep by services that need it
│   ├── Chart.yaml
│   ├── values.yaml                        # istio block schema + safe defaults
│   └── templates/
│       ├── _helpers.tpl                   # inherits/mirrors app helpers; tpl for label fields
│       ├── gateway.yaml                   # Istio Gateway (conditional)
│       ├── virtualservice.yaml            # Istio VirtualService (conditional)
│       └── destinationrule.yaml           # Istio DestinationRule (conditional)
│
├── frontend/                              # values only — no chart
│   ├── values.yaml
│   ├── values-dev.yaml
│   ├── values-staging.yaml
│   └── values-prod.yaml
│
├── backend/                               # values only — no chart
│   ├── values.yaml
│   ├── values-dev.yaml
│   ├── values-staging.yaml
│   └── values-prod.yaml
│
└── terraform-parse-service/              # proper chart — depends on app + gateway
    ├── Chart.yaml                         # type: application, deps: ../app, ../gateway
    ├── charts/                            # app chart unpacked by helm dep update
    ├── files/
    │   └── templates/
    │       └── aws/s3/
    │           ├── bucket.tf.tmpl         # source of truth on disk; read via .Files.Get
    │           └── rds.tf.tmpl            # (example) not active until listed in values
    ├── values.yaml                        # app: { config, configMaps, ... } + tfTemplates (allowlist)
    ├── values-dev.yaml
    ├── values-staging.yaml
    ├── values-prod.yaml
    └── templates/
        └── configmap-templates.yaml       # CM: .Files.Get per filename listed in tfTemplates
```

---

## Step 1 — Create `app/Chart.yaml`

```yaml
# app/Chart.yaml
apiVersion: v2
name: app
description: Generic self-contained chart — Deployment, Service, HPA, ConfigMap
type: application
version: 0.1.0
appVersion: "0.1.0"
```

---

## Step 2 — Create `app/values.yaml`

Full schema with safe defaults. Every field used in any template must appear here.

Notable additions over the previous revision:
- `nameOverride` — overrides the name component used in `selectorLabels`; evaluated via `tpl` so it can reference `{{ .Release.Name }}` or `{{ .Values.global.name }}` without duplicating the string
- `hpa.targetMemoryUtilizationPercentage` — memory metric for HPA; `0` disables it
- `extraVolumes` / `extraVolumeMounts` — arbitrary volume injection; values support Go template expressions rendered via `tpl`
- `configMaps` — list of ConfigMaps to render inline via `tpl`; useful for frontend/backend if they ever need one
- `sidecars` — list of additional containers to inject alongside the main container; values rendered via `tpl`
- Istio block removed — moved to `gateway/` chart

```yaml
# app/values.yaml
replicaCount: 1

nameOverride: ""   # overrides the name segment in selectorLabels; supports tpl expressions
                   # e.g. '{{ .Values.global.name }}' — evaluated via tpl at render time
                   # when empty, defaults to .Chart.Name

image:
  repository: ""
  tag: ""
  pullPolicy: IfNotPresent

args: []

env: []
# env:
#   - name: APP_ENV
#     value: production

podAnnotations: {}
# podAnnotations:
#   prometheus.io/scrape: "true"
#   prometheus.io/port: "9091"

service:
  type: ClusterIP
  port: 80

extraPorts: []
# extraPorts:
#   - name: metrics
#     containerPort: 9091
#     protocol: TCP

extraVolumes: []
# extraVolumes support tpl expressions:
#   - name: app-config
#     configMap:
#       name: '{{ include "app.fullname" . }}-config'

extraVolumeMounts: []
# extraVolumeMounts:
#   - name: app-config
#     mountPath: /configs
#     readOnly: true

configMaps: []
# configMaps:
#   - name: config
#     data:
#       config.yaml:            # key = filename; value = content (map or string)
#         key: value
#         nested:
#           field: example
#       another.yaml: |         # string literal also valid
#         plain: text

resources:
  requests:
    cpu: "100m"
    memory: "128Mi"
  limits:
    cpu: "250m"
    memory: "256Mi"

probes:
  type: httpGet    # httpGet | tcpSocket
  readiness:
    path: /        # only used when type: httpGet
    port: 80
    initialDelaySeconds: 5
    periodSeconds: 10
  liveness:
    path: /        # only used when type: httpGet
    port: 80
    initialDelaySeconds: 15
    periodSeconds: 20

hpa:
  enabled: false
  minReplicas: 1
  maxReplicas: 5
  targetCPUUtilizationPercentage: 50
  targetMemoryUtilizationPercentage: 0   # 0 = disabled; set e.g. 80 to enable

sidecars: []
# sidecars:
#   - name: sidecar
#     image: "example/sidecar:latest"
#     env: []
#     volumeMounts: []
```

---

## Step 3 — Create `app/templates/_helpers.tpl`

`app.fullname` uses `.Release.Name` directly. This means the resource name always equals the Helm release name — clean, predictable, no `release-chart` concatenation. Both frontend/backend (`helm install frontend app/`) and terraform-parse-service (as a subchart) resolve to the right name: the release name.

`selectorLabels` is kept separate from `labels` because selector labels are immutable after the first apply. `labels` includes `helm.sh/chart` with the chart version, which changes on every upgrade. Merging the two into the selector causes Deployments to be rejected on upgrade.

**`tpl` for `app.selectorLabels.name`:** The `app.kubernetes.io/name` label in `selectorLabels` is evaluated via `tpl`. This is the only selector field that needs it — `instance` is always `.Release.Name` (a built-in, no duplication). The `name` field defaults to `.Chart.Name` but can be overridden via `nameOverride`. Since `nameOverride` is a values string, it may itself contain a template expression like `'{{ .Values.global.appName }}'` or `'{{ .Release.Name }}'`. Passing it through `tpl` allows the user to share a single string across chart contexts (e.g. from a parent chart's `global:` block) without duplicating it per-service. A plain string (no `{{`) passes through `tpl` unchanged, so there is no regression for callers that don't need this feature.

```
# app/templates/_helpers.tpl

{{- define "app.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "app.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}

{{- define "app.selectorLabels" -}}
app.kubernetes.io/name: {{ tpl (default .Chart.Name .Values.nameOverride) . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
```

---

## Step 4 — Create `app/templates/deployment.yaml`

Key design decisions:
- `replicas` is conditionally omitted when HPA is enabled. A static `replicas` field fights HPA on every reconcile loop and resets HPA's scaling decision.
- `extraVolumes` and `extraVolumeMounts` are rendered with `tpl` so values can contain Go template expressions like `'{{ include "app.fullname" . }}-config'`. This allows ConfigMap names to be derived dynamically from the release name without hardcoding.
- `env` is rendered with `tpl` (not plain `toYaml`) so env var values can also contain template expressions — e.g. referencing a Secret name derived from the release name.
- `args`, `env`, `extraPorts`, `extraVolumes`, `extraVolumeMounts`, `sidecars` are all conditional on non-empty to keep the rendered manifest clean for simple services.
- `sidecars` is a list of additional containers appended to `spec.containers` after the main container. Rendered via `tpl` so sidecar fields can reference release name or values expressions.
- When `configMaps` is non-empty, a `checksum/<name>` annotation is added to the pod template for each entry. The hash is computed from the rendered CM data at template time. Any change to a ConfigMap's content changes its checksum, changes the pod template annotation, and triggers a rolling update. Without this, config changes are silently ignored by running pods until the next unrelated restart.

**Checksum limitation at the subchart boundary:** The `app` chart can only auto-checksum its own `configMaps` list (inline data). For `terraform-parse-service`'s file-based ConfigMaps (created in its own `templates/` via `.Files.Get`), the `app` subchart's deployment template has no access to the parent chart's rendered output. Those checksums must be passed explicitly via `app.podAnnotations` in values — computed from the source files during the chart packaging or CI step (e.g. `sha256sum files/configs/config.yaml`). This is documented in Step 13.

```yaml
# app/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "app.fullname" . }}
  labels:
    {{- include "app.labels" . | nindent 4 }}
spec:
  {{- if not .Values.hpa.enabled }}
  replicas: {{ .Values.replicaCount }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "app.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "app.selectorLabels" . | nindent 8 }}
      {{- if or .Values.podAnnotations .Values.configMaps }}
      annotations:
        {{- with .Values.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
        {{- range .Values.configMaps }}
        checksum/{{ .name }}: {{ tpl .data $ | sha256sum }}
        {{- end }}
      {{- end }}
    spec:
      {{- if .Values.extraVolumes }}
      volumes:
        {{- tpl (toYaml .Values.extraVolumes) . | nindent 8 }}
      {{- end }}
      containers:
        - name: {{ .Chart.Name }}
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          {{- if .Values.args }}
          args:
            {{- toYaml .Values.args | nindent 12 }}
          {{- end }}
          {{- if .Values.env }}
          env:
            {{- tpl (toYaml .Values.env) . | nindent 12 }}
          {{- end }}
          ports:
            - name: http
              containerPort: {{ .Values.service.port }}
              protocol: TCP
            {{- if .Values.extraPorts }}
            {{- toYaml .Values.extraPorts | nindent 12 }}
            {{- end }}
          {{- if .Values.extraVolumeMounts }}
          volumeMounts:
            {{- tpl (toYaml .Values.extraVolumeMounts) . | nindent 12 }}
          {{- end }}
          readinessProbe:
            {{- if eq .Values.probes.type "tcpSocket" }}
            tcpSocket:
              port: {{ .Values.probes.readiness.port }}
            {{- else }}
            httpGet:
              path: {{ .Values.probes.readiness.path }}
              port: {{ .Values.probes.readiness.port }}
            {{- end }}
            initialDelaySeconds: {{ .Values.probes.readiness.initialDelaySeconds }}
            periodSeconds: {{ .Values.probes.readiness.periodSeconds }}
          livenessProbe:
            {{- if eq .Values.probes.type "tcpSocket" }}
            tcpSocket:
              port: {{ .Values.probes.liveness.port }}
            {{- else }}
            httpGet:
              path: {{ .Values.probes.liveness.path }}
              port: {{ .Values.probes.liveness.port }}
            {{- end }}
            initialDelaySeconds: {{ .Values.probes.liveness.initialDelaySeconds }}
            periodSeconds: {{ .Values.probes.liveness.periodSeconds }}
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
        {{- if .Values.sidecars }}
        {{- tpl (toYaml .Values.sidecars) . | nindent 8 }}
        {{- end }}
```

---

## Step 5 — Create `app/templates/service.yaml`

`targetPort: http` references the named port on the container. If the port number changes in values, both the container port and the service target update in sync.

```yaml
# app/templates/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: {{ include "app.fullname" . }}
  labels:
    {{- include "app.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  ports:
    - port: {{ .Values.service.port }}
      targetPort: http
      protocol: TCP
      name: http
  selector:
    {{- include "app.selectorLabels" . | nindent 4 }}
```

---

## Step 6 — Create `app/templates/hpa.yaml`

HPA CPU utilization requires `resources.requests.cpu` on the container — enforced by the mandatory `resources` block in `values.yaml`.

Memory scaling is added as an optional second metric. The HPA controller evaluates all metrics and scales to satisfy the most demanding one. `targetMemoryUtilizationPercentage: 0` suppresses the memory metric entirely.

```yaml
# app/templates/hpa.yaml
{{- if .Values.hpa.enabled }}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ include "app.fullname" . }}
  labels:
    {{- include "app.labels" . | nindent 4 }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {{ include "app.fullname" . }}
  minReplicas: {{ .Values.hpa.minReplicas }}
  maxReplicas: {{ .Values.hpa.maxReplicas }}
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: {{ .Values.hpa.targetCPUUtilizationPercentage }}
    {{- if .Values.hpa.targetMemoryUtilizationPercentage }}
    - type: Resource
      resource:
        name: memory
        target:
          type: Utilization
          averageUtilization: {{ .Values.hpa.targetMemoryUtilizationPercentage }}
    {{- end }}
{{- end }}
```

---

## Step 7 — Create `app/templates/configmap.yaml`

Generic ConfigMap template. `data` is a **map** — each key is a filename, each value is the file content (either a structured YAML map or a string literal). Each value is passed through `tpl` so template expressions (e.g. `{{ .Release.Name }}`) are evaluated. The template iterates the map and emits each key-value pair as a ConfigMap data entry.

Using a map for `data` instead of a multi-line string gives Helm deep-merge semantics: env overlays can override individual keys (e.g. override only `config.yaml` content) without replacing the whole `data` block.

```yaml
# app/templates/configmap.yaml
{{- range .Values.configMaps }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "app.fullname" $ }}-{{ .name }}
  labels:
    {{- include "app.labels" $ | nindent 4 }}
data:
  {{- range $key, $val := .data }}
  {{ $key }}: |
    {{- tpl ($val | toYaml) $ | nindent 4 }}
  {{- end }}
{{- end }}
```

---

## Step 8 — Create `gateway/` chart

The `gateway/` chart is a standalone Helm application chart that owns all Istio resources: Gateway, VirtualService, and DestinationRule. Services that need Istio routing declare it as a dependency in their own `Chart.yaml`. Services that do not need Istio simply omit the dependency — `app/` has no Istio templates and renders cleanly without it.

**Why a separate chart, not inside `app/`:**
- `app/` is generic across all deployment patterns including non-Istio clusters. Embedding Istio CRDs means `helm lint` fails on clusters without Istio installed, even when `istio.enabled: false`.
- The gateway chart composes across multiple service releases to do canary/A/B weighting — that logic belongs in one place, not replicated per-service.
- Service chart authors can opt in by adding one dependency line; ops teams can install the gateway chart standalone to manage routing for multiple services centrally.

**`tpl` for label matching fields:** All fields that identify the target service in VirtualService `host` and DestinationRule `host` are evaluated via `tpl`. This lets the caller pass `'{{ .Release.Name }}'`, `'{{ .Values.global.serviceName }}'`, or a literal string — without duplicating the value across the gateway chart's values and the service chart's values. The same pattern applies to `gateways` reference in the VS when pointing to a gateway owned by another release.

**Chart structure:**
```yaml
# gateway/Chart.yaml
apiVersion: v2
name: gateway
description: Istio routing resources — Gateway, VirtualService, DestinationRule
type: application
version: 0.1.0
appVersion: "0.1.0"
```

```yaml
# gateway/values.yaml
istio:
  enabled: false

  gateway:
    create: false      # true = create a Gateway resource owned by this release
    name: ""           # if create: false, name of an existing Gateway to attach VS to
    port: 80
    hosts:
      - "*"

  virtualService:
    enabled: false
    hosts: []          # e.g. ["frontend.example.com"]
    routes: []         # full Istio http route objects — user defines structure completely
    # routes:
    #   # header match + single destination (per-service subchart pattern):
    #   - match:
    #       - headers:
    #           release:
    #             exact: my-release
    #     route:
    #       - destination:
    #           host: my-service
    #           port:
    #             number: 8080
    #   # weighted canary (top-level gateway pattern):
    #   - route:
    #       - destination:
    #           host: my-service-stable
    #           port:
    #             number: 80
    #         weight: 90
    #       - destination:
    #           host: my-service-canary
    #           port:
    #             number: 80
    #         weight: 10

  destinationRule:
    enabled: false
    # host supports tpl — e.g. '{{ .Release.Name }}'
    host: ""
    trafficPolicy: {}  # optional: connectionPool, outlierDetection
    # trafficPolicy:
    #   connectionPool:
    #     http:
    #       http1MaxPendingRequests: 1024
    #       http2MaxRequests: 1024
    #   outlierDetection:
    #     consecutive5xxErrors: 5
    #     interval: 30s
    #     baseEjectionTime: 30s
```

**`gateway/templates/_helpers.tpl`** mirrors `app/`'s helpers. `gateway.fullname` and `gateway.selectorLabels` follow the same `tpl`-on-`nameOverride` pattern as `app/` so that if the gateway chart is used as a subchart its name resolves consistently with the service it routes.

```
# gateway/templates/_helpers.tpl

{{- define "gateway.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "gateway.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
```

**Templates:**

```yaml
# gateway/templates/gateway.yaml
{{- if and .Values.istio.enabled .Values.istio.gateway.create }}
apiVersion: networking.istio.io/v1beta1
kind: Gateway
metadata:
  name: {{ include "gateway.fullname" . }}
  labels:
    {{- include "gateway.labels" . | nindent 4 }}
spec:
  selector:
    istio: ingressgateway
  servers:
    - port:
        number: {{ .Values.istio.gateway.port }}
        name: http
        protocol: HTTP
      hosts:
        {{- toYaml .Values.istio.gateway.hosts | nindent 8 }}
{{- end }}
```
The VirtualService template is intentionally thin — it renders `gateways` and `hosts` from structured fields, then passes `routes` verbatim as raw YAML. The user defines the complete `http` route structure in values: match conditions, destinations, weights, retries, timeouts. No templating logic constrains the route shape.

This covers both common patterns without any branching in the template:
- **Header-match per-service**: define a `match.headers.release.exact` rule in the route entry
- **Weighted canary**: define multiple `route` destinations with `weight` fields, no `match`

```yaml
# gateway/templates/virtualservice.yaml
{{- if and .Values.istio.enabled .Values.istio.virtualService.enabled }}
apiVersion: networking.istio.io/v1beta1
kind: VirtualService
metadata:
  name: {{ include "gateway.fullname" . }}
  labels:
    {{- include "gateway.labels" . | nindent 4 }}
spec:
  hosts:
    {{- toYaml .Values.istio.virtualService.hosts | nindent 4 }}
  gateways:
    {{- if .Values.istio.gateway.create }}
    - {{ include "gateway.fullname" . }}
    {{- else }}
    - {{ required "istio.gateway.name is required when gateway.create is false" .Values.istio.gateway.name }}
    {{- end }}
  http:
    {{- tpl (toYaml .Values.istio.virtualService.routes) . | nindent 4 }}
{{- end }}
```

`tpl` is applied to `routes` before `toYaml` output so route fields can reference `{{ .Release.Name }}` or `{{ .Values.global.* }}` inline. An empty `routes: []` produces a VS with no `http` rules — Envoy drops all traffic. `helm lint` does not validate this, so CI should assert `routes` is non-empty when `virtualService.enabled: true`.

```yaml
# gateway/templates/destinationrule.yaml
{{- if and .Values.istio.enabled .Values.istio.destinationRule.enabled }}
apiVersion: networking.istio.io/v1beta1
kind: DestinationRule
metadata:
  name: {{ include "gateway.fullname" . }}
  labels:
    {{- include "gateway.labels" . | nindent 4 }}
spec:
  host: {{ tpl (required "istio.destinationRule.host is required" .Values.istio.destinationRule.host) . }}
  {{- if .Values.istio.destinationRule.trafficPolicy }}
  trafficPolicy:
    {{- toYaml .Values.istio.destinationRule.trafficPolicy | nindent 4 }}
  {{- end }}
{{- end }}
```

**Design:** `trafficPolicy` is optional — a DestinationRule with no trafficPolicy is valid YAML but serves no purpose. An empty DR (`enabled: true`, `trafficPolicy: {}`) should not be deployed; `helm lint` will not catch this so values must be reviewed before enabling the DR.

**How services declare the gateway dependency:**

```yaml
# terraform-parse-service/Chart.yaml
apiVersion: v2
name: terraform-parse-service
description: Terraform HCL generation service
type: application
version: 0.1.0
appVersion: "0.1.0"
dependencies:
  - name: app
    version: "0.1.0"
    repository: "file://../app"
  - name: gateway
    version: "0.1.0"
    repository: "file://../gateway"
```

`frontend/` and `backend/` are values-only directories — they have no `Chart.yaml`, so they cannot declare dependencies. Services that need Istio and use the values-only pattern must be promoted to a proper chart (like `terraform-parse-service`). Alternatively, the gateway chart can be installed as a standalone release that points at the same service host — though this splits values management across two installs. The recommended path for any service needing Istio is: create a `Chart.yaml`, declare `app` + `gateway` as dependencies.

**Values scoping for the `gateway` subchart:** All gateway configuration lives under `gateway:` in the service chart's values, routed by Helm's subchart values key convention.

**Per-service subchart pattern** — header-match route (terraform-parse-service):

```yaml
# in terraform-parse-service/values.yaml
gateway:
  istio:
    enabled: true
    gateway:
      create: true
      port: 80
      hosts:
        - "terraform-parse-service.example.com"
    virtualService:
      enabled: true
      hosts:
        - "terraform-parse-service.example.com"
      routes:
        - match:
            - headers:
                release:
                  exact: '{{ .Release.Name }}'   # tpl evaluated
          route:
            - destination:
                host: '{{ .Release.Name }}'       # tpl evaluated
                port:
                  number: 8080
    destinationRule:
      enabled: false
```

**Standalone canary gateway** — weighted route (e.g. `helm upgrade --install frontend-gw gateway/ -f values.yaml`):

```yaml
# standalone gateway values for canary rollout
istio:
  enabled: true
  gateway:
    create: false
    name: "frontend-gateway"   # attach to existing gateway
  virtualService:
    enabled: true
    hosts:
      - "frontend.example.com"
    routes:
      - route:
          - destination:
              host: frontend-stable
              port:
                number: 80
            weight: 90
          - destination:
              host: frontend-canary
              port:
                number: 80
            weight: 10
  destinationRule:
    enabled: false
```

---

## Step 9 — Create `frontend/` values files

Since all environments use `ClusterIP` — there is no need for `NodePort` or `LoadBalancer`. NodePort has been removed from the dev overlay.

**Values-only services and Istio:** `frontend/` and `backend/` are values-only directories (no `Chart.yaml`). They cannot declare Helm dependencies, which means they cannot use the `gateway/` chart. For services that need Istio routing, the options are:
1. Promote the service to a proper chart (create `Chart.yaml`, declare `app` + `gateway` as deps) — recommended
2. Install the gateway chart as a separate standalone release that references the same service host

The values files below omit the `istio:` block because nothing in `app/` reads it after the move to `gateway/`. If `frontend` is promoted to a proper chart, add `gateway:` at the top level following the pattern from Step 8.

```yaml
# frontend/values.yaml
replicaCount: 2

image:
  repository: nginx
  tag: "1.25.3"
  pullPolicy: IfNotPresent

service:
  type: ClusterIP
  port: 80

resources:
  requests:
    cpu: "100m"
    memory: "128Mi"
  limits:
    cpu: "250m"
    memory: "256Mi"

probes:
  type: httpGet
  readiness:
    path: /
    port: 80
    initialDelaySeconds: 5
    periodSeconds: 10
  liveness:
    path: /
    port: 80
    initialDelaySeconds: 15
    periodSeconds: 20

hpa:
  enabled: false
  minReplicas: 2
  maxReplicas: 6
  targetCPUUtilizationPercentage: 60
  targetMemoryUtilizationPercentage: 0
```

```yaml
# frontend/values-dev.yaml
replicaCount: 1
hpa:
  enabled: false
```

```yaml
# frontend/values-staging.yaml
replicaCount: 2
hpa:
  enabled: true
  minReplicas: 2
  maxReplicas: 4
  targetCPUUtilizationPercentage: 60
```

```yaml
# frontend/values-prod.yaml
replicaCount: 3
resources:
  requests:
    cpu: "200m"
    memory: "256Mi"
  limits:
    cpu: "500m"
    memory: "512Mi"
hpa:
  enabled: true
  minReplicas: 3
  maxReplicas: 10
  targetCPUUtilizationPercentage: 50
  targetMemoryUtilizationPercentage: 80
```

---

## Step 10 — Create `backend/` values files

Same constraint as `frontend/` — values-only, no Helm dependency support, no Istio. Promote to a proper chart if Istio routing is needed.

```yaml
# backend/values.yaml
replicaCount: 1

image:
  repository: hashicorp/http-echo
  tag: "0.2.3"
  pullPolicy: IfNotPresent

args:
  - "-text=hello"

service:
  type: ClusterIP
  port: 8080

resources:
  requests:
    cpu: "50m"
    memory: "64Mi"
  limits:
    cpu: "150m"
    memory: "128Mi"

probes:
  type: httpGet
  readiness:
    path: /
    port: 8080
    initialDelaySeconds: 5
    periodSeconds: 10
  liveness:
    path: /
    port: 8080
    initialDelaySeconds: 10
    periodSeconds: 20

hpa:
  enabled: true
  minReplicas: 1
  maxReplicas: 5
  targetCPUUtilizationPercentage: 50
  targetMemoryUtilizationPercentage: 0
```

```yaml
# backend/values-dev.yaml
replicaCount: 1
hpa:
  enabled: false
```

```yaml
# backend/values-staging.yaml
replicaCount: 1
hpa:
  enabled: true
  minReplicas: 1
  maxReplicas: 3
```

```yaml
# backend/values-prod.yaml
replicaCount: 2
resources:
  requests:
    cpu: "100m"
    memory: "128Mi"
  limits:
    cpu: "300m"
    memory: "256Mi"
hpa:
  enabled: true
  minReplicas: 2
  maxReplicas: 8
  targetCPUUtilizationPercentage: 40
  targetMemoryUtilizationPercentage: 80
```

---

## Step 11 — Create `terraform-parse-service/Chart.yaml`

`terraform-parse-service` is a real chart because it needs `.Files.Get` to read `configs/config.yaml` and `templates/aws/s3/bucket.tf.tmpl` from disk and package them as Kubernetes ConfigMaps. This is only possible inside a chart's template context — it cannot be done from a values-only directory.

Declaring `app` and `gateway` as dependencies means Helm renders all `app/` templates (Deployment, Service, HPA, etc.) and all `gateway/` templates (Gateway, VirtualService, DestinationRule) alongside the service's own ConfigMap templates in a single `helm upgrade --install`.

```yaml
# terraform-parse-service/Chart.yaml
apiVersion: v2
name: terraform-parse-service
description: Terraform HCL generation service
type: application
version: 0.1.0
appVersion: "0.1.0"
dependencies:
  - name: app
    version: "0.1.0"
    repository: "file://../app"
  - name: gateway
    version: "0.1.0"
    repository: "file://../gateway"
```

---

## Step 12 — Create `terraform-parse-service/` ConfigMap templates

Only one custom template is needed — `configmap-templates.yaml` for the file-based Terraform templates. The service config is handled by `app/templates/configmap.yaml` via `app.configMaps`.

**How the config CM reuses `app/configmap.yaml`:** `app.configMaps[].data` is a map where each key is a filename and the value is the file content as a structured YAML map. The template iterates the map, renders each value through `tpl`, and emits the result as a block scalar. Helm deep-merges map values, so env overlays can override individual keys within `data` without clobbering unrelated keys. `configmap-config.yaml` is removed from `terraform-parse-service/templates/`.

```yaml
# in terraform-parse-service/values.yaml (under app:)
app:
  configMaps:
    - name: config
      data:
        config.yaml:
          listen_addr: ":8080"
          logger:
            level: "info"
          # ... rest of config
```

Per-environment overrides require replacing the full `data` block in the env overlay values file — Helm merges do not deep-merge multi-line string values. `configmap-config.yaml` is removed from `terraform-parse-service/templates/`.

**`configmap-templates.yaml`** — one ConfigMap per template file. Each entry in `tfTemplates` is a **full relative path** (e.g. `aws/s3/bucket.tf.tmpl`, `azure/vm/main.tf.tmpl`) — not a bare filename. This supports arbitrary provider/resource hierarchies under `files/templates/` without any hardcoded path prefix.

**Naming convention:** The CM name is derived by replacing `/` and `.` separators in the relative path with `-` and appending `-tmpl`:

```
aws/s3/bucket.tf.tmpl   →  slug: aws-s3-bucket-tf-tmpl   →  CM name: <fullname>-aws-s3-bucket-tf-tmpl
azure/vm/main.tf.tmpl   →  slug: azure-vm-main-tf-tmpl   →  CM name: <fullname>-azure-vm-main-tf-tmpl
```

Slug derivation in Helm: `replace "/" "-" | replace "." "-"` applied to the relative path, then append `-tmpl`.

**CM data key** is the relative path itself (e.g. `aws/s3/bucket.tf.tmpl`). This is the key k8s-sidecar reads and uses to reconstruct the directory structure under its target directory.

```yaml
# terraform-parse-service/templates/configmap-templates.yaml
{{- range .Values.tfTemplates }}
{{- $slug := printf "%s-tmpl" (. | replace "/" "-" | replace "." "-") }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "app.fullname" $ }}-{{ $slug }}
  labels:
    {{- include "app.labels" $ | nindent 4 }}
    terraform-parse-service/template: "true"
data:
  {{ . }}: |
    {{- $.Files.Get (printf "files/templates/%s" .) | nindent 4 }}
{{- end }}
```

**Mount strategy — k8s-sidecar:** Instead of one `subPath` volume mount per template, a single [kiwigrid/k8s-sidecar](https://github.com/kiwigrid/k8s-sidecar) container runs alongside the main container. It watches for ConfigMaps with the label `terraform-parse-service/template: "true"`, reads each key (the relative path), and writes the content to `/templates/<relative-path>` — reconstructing the full directory tree automatically. This means adding a new template requires only committing the file and listing its path in `tfTemplates`; no `extraVolumes`/`extraVolumeMounts` changes are needed.

The sidecar shares a single `emptyDir` volume mounted at `/templates` with the main container.

**`sidecars` field added to `app/values.yaml`** — arbitrary sidecar container definitions injected alongside the main container. Follows the same `tpl`-rendered `extraVolumes`/`extraVolumeMounts` pattern.

**`app/values.yaml` addition:**
```yaml
sidecars: []
# sidecars:
#   - name: sidecar
#     image: "example/sidecar:latest"
#     env: []
#     volumeMounts: []
```

**`app/templates/deployment.yaml` addition** (inside `spec.containers`, after the main container):
```yaml
          {{- if .Values.sidecars }}
          {{- tpl (toYaml .Values.sidecars) . | nindent 8 }}
          {{- end }}
```

**Missing file behaviour:** If a path is listed in `tfTemplates` but the file does not exist under `files/templates/`, `.Files.Get` returns an empty string — the CM key is present but empty and the sidecar writes an empty file. Catch in CI: `helm template terraform-parse-service terraform-parse-service/ | grep -A2 'tf.tmpl'`.

---

## Step 13 — Create `terraform-parse-service/` values files

Values structure:
- `app:` — routes all values to the `app` subchart (Deployment, Service, HPA, ConfigMaps, sidecars). `app.configMaps` holds one entry with the full config written as a literal YAML string in `data`. Env overlays replace the entire `data` block for that environment.
- `gateway:` — routes all values to the `gateway` subchart (Istio Gateway, VirtualService, DestinationRule)
- `tfTemplates:` — top-level key; **list of relative paths** under `files/templates/` (e.g. `aws/s3/bucket.tf.tmpl`, `azure/vm/main.tf.tmpl`); `configmap-templates.yaml` reads each file from disk via `.Files.Get` and emits one CM per path; only listed paths are active per environment

**Checksums:** The config data string is inside `app.configMaps`, so `app/deployment.yaml`'s automatic `checksum/config` annotation applies — changing the data block triggers a rolling restart. `tfTemplates`-sourced CMs are watched live by the k8s-sidecar — it reacts to CM changes without a pod restart, so no checksum annotation is needed for those.

`CONFIG_PATH` is removed from `env` — the container's default `CONFIG_PATH=/configs/config.yaml` matches the volume mount point.

`extraVolumes` includes one `emptyDir` named `templates` shared between the main container and the k8s-sidecar, and one `emptyDir` named `output` mounted at `/output` for generated `.tf` files. The config CM is still mounted via `extraVolumes`/`extraVolumeMounts` as before — only the template CMs are handled by the sidecar.
```yaml
# terraform-parse-service/values.yaml
app:
  replicaCount: 1

  image:
    repository: terraform-parse-service
    tag: "0.1.0"
    pullPolicy: IfNotPresent

  env:
    - name: APP_ENV
      value: "production"

  service:
    type: ClusterIP
    port: 8080

  extraPorts:
    - name: metrics
      containerPort: 9091
      protocol: TCP

  podAnnotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "9091"
    prometheus.io/path: "/metrics"

  extraVolumes:
    - name: app-config
      configMap:
        name: '{{ include "app.fullname" . }}-config'
    - name: templates
      emptyDir: {}
    - name: output
      emptyDir: {}

  extraVolumeMounts:
    - name: app-config
      mountPath: /configs
      readOnly: true
    - name: templates
      mountPath: /templates
    - name: output
      mountPath: /output

  sidecars:
    - name: k8s-sidecar
      image: kiwigrid/k8s-sidecar:1.27.5
      env:
        - name: LABEL
          value: "terraform-parse-service/template"
        - name: LABEL_VALUE
          value: "true"
        - name: FOLDER
          value: "/templates"
        - name: RESOURCE
          value: "configmap"
        - name: NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
      volumeMounts:
        - name: templates
          mountPath: /templates

  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "500m"
      memory: "256Mi"

  probes:
    type: tcpSocket    # no /health endpoint; scratch image blocks exec probes
    readiness:
      port: 8080
      initialDelaySeconds: 5
      periodSeconds: 10
    liveness:
      port: 8080
      initialDelaySeconds: 10
      periodSeconds: 20

  hpa:
    enabled: false
    minReplicas: 1
    maxReplicas: 4
    targetCPUUtilizationPercentage: 60
    targetMemoryUtilizationPercentage: 0

  configMaps:
    - name: config
      data:
        config.yaml:
          listen_addr: ":8080"
          logger:
            level: "info"
            metadata:
              service: "terraform-parse-service"
              env: "${APP_ENV}"
          tracing:
            exporter: "stdout"
            endpoint: "localhost:4317"
            insecure: false
            sample_ratio: 1.0
          metrics:
            addr: ":9091"
          providers:
            aws:
              templates_dir: "./templates/aws"
              storage_dir: "./output/aws"

# Istio routing — routed to the gateway subchart
gateway:
  istio:
    enabled: true
    gateway:
      create: true
      port: 80
      hosts:
        - "terraform-parse-service.example.com"
    virtualService:
      enabled: true
      hosts:
        - "terraform-parse-service.example.com"
      routes:
        - match:
            - headers:
                release:
                  exact: '{{ .Release.Name }}'   # tpl evaluated
          route:
            - destination:
                host: '{{ .Release.Name }}'       # tpl evaluated
                port:
                  number: 8080
    destinationRule:
      enabled: false

# Terraform templates — list of relative paths under files/templates/; content read via .Files.Get
tfTemplates:
  - aws/s3/bucket.tf.tmpl
```

```yaml
# terraform-parse-service/values-dev.yaml
app:
  replicaCount: 1
  env:
    - name: APP_ENV
      value: "dev"
  hpa:
    enabled: false
  configMaps:
    - name: config
      data:
        config.yaml:
          listen_addr: ":8080"
          logger:
            level: "debug"
            metadata:
              service: "terraform-parse-service"
              env: "dev"
          tracing:
            exporter: "stdout"
            endpoint: "localhost:4317"
            insecure: false
            sample_ratio: 1.0
          metrics:
            addr: ":9091"
          providers:
            aws:
              templates_dir: "./templates/aws"
              storage_dir: "./output/aws"

gateway:
  istio:
    gateway:
      hosts:
        - "terraform-parse-service.dev.example.com"
    virtualService:
      hosts:
        - "terraform-parse-service.dev.example.com"

# dev: only bucket template active
tfTemplates:
  - aws/s3/bucket.tf.tmpl
```

```yaml
# terraform-parse-service/values-staging.yaml
app:
  replicaCount: 1
  env:
    - name: APP_ENV
      value: "staging"
  hpa:
    enabled: true
    minReplicas: 1
    maxReplicas: 2
    targetCPUUtilizationPercentage: 60
  configMaps:
    - name: config
      data:
        config.yaml:
          listen_addr: ":8080"
          logger:
            level: "info"
            metadata:
              service: "terraform-parse-service"
              env: "staging"
          tracing:
            exporter: "otlp_grpc"
            endpoint: "tempo.monitoring.svc.cluster.local:4317"
            insecure: true
            sample_ratio: 0.5
          metrics:
            addr: ":9091"
          providers:
            aws:
              templates_dir: "./templates/aws"
              storage_dir: "./output/aws"

gateway:
  istio:
    virtualService:
      hosts:
        - "terraform-parse-service.staging.example.com"

# staging: bucket validated, rds under test
tfTemplates:
  - aws/s3/bucket.tf.tmpl
  - aws/s3/rds.tf.tmpl
```

```yaml
# terraform-parse-service/values-prod.yaml
app:
  replicaCount: 2
  env:
    - name: APP_ENV
      value: "production"
  resources:
    requests:
      cpu: "200m"
      memory: "256Mi"
    limits:
      cpu: "1000m"
      memory: "512Mi"
  hpa:
    enabled: true
    minReplicas: 2
    maxReplicas: 6
    targetCPUUtilizationPercentage: 50
    targetMemoryUtilizationPercentage: 80
  configMaps:
    - name: config
      data:
        config.yaml:
          listen_addr: ":8080"
          logger:
            level: "info"
            metadata:
              service: "terraform-parse-service"
              env: "production"
          tracing:
            exporter: "otlp_grpc"
            endpoint: "tempo.monitoring.svc.cluster.local:4317"
            insecure: false
            sample_ratio: 0.1
          metrics:
            addr: ":9091"
          providers:
            aws:
              templates_dir: "./templates/aws"
              storage_dir: "./output/aws"

gateway:
  istio:
    virtualService:
      hosts:
        - "terraform-parse-service.example.com"
    destinationRule:
      enabled: true
      host: '{{ .Release.Name }}'

# prod: only bucket — rds not yet promoted
tfTemplates:
  - aws/s3/bucket.tf.tmpl
```

---

## Step 14 — Verification

```bash
# Bootstrap subchart dependencies (app + gateway)
helm dep update terraform-parse-service/

# Lint standalone charts
helm lint app/
helm lint gateway/
helm lint terraform-parse-service/ -f terraform-parse-service/values.yaml

# Lint with values-only services
helm lint app/ -f frontend/values.yaml
helm lint app/ -f backend/values.yaml

# Dry-run render + API server schema validation
helm template frontend app/ -f frontend/values.yaml | kubectl apply --dry-run=client -f -
helm template backend  app/ -f backend/values.yaml  | kubectl apply --dry-run=client -f -
helm template terraform-parse-service terraform-parse-service/ \
  -f terraform-parse-service/values.yaml | kubectl apply --dry-run=client -f -

# Verify HPA has both cpu and memory metrics in prod
helm template backend app/ -f backend/values.yaml -f backend/values-prod.yaml \
  | grep -A20 "HorizontalPodAutoscaler" | grep "name: memory"  # expect a match

# Verify HPA absent in dev
helm template backend app/ -f backend/values.yaml -f backend/values-dev.yaml \
  | grep -c "HorizontalPodAutoscaler"  # expect 0

# Verify tcpSocket probes for terraform-parse-service
helm template terraform-parse-service terraform-parse-service/ \
  -f terraform-parse-service/values.yaml | grep -A2 "readinessProbe"
# expected: tcpSocket:

# Verify ConfigMaps are rendered with correct names
helm template terraform-parse-service terraform-parse-service/ \
  -f terraform-parse-service/values.yaml | grep "^  name:" | grep "terraform-parse-service"
# expected: terraform-parse-service-config, terraform-parse-service-templates

# Verify Istio resources rendered for terraform-parse-service (has gateway dep)
helm template terraform-parse-service terraform-parse-service/ \
  -f terraform-parse-service/values.yaml | grep "^kind:" | sort | uniq
# expected includes: Deployment, Service, ConfigMap, Gateway, VirtualService

# Verify Istio resources absent from app/ (no Istio templates in app/)
helm template frontend app/ -f frontend/values.yaml | grep "kind:" | sort | uniq
# expected: Deployment, Service only (no Gateway, VirtualService, DestinationRule)

# Verify tpl rendering for selectorLabels — name matches chart name by default
helm template frontend app/ -f frontend/values.yaml \
  | grep "app.kubernetes.io/name"  # expect: app.kubernetes.io/name: app

# Verify selectorLabels with nameOverride via tpl
helm template frontend app/ -f frontend/values.yaml \
  --set 'nameOverride={{ .Release.Name }}' \
  | grep "app.kubernetes.io/name"  # expect: app.kubernetes.io/name: frontend

```

---

## Known Limitation: Output Storage

`terraform-parse-service` writes generated `.tf` files to `./output/aws` inside the container. An `emptyDir` volume is mounted at `/output` (Step 13 `extraVolumes`) — this keeps the output path writable and prevents write failures, but data is lost on pod restart.

Long-term, the service should write to external object storage (S3) rather than the container filesystem — `emptyDir` is incompatible with horizontal scaling (each pod writes to its own ephemeral volume, output is not shared across replicas) and data is lost on any pod restart or reschedule.

---

## Issue-to-Fix Mapping

| Issue | Fixed in Step |
|---|---|
| `backend-deployment.yaml` leading `\`, broken YAML | Step 4 (full rewrite) |
| Both deployments missing `spec.selector` | Step 4 |
| `frontend-service` selector ≠ pod labels | Steps 3–5 (shared `selectorLabels` helper, drift structurally impossible) |
| HPA CPU metric non-functional (no resource requests) | Steps 2, 10 (resources mandatory in schema) |
| `values.yaml` entirely unused | Steps 4–13 (all templates read `.Values.*` exclusively) |
| No `_helpers.tpl`, label drift | Step 3 |
| Missing Helm/K8s recommended labels | Step 3 |
| No resource requests/limits | Steps 2, 9, 10, 13 |
| Mutable `latest` tags | Steps 9, 10, 13 |
| No liveness/readiness probes | Steps 4, 9, 10, 13 |
| No per-environment configuration | Steps 9, 10, 13 |
| HPA not conditional | Step 6 |
| HPA only scales on CPU, not memory | Step 6 (`targetMemoryUtilizationPercentage`) |
| Static `replicas` conflicts with HPA | Step 4 (conditional replicas block) |
| Monolithic chart, services coupled | Architecture change: single generic `app` chart + per-service values dirs |
| No ingress / traffic management | Step 8 (`gateway/` chart — Gateway, VirtualService, DestinationRule; declared as dep by services that need Istio) |
| Istio coupled into `app/`, breaks non-Istio clusters | Step 8 (Istio moved to `gateway/` chart; `app/` renders cleanly without it) |
| `terraform-parse-service` has no health endpoint | Step 13 (`probes.type: tcpSocket`) |
| `terraform-parse-service` dual ports | Steps 2, 4, 13 (`extraPorts` field) |
| `terraform-parse-service` Prometheus metrics not scraped | Step 13 (`podAnnotations`) |
| `terraform-parse-service` config not externalizable | Step 13 (`app.configMaps[].data` inline literal rendered by `app/configmap.yaml`; no custom template needed) |
| `terraform-parse-service` Terraform templates not externalizable | Steps 12–13 (ConfigMap per template via `.Files.Get`) |
| Config changes don't restart pods (inline CMs) | Step 4 (`checksum/<name>` annotations computed from `tpl .data`) |
| Config changes don't restart pods (file-based CMs) | Step 13 (values-driven CMs — checksum is automatic, no CI step needed) |
| `env` values cannot use template expressions | Step 4 (`tpl` applied to env block) |
| Istio VS routing | Step 8 (`gateway/` chart — `routes: []` is a raw Istio http route list; user defines match/destination/weight structure; tpl applied so fields can reference release name or global values) |
| Istio VS has no header-based routing | Step 8 (`release: <release-name>` header match in `headerMatch` mode; mandatory, unconditional) |
| Istio DR `trafficPolicy` always required | Step 8 (conditional — omitted when empty) |
| Label matching fields cannot reference shared/global values | Steps 3, 8 (`tpl` on `nameOverride` in `selectorLabels`; same in `gateway/` for `host` and `serviceName` fields) |
| terraform-parse-service config not per-environment | Step 13 (`app.configMaps[].data` inline literal — each env overlay replaces the full data block with its environment-specific config) |
| All Terraform templates always deployed regardless of env | Steps 12–13 (`tfTemplates:` path allowlist — full relative paths under `files/templates/`; one CM per path; only listed paths active per env) |
| Terraform templates hardcoded to single provider/path | Step 12 (`tfTemplates` uses full relative path, not bare filename; supports `aws/s3/`, `azure/vm/`, `aws/ec2/`, any hierarchy) |
| Templates require per-file volume mounts | Step 12 (k8s-sidecar watches for CMs labelled `terraform-parse-service/template: "true"`, writes to `/templates/<relative-path>`; one `emptyDir` volume shared with main container) |

---

## TODO

### Phase 0 — Cleanup: remove the old monolithic chart

- [x] Delete `helm/Chart.yaml`
- [x] Delete `helm/values.yaml`
- [x] Delete `helm/templates/frontend-deployment.yaml`
- [x] Delete `helm/templates/frontend-service.yaml`
- [x] Delete `helm/templates/backend-deployment.yaml`
- [x] Delete `helm/templates/backend-service.yaml`
- [x] Delete `helm/templates/hpa.yaml`
- [x] Remove `helm/templates/` directory once empty

### Phase 1 — `app/` chart

- [x] Create `app/Chart.yaml` (Step 1)
- [x] Create `app/values.yaml` — full schema with safe defaults including `sidecars: []` (Step 2)
- [x] Create `app/templates/_helpers.tpl` — `app.fullname`, `app.labels`, `app.selectorLabels` with `tpl` on `nameOverride` (Step 3)
- [x] Create `app/templates/deployment.yaml` — conditional `replicas`, `tpl`-rendered `extraVolumes`/`extraVolumeMounts`/`env`/`sidecars`, `checksum` annotations, `tcpSocket`/`httpGet` probe switch (Step 4)
- [x] Create `app/templates/service.yaml` — named port `http`, selector via `app.selectorLabels` (Step 5)
- [x] Create `app/templates/hpa.yaml` — `autoscaling/v2`, CPU + optional memory metric, conditional on `hpa.enabled` (Step 6)
- [x] Create `app/templates/configmap.yaml` — `range .Values.configMaps`, `range $key, $val := .data`, `tpl ($val | toYaml)` (Step 7)

### Phase 2 — `gateway/` chart

- [x] Create `gateway/Chart.yaml` (Step 8)
- [x] Create `gateway/values.yaml` — full `istio` block with safe defaults, `routes: []` (Step 8)
- [x] Create `gateway/templates/_helpers.tpl` — mirrors `app/` helpers (Step 8)
- [x] Create `gateway/templates/gateway.yaml` — conditional on `istio.enabled` and `istio.gateway.create` (Step 8)
- [x] Create `gateway/templates/virtualservice.yaml` — `tpl (toYaml .Values.istio.virtualService.routes)`, `required` guard for `gateway.name` when `create: false` (Step 8)
- [x] Create `gateway/templates/destinationrule.yaml` — conditional `trafficPolicy` block, `tpl` on `host` (Step 8)

### Phase 3 — `frontend/` values

- [x] Create `frontend/values.yaml` — base values: image, resources, probes, hpa defaults (Step 9)
- [x] Create `frontend/values-dev.yaml` — `replicaCount: 1`, `hpa.enabled: false` (Step 9)
- [x] Create `frontend/values-staging.yaml` — HPA enabled, 2–4 replicas (Step 9)
- [x] Create `frontend/values-prod.yaml` — higher resources, HPA enabled, 3–10 replicas, memory metric (Step 9)

### Phase 4 — `backend/` values

- [x] Create `backend/values.yaml` — base values: `http-echo` image, `args`, resources, probes, hpa (Step 10)
- [x] Create `backend/values-dev.yaml` — `replicaCount: 1`, `hpa.enabled: false` (Step 10)
- [x] Create `backend/values-staging.yaml` — HPA enabled (Step 10)
- [x] Create `backend/values-prod.yaml` — higher resources, HPA enabled, memory metric (Step 10)

### Phase 5 — `terraform-parse-service/` chart scaffold

- [x] Create `terraform-parse-service/Chart.yaml` — `type: application`, deps `app` + `gateway` at `file://../app` and `file://../gateway` (Step 11)
- [x] Create `terraform-parse-service/files/templates/aws/s3/bucket.tf.tmpl` — source-of-truth template file on disk
- [x] Create `terraform-parse-service/files/templates/aws/s3/rds.tf.tmpl` — staging/test template
- [x] Run `helm dep update terraform-parse-service/` to populate `charts/`

### Phase 6 — `terraform-parse-service/` ConfigMap template

- [x] Create `terraform-parse-service/templates/configmap-templates.yaml` — `range .Values.tfTemplates`, slug from full relative path (`replace "/" "-" | replace "." "-"` + `-tmpl`), label `terraform-parse-service/template: "true"`, data key = relative path, content via `.Files.Get (printf "files/templates/%s" .)` (Step 12)

### Phase 7 — `terraform-parse-service/` values files

- [x] Create `terraform-parse-service/values.yaml` — `app` block with image, env, service, extraPorts, podAnnotations, extraVolumes (`app-config` CM + `templates` emptyDir + `output` emptyDir), extraVolumeMounts, sidecars (k8s-sidecar), resources, probes (tcpSocket), hpa, configMaps (inline config); `gateway` block with Istio enabled, header-match VS route; `tfTemplates: [aws/s3/bucket.tf.tmpl]` (Step 13)
- [x] Create `terraform-parse-service/values-dev.yaml` — `APP_ENV: dev`, `logger.level: debug`, tracing stdout, `hpa.enabled: false`, `tfTemplates: [aws/s3/bucket.tf.tmpl]` (Step 13)
- [x] Create `terraform-parse-service/values-staging.yaml` — `APP_ENV: staging`, OTLP tracing, `hpa.enabled: true` 1–2 replicas, `tfTemplates` includes `aws/s3/rds.tf.tmpl` (Step 13)
- [x] Create `terraform-parse-service/values-prod.yaml` — `APP_ENV: production`, higher resources, HPA 2–6 replicas, memory metric, `destinationRule.enabled: true`, `tfTemplates: [aws/s3/bucket.tf.tmpl]` (Step 13)

### Phase 8 — Verification

- [x] `helm lint app/`
- [x] `helm lint gateway/`
- [x] `helm lint terraform-parse-service/ -f terraform-parse-service/values.yaml`
- [x] `helm lint app/ -f frontend/values.yaml`
- [x] `helm lint app/ -f backend/values.yaml`
- [x] `helm template frontend app/ -f frontend/values.yaml | kubectl apply --dry-run=client -f -`
- [x] `helm template backend app/ -f backend/values.yaml | kubectl apply --dry-run=client -f -`
- [x] `helm template terraform-parse-service terraform-parse-service/ -f terraform-parse-service/values.yaml | kubectl apply --dry-run=client -f -`
- [x] Verify HPA has memory metric in prod: `helm template backend app/ -f backend/values.yaml -f backend/values-prod.yaml | grep -A20 HorizontalPodAutoscaler | grep "name: memory"`
- [x] Verify HPA absent in dev: output of `grep -c HorizontalPodAutoscaler` is `0`
- [x] Verify `tcpSocket` probes for terraform-parse-service
- [x] Verify CM names include release name: `grep "^  name:" | grep terraform-parse-service`
- [x] Verify Istio resources present for terraform-parse-service and absent for frontend/backend
- [x] Verify k8s-sidecar container present in terraform-parse-service pod spec
- [x] Verify `emptyDir` volumes `templates` and `output` present in terraform-parse-service pod spec
- [x] Verify each `tfTemplates` entry produces a separate CM with label `terraform-parse-service/template: "true"`
