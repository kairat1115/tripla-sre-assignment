# Plan 4 — Init Container for Template Pre-population

## Problem

k8s-sidecar running as a sidecar container watches ConfigMaps continuously. However, on pod start there is a race: the main app container starts simultaneously and may attempt to load Terraform templates from `/templates` before the sidecar has fetched them. The startup probe gives the app 15 seconds (`initialDelaySeconds: 15`, `failureThreshold: 3`, `periodSeconds: 1`), but that window is not guaranteed to be enough depending on API server latency and ConfigMap count.

An init container running k8s-sidecar in one-shot mode (exits after initial sync) eliminates the race entirely — the main app container and the persistent sidecar only start after `/templates` is already populated.

## How k8s-sidecar one-shot mode works

k8s-sidecar supports one-shot execution via the `METHOD` env var:

- `METHOD=LIST` — fetches all matching ConfigMaps once, writes files, exits 0
- Default (`METHOD=WATCH`) — loops forever (normal sidecar behavior)

The init container uses `METHOD=LIST`. All other env vars (`LABEL`, `LABEL_VALUE`, `FOLDER`, `RESOURCE`, `NAMESPACE`) are identical to the persistent sidecar so both target the same ConfigMaps.

## Scope

- `app/` chart: add `initContainers` support (generic, same pattern as `sidecars`)
- `terraform-parse-service/values.yaml`: configure init container using k8s-sidecar in LIST mode

RBAC already covers the init container — it runs under the same ServiceAccount that already has `get/list/watch` on `configmaps`.

## Design decisions

- `initContainers` is a new top-level value in `app/values.yaml`, defaulting to `[]`, rendered via `tpl` (same as `sidecars`) so template references like `{{ include "app.fullname" . }}` work
- Init container is separate from `sidecars` — different lifecycle, different spec position in the Deployment
- `METHOD=LIST` is set only on the init container; the persistent sidecar keeps `METHOD=WATCH` (or omits it, which defaults to WATCH)
- Image and env vars duplicated in values — no abstraction. The init container and sidecar may diverge over time (e.g. different images during upgrades)
- No `resources` block on the init container beyond what k8s-sidecar's image defaults to — same as current sidecar definition. Can be added if needed.

---

## Phase 0 — app chart: initContainers support

### 0.1 Add `initContainers` to `app/values.yaml`

```yaml
initContainers: []
```

Add after `sidecars: []`.

### 0.2 Add initContainers to `app/templates/deployment.yaml`

Add before the `containers:` block, after `volumes:`:

```yaml
      {{- if .Values.initContainers }}
      initContainers:
        {{- tpl (toYaml .Values.initContainers) . | nindent 8 }}
      {{- end }}
```

Full context in deployment.yaml after change:

```yaml
      {{- if .Values.extraVolumes }}
      volumes:
        {{- tpl (toYaml .Values.extraVolumes) . | nindent 8 }}
      {{- end }}
      {{- if .Values.initContainers }}
      initContainers:
        {{- tpl (toYaml .Values.initContainers) . | nindent 8 }}
      {{- end }}
      containers:
        - name: {{ .Chart.Name }}
```

---

## Phase 1 — terraform-parse-service: configure init container

### 1.1 Add `initContainers` to `terraform-parse-service/values.yaml`

Under `app:`, alongside `sidecars:`:

```yaml
  initContainers:
    - name: k8s-sidecar-init
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
        - name: METHOD
          value: "LIST"
        - name: NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
      volumeMounts:
        - name: templates
          mountPath: /templates
```

Key difference from the sidecar: `METHOD: LIST` causes the container to exit after one pass.

---

## Phase 2 — tests

### 2.1 Add `app/tests/deployment_test.yaml` cases

Add two cases to the existing deployment test suite:

```yaml
  - it: no initContainers by default
    asserts:
      - isNull:
          path: spec.template.spec.initContainers

  - it: initContainers rendered when set
    values:
      - fixtures/init-containers.yaml
    asserts:
      - equal:
          path: spec.template.spec.initContainers[0].name
          value: my-init
```

Fixture `app/tests/fixtures/init-containers.yaml`:

```yaml
initContainers:
  - name: my-init
    image: busybox:1.36
    command: ["sh", "-c", "echo init"]
```

### 2.2 Add `terraform-parse-service/tests/integration_test.yaml` cases

```yaml
  - it: initContainers[0] is k8s-sidecar-init
    template: charts/app/templates/deployment.yaml
    asserts:
      - equal:
          path: spec.template.spec.initContainers[0].name
          value: k8s-sidecar-init

  - it: k8s-sidecar-init METHOD env var is LIST
    template: charts/app/templates/deployment.yaml
    asserts:
      - contains:
          path: spec.template.spec.initContainers[0].env
          content:
            name: METHOD
            value: LIST

  - it: k8s-sidecar-init mounts templates volume
    template: charts/app/templates/deployment.yaml
    asserts:
      - contains:
          path: spec.template.spec.initContainers[0].volumeMounts
          content:
            name: templates
            mountPath: /templates
```

---

## TODO

### Phase 0 — app chart initContainers

- [x] Add `initContainers: []` to `app/values.yaml`
- [x] Add `initContainers` block to `app/templates/deployment.yaml`

### Phase 1 — terraform-parse-service init container config

- [x] Add `app.initContainers` block to `terraform-parse-service/values.yaml`

### Phase 2 — tests

- [x] Add 2 deployment test cases to `app/tests/deployment_test.yaml` (no initContainers by default, initContainers rendered when set)
- [x] Create `app/tests/fixtures/init-containers.yaml`
- [x] Add 3 integration test cases to `terraform-parse-service/tests/integration_test.yaml`
- [x] Run `helm unittest app/` — all cases pass
- [x] Run `helm unittest --with-subchart=false terraform-parse-service/` — all cases pass
- [x] Run `ct lint --config ct.yaml --all` — all charts pass
