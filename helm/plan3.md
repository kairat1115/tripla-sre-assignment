# Plan 3 — ServiceAccount + RBAC

## Problem

k8s-sidecar (kiwigrid/k8s-sidecar) watches ConfigMaps by label in the pod namespace. Without a ServiceAccount bound to a Role that allows `get/list/watch` on `configmaps`, the sidecar cannot read the template ConfigMaps and `/templates` stays empty — the app fails to generate any Terraform HCL.

## Scope

- `app/` chart: add generic `ServiceAccount` template, wire it into `Deployment.spec.serviceAccountName`
- `terraform-parse-service/` chart: add `Role` + `RoleBinding` templates that grant the sidecar the minimum required verbs on `configmaps`

RBAC lives in `terraform-parse-service` (not `app`) because the permission is specific to the k8s-sidecar use-case, not generic app behaviour.

## Design decisions

- ServiceAccount creation is opt-in via `serviceAccount.create: true` (default `false` to avoid breaking existing installs that provide their own SA or rely on `default`)
- `serviceAccount.name` overrides the SA name; when empty the SA name falls back to `app.fullname`
- `serviceAccount.annotations` allows IRSA / Workload Identity attachment without touching the template
- `automountServiceAccountToken: false` is a top-level value controlling the pod spec only — the ServiceAccount resource does not set this field, so kubelet's default applies to the SA itself; the pod spec override always wins at the pod level regardless
- `serviceAccount.automountServiceAccountToken` is removed — automount control is intentionally separate from SA identity
- `rbac.create` is a separate flag in `terraform-parse-service/values.yaml` — default `true` because the sidecar always needs it there, but it can be disabled for clusters where RBAC is managed externally
- Role is namespace-scoped (`Role` + `RoleBinding`), not cluster-scoped — sidecar only watches the release namespace

---

## Phase 0 — app chart: ServiceAccount

### 0.1 Add `serviceAccount` block to `app/values.yaml`

```yaml
serviceAccount:
  create: false
  name: ""
  annotations: {}

automountServiceAccountToken: false
```

### 0.2 Add `app/templates/serviceaccount.yaml`

```yaml
{{- if .Values.serviceAccount.create }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "app.serviceAccountName" . }}
  labels:
    {{- include "app.labels" . | nindent 4 }}
  {{- with .Values.serviceAccount.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
```

No `automountServiceAccountToken` field on the ServiceAccount resource. The pod spec controls mounting.

### 0.3 Add `app.serviceAccountName` helper to `app/templates/_helpers.tpl`

```
{{- define "app.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
  {{- default (include "app.fullname" .) .Values.serviceAccount.name }}
{{- else }}
  {{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
```

### 0.4 Wire SA into `app/templates/deployment.yaml`

Add `serviceAccountName` and `automountServiceAccountToken` to `spec.template.spec`:

```yaml
    spec:
      serviceAccountName: {{ include "app.serviceAccountName" . }}
      automountServiceAccountToken: {{ .Values.automountServiceAccountToken }}
      {{- if .Values.extraVolumes }}
      volumes:
```

Both fields set unconditionally — `serviceAccountName` resolves to `"default"` when SA creation disabled; `automountServiceAccountToken` reads from the top-level value (not the `serviceAccount` block).

---

## Phase 1 — terraform-parse-service: RBAC

### 1.1 Add `rbac` block and SA/automount settings to `terraform-parse-service/values.yaml`

```yaml
rbac:
  create: true

app:
  serviceAccount:
    create: true
    name: ""
    annotations: {}
  automountServiceAccountToken: true
```

### 1.2 Update all `terraform-parse-service/values-*.yaml` overlays

All three overlays (`values-dev.yaml`, `values-staging.yaml`, `values-prod.yaml`) must also carry the SA and automount settings so they remain in effect when an overlay is applied. Without this, `helm install -f values-staging.yaml` would reset `serviceAccount.create` and `automountServiceAccountToken` to the `app` chart defaults (`false`).

