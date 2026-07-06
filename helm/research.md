# Helm Chart Research: `tripla-apps`

## Overview

A single Helm chart (`apiVersion: v2`, type `application`, version `0.1.0`) deploying two services: a frontend (nginx) and a backend (hashicorp/http-echo). The chart contains 5 templates and a `values.yaml`. It has intentional issues per its own description.

**File inventory:**
```
Chart.yaml
values.yaml
templates/
  frontend-deployment.yaml
  backend-deployment.yaml
  frontend-service.yaml
  backend-service.yaml
  hpa.yaml
```

No `_helpers.tpl`, no `NOTES.txt`, no `tests/` directory.

---

## Critical Bugs (Will Break Deployment)

### 1. `backend-deployment.yaml` — Leading backslash invalidates YAML

The file starts with a literal `\` character on line 1, followed by indented content. This is not valid YAML and will cause `helm template` or `helm install` to fail with a parse error.

```yaml
\
    apiVersion: apps/v1
    ...
```

**Fix:** Remove the `\` and strip the 4-space leading indentation from every line.

---

### 2. Both deployments missing `spec.selector`

Kubernetes `Deployment` requires `spec.selector` to identify which pods it owns. Both `frontend-deployment.yaml` and `backend-deployment.yaml` omit this field entirely. Applying either manifest will be rejected by the API server with a validation error.

Required structure:
```yaml
spec:
  selector:
    matchLabels:
      app: <name>
  template:
    metadata:
      labels:
        app: <name>
```

---

### 3. `frontend-service.yaml` selector does not match frontend pod labels

`frontend-deployment.yaml` sets pod label `app: frontend-app`.  
`frontend-service.yaml` selects on `app: frontend`.

These never match. The service will have 0 endpoints. All traffic to `frontend-svc` is silently dropped.

| Resource | Label |
|---|---|
| `frontend-deployment` pod template | `app: frontend-app` |
| `frontend-service` selector | `app: frontend` |

---

### 4. HPA targets CPU utilization but backend container has no resource requests

`hpa.yaml` configures CPU utilization autoscaling (`averageUtilization: 50`) against the `backend` deployment. The backend container defines no `resources.requests.cpu`. Without a CPU request, `metrics-server` cannot compute utilization percentage — HPA will log `FailedGetScale` or `unknown` for the metric and never scale.

---

## Helm Template Issues (Structural / Design)

### 5. `values.yaml` is completely unused — zero Go template expressions in any template

None of the 5 templates reference `.Values.*`. Every value is hardcoded. This makes the chart non-configurable and defeats the purpose of Helm.

Comparison of `values.yaml` vs actual template behavior:

| Field | `values.yaml` | Template |
|---|---|---|
| Frontend image | `nginx:latest` | `nginx:1.16.0` (hardcoded) |
| Backend image | `hashicorp/http-echo:latest` | `hashicorp/http-echo:0.2.3` (hardcoded) |
| Frontend service type | `LoadBalancer` | `ClusterIP` (hardcoded) |
| Backend service type | `ClusterIP` | `ClusterIP` (matches, but still hardcoded) |
| `replicaCount` | `1` | Frontend: `2`, Backend: `1` (both hardcoded) |
| `resources` | `{}` | Not applied anywhere |

### 6. No `_helpers.tpl`

No named templates exist. Resource names, labels, and selectors are copy-pasted literals with no shared definition. Changes require editing every file independently, creating label drift (as already demonstrated by the frontend label mismatch).

### 7. No standard Kubernetes/Helm labels

None of the resources carry recommended labels:
- `app.kubernetes.io/name`
- `app.kubernetes.io/instance`
- `app.kubernetes.io/version`
- `app.kubernetes.io/managed-by`
- `helm.sh/chart`

Without `app.kubernetes.io/managed-by: Helm` and `app.kubernetes.io/instance: {{ .Release.Name }}`, Helm cannot track ownership correctly, and `helm uninstall` may leave orphaned resources.

### 8. No `NOTES.txt`

No post-install instructions are shown to the operator. In a multi-service chart this matters — endpoint URLs, access instructions, and gotchas are normally communicated here.

---

## Operational / Production Readiness Issues

### 9. No resource requests or limits on any container

Neither frontend nor backend defines `resources.requests` or `resources.limits`. Consequences:
- Kubernetes scheduler places pods without capacity hints, risking node overcommit.
- HPA CPU utilization is non-functional (see issue #4).
- Pods can consume unbounded memory and trigger OOM kills on the node.
- No QoS class above `BestEffort` — first evicted under node pressure.

### 10. Mutable image tags (`latest`) in `values.yaml`

`values.yaml` specifies `nginx:latest` and `hashicorp/http-echo:latest`. Mutable tags:
- Break reproducibility — the same Helm install command pulls different binaries at different times.
- Interact poorly with `imagePullPolicy: IfNotPresent` (default) — running pods may silently diverge from newly pulled images.
- Make rollback semantically meaningless.

Note: the templates happen to hardcode pinned tags (`nginx:1.16.0`, `hashicorp/http-echo:0.2.3`), but since values are ignored these pinned versions are not operator-visible or overridable.

### 11. No liveness or readiness probes

Both deployments omit probes. Without a readiness probe, pods are added to service endpoints immediately on container start, before the application is ready to serve traffic. Without a liveness probe, hung or deadlocked processes are never restarted.

### 12. No namespace scoping

No resource sets `metadata.namespace`. All resources deploy to the namespace specified at install time (or `default`). For a shared cluster this is acceptable if enforced by the installer, but a missing explicit namespace in the chart creates ambiguity.

### 13. No `PodDisruptionBudget`

Neither service has a PDB. Node drains (rolling upgrades, spot reclamation) can terminate all replicas simultaneously. With `replicaCount: 1` for backend, any drain causes a full outage.

### 14. Frontend deployment hardcodes `replicas: 2`, ignoring `values.replicaCount`

The backend uses `replicas: 1` (matching `values.replicaCount`) but frontend uses `replicas: 2`. Neither reads from `.Values.replicaCount`. This inconsistency means frontend is always over-provisioned relative to the values default, and there is no single knob to scale both services.

---

## Architecture Summary

```
[LoadBalancer per values.yaml, but ClusterIP in template]
        frontend-svc (port 80) ──► [0 endpoints — label mismatch]
                                           frontend Deployment (replicas: 2, no selector)