Add to each overlay under the `app:` block:

```yaml
app:
  serviceAccount:
    create: true
  automountServiceAccountToken: true
```

### 1.2 Add `terraform-parse-service/templates/role.yaml`

Grants the SA `get/list/watch` on `configmaps` in the release namespace — minimum required by k8s-sidecar when `RESOURCE=configmap`.

```yaml
{{- if .Values.rbac.create }}
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ include "app.fullname" . }}-sidecar
  labels:
    {{- include "app.labels" . | nindent 4 }}
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list", "watch"]
{{- end }}
```

### 1.3 Add `terraform-parse-service/templates/rolebinding.yaml`

```yaml
{{- if .Values.rbac.create }}
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ include "app.fullname" . }}-sidecar
  labels:
    {{- include "app.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ include "app.fullname" . }}-sidecar
subjects:
  - kind: ServiceAccount
    name: {{ include "app.serviceAccountName" . }}
    namespace: {{ .Release.Namespace }}
{{- end }}
```

Note: `include "app.serviceAccountName"` resolves because `app` is a subchart and its helpers are available in the parent chart's template context.

---

## Phase 2 — tests

### 2.1 Add `app/tests/serviceaccount_test.yaml`

Cases:
- `serviceAccount.create: false` (default) renders zero documents
- `serviceAccount.create: true` renders one `ServiceAccount`
- SA name defaults to release name when `serviceAccount.name` is empty
- `serviceAccount.name` override used as SA name
- `serviceAccount.annotations` appear in metadata
- SA resource has no `automountServiceAccountToken` field (pod spec controls it)
- Deployment `spec.template.spec.serviceAccountName` equals SA name when `create: true`
- Deployment `spec.template.spec.serviceAccountName` equals `"default"` when `create: false` and no name set
- Deployment `spec.template.spec.automountServiceAccountToken` defaults to `false`
- Deployment `spec.template.spec.automountServiceAccountToken` is `true` when `automountServiceAccountToken: true` set

```yaml
suite: app serviceaccount
templates:
  - templates/serviceaccount.yaml
tests:
  - it: renders zero documents by default
    asserts:
      - hasDocuments:
          count: 0

  - it: renders one ServiceAccount when create is true
    set:
      serviceAccount.create: true
    asserts:
      - hasDocuments:
          count: 1
      - isKind:
          of: ServiceAccount

  - it: SA name defaults to release name
    set:
      serviceAccount.create: true
    asserts:
      - equal:
          path: metadata.name
          value: RELEASE-NAME

  - it: SA name uses serviceAccount.name override
    set:
      serviceAccount.create: true
      serviceAccount.name: my-sa
    asserts:
      - equal:
          path: metadata.name
          value: my-sa

  - it: SA carries annotations
    set:
      serviceAccount.create: true
      serviceAccount.annotations["eks.amazonaws.com/role-arn"]: "arn:aws:iam::123:role/my-role"
    asserts:
      - equal:
          path: metadata.annotations["eks.amazonaws.com/role-arn"]
          value: "arn:aws:iam::123:role/my-role"

  - it: SA resource has no automountServiceAccountToken field
    set:
      serviceAccount.create: true
    asserts:
      - isNull:
          path: automountServiceAccountToken
```

Add to `app/tests/deployment_test.yaml`:

```yaml
  - it: serviceAccountName equals release name when serviceAccount.create is true
    set:
      serviceAccount.create: true
    asserts:
      - equal:
          path: spec.template.spec.serviceAccountName
          value: RELEASE-NAME

  - it: serviceAccountName is default when serviceAccount.create is false
    asserts:
      - equal:
          path: spec.template.spec.serviceAccountName
          value: default

  - it: automountServiceAccountToken defaults to false on pod spec
    asserts:
      - equal:
          path: spec.template.spec.automountServiceAccountToken
          value: false

  - it: automountServiceAccountToken true when value set
    set:
      automountServiceAccountToken: true
    asserts:
      - equal:
          path: spec.template.spec.automountServiceAccountToken
          value: true
```