[ClusterIP]
        backend-svc (port 8080) ──► backend Deployment (replicas: 1, no selector, YAML broken)
                                           ▲
                                    backend-hpa (CPU 50%, non-functional — no resource requests)
```

In its current state the chart will fail to install due to the YAML parse error in `backend-deployment.yaml` and will fail API validation due to missing `spec.selector` on both deployments. Even if those were fixed, frontend traffic would be dropped due to the label mismatch.

---

## Issue Summary Table

| # | File | Severity | Category | Description |
|---|---|---|---|---|
| 1 | `backend-deployment.yaml` | Critical | Correctness | Leading `\` breaks YAML parse |
| 2 | `frontend-deployment.yaml`, `backend-deployment.yaml` | Critical | Correctness | Missing `spec.selector` |
| 3 | `frontend-service.yaml` | Critical | Correctness | Selector `app: frontend` ≠ pod label `app: frontend-app` |
| 4 | `hpa.yaml` + `backend-deployment.yaml` | High | Correctness | HPA CPU metric non-functional — no resource requests |
| 5 | All templates | High | Design | `values.yaml` entirely unused — no Go template expressions |
| 6 | All templates | Medium | Design | No `_helpers.tpl`, no shared label definitions |
| 7 | All templates | Medium | Design | Missing Helm/K8s recommended labels |
| 8 | All templates | Medium | Operational | No resource requests/limits |
| 9 | `values.yaml` | Medium | Operational | Mutable `latest` tags |
| 10 | All templates | Medium | Operational | No liveness/readiness probes |
| 11 | Chart | Low | Operational | No `PodDisruptionBudget` |
| 12 | Chart | Low | Operational | No `NOTES.txt` |
| 13 | All templates | Low | Operational | No namespace scoping |