### 2.2 Add `terraform-parse-service/tests/rbac_test.yaml`

Cases:
- `rbac.create: true` (default) renders Role and RoleBinding
- Role name is `<release>-sidecar`
- Role grants `get/list/watch` on `configmaps`
- RoleBinding `roleRef.name` matches Role name
- RoleBinding subject SA name matches `app.serviceAccountName`
- RoleBinding subject namespace equals release namespace
- `rbac.create: false` renders zero Role documents
- `rbac.create: false` renders zero RoleBinding documents

```yaml
suite: terraform-parse-service rbac
templates:
  - templates/role.yaml
  - templates/rolebinding.yaml
tests:
  - it: Role renders by default
    template: templates/role.yaml
    asserts:
      - hasDocuments:
          count: 1
      - isKind:
          of: Role

  - it: Role name is release-sidecar
    template: templates/role.yaml
    asserts:
      - equal:
          path: metadata.name
          value: RELEASE-NAME-sidecar

  - it: Role grants get list watch on configmaps
    template: templates/role.yaml
    asserts:
      - equal:
          path: rules[0].resources[0]
          value: configmaps
      - contains:
          path: rules[0].verbs
          content: get
      - contains:
          path: rules[0].verbs
          content: list
      - contains:
          path: rules[0].verbs
          content: watch

  - it: RoleBinding renders by default
    template: templates/rolebinding.yaml
    asserts:
      - hasDocuments:
          count: 1
      - isKind:
          of: RoleBinding

  - it: RoleBinding roleRef points to correct Role
    template: templates/rolebinding.yaml
    asserts:
      - equal:
          path: roleRef.name
          value: RELEASE-NAME-sidecar
      - equal:
          path: roleRef.kind
          value: Role

  - it: RoleBinding subject SA name matches app SA
    template: templates/rolebinding.yaml
    asserts:
      - equal:
          path: subjects[0].kind
          value: ServiceAccount
      - equal:
          path: subjects[0].name
          value: RELEASE-NAME

  - it: Role absent when rbac.create false
    template: templates/role.yaml
    set:
      rbac.create: false
    asserts:
      - hasDocuments:
          count: 0

  - it: RoleBinding absent when rbac.create false
    template: templates/rolebinding.yaml
    set:
      rbac.create: false
    asserts:
      - hasDocuments:
          count: 0
```

---

## TODO

### Phase 0 — app chart ServiceAccount

- [x] Add `serviceAccount` block to `app/values.yaml`
- [x] Add `app.serviceAccountName` helper to `app/templates/_helpers.tpl`
- [x] Create `app/templates/serviceaccount.yaml`
- [x] Add `serviceAccountName` field to `app/templates/deployment.yaml` under `spec.template.spec`

### Phase 1 — terraform-parse-service RBAC

- [x] Add `rbac.create: true`, `app.serviceAccount.create: true`, `app.automountServiceAccountToken: true` to `terraform-parse-service/values.yaml`
- [x] Add `app.serviceAccount.create: true` and `app.automountServiceAccountToken: true` to `values-dev.yaml`, `values-staging.yaml`, `values-prod.yaml`
- [x] Create `terraform-parse-service/templates/role.yaml`
- [x] Create `terraform-parse-service/templates/rolebinding.yaml`

### Phase 2 — tests

- [x] Create `app/tests/serviceaccount_test.yaml` (6 cases — SA resource has no automount field)
- [x] Add 4 deployment test cases to `app/tests/deployment_test.yaml` (`serviceAccountName` x2, `automountServiceAccountToken` x2)
- [x] Create `terraform-parse-service/tests/rbac_test.yaml` (8 cases)
- [x] Run `helm unittest app/` — all cases pass
- [x] Run `helm unittest --with-subchart=false terraform-parse-service/` — all cases pass
- [x] Run `ct lint --config ct.yaml --all` — all charts pass
