# terraform-parse-service

Production chart for the Terraform HCL generation service. Composes the `app` and `gateway` local charts as dependencies and adds:

- A k8s-sidecar container that watches ConfigMaps labelled `terraform-parse-service/template: "true"` and writes their contents into a shared `/templates` volume
- Per-template ConfigMaps generated from files under `files/templates/`, one per entry in `tfTemplates`

## Architecture

```
Pod
├── app container          — serves requests on :8080, reads templates from /templates, writes .tf to /output
└── k8s-sidecar container  — watches labelled ConfigMaps in namespace, syncs data keys (relative paths) to /templates

Volumes (emptyDir)
├── templates              — shared between sidecar (write) and app (read)
└── output                 — generated .tf files written by app

ConfigMaps (per tfTemplate entry)
└── <release>-<slug>-tmpl  — data key = relative path (e.g. aws/s3/bucket.tf.tmpl), value = template content
```

## Prerequisites

- Istio installed in the cluster (Gateway and VirtualService are enabled by default)
- RBAC allowing the sidecar to watch ConfigMaps in the release namespace (managed outside this chart)

## Dependencies

```sh
helm dependency update .
```

This pulls `app` and `gateway` from local `file://` paths into `charts/`.

## Install

```sh
# Base values (production)
helm install terraform-parse-service . -n <namespace>

# With environment overlay
helm install terraform-parse-service . -n <namespace> -f values-staging.yaml
helm install terraform-parse-service . -n <namespace> -f values-prod.yaml
helm install terraform-parse-service . -n <namespace> -f values-dev.yaml
```

## Environment overlays

| File | Key differences |
|---|---|
| `values-dev.yaml` | `log.level: debug`, HPA disabled, dev hostnames |
| `values-staging.yaml` | HPA enabled (1–2 replicas), OTLP tracing to Tempo, staging hostname, adds `rds.tf.tmpl` |
| `values-prod.yaml` | HPA enabled (2–6 replicas), higher resource limits, memory HPA metric, DestinationRule enabled |

## Terraform templates

Templates live in `files/templates/` and are referenced by relative path in `tfTemplates`. Each entry produces a ConfigMap named `<release>-<slug>-tmpl` where the slug is the path with `/` and `.` replaced by `-`.

```yaml
tfTemplates:
  - aws/s3/bucket.tf.tmpl         # → <release>-aws-s3-bucket-tf-tmpl-tmpl
  - aws/s3/rds.tf.tmpl            # → <release>-aws-s3-rds-tf-tmpl-tmpl
```

The ConfigMap data key is the full relative path (`aws/s3/bucket.tf.tmpl`). The k8s-sidecar reconstructs the directory tree under `/templates` from this key, so the app finds templates at `/templates/aws/s3/bucket.tf.tmpl`.

To add a new template:

1. Place the file at `files/templates/<provider>/<resource>.tf.tmpl`
2. Add the relative path to `tfTemplates` in `values.yaml` (and relevant overlay files)

## Key values

### Workload (`app.*`)

All values under `app:` are forwarded to the `app` subchart. See [`../app/README.md`](../app/README.md) for the full reference. Notable overrides for this service:

```yaml
app:
  service:
    port: 8080

  probes:
    type: tcpSocket    # no HTTP health endpoint
    readiness:
      port: 8080
    liveness:
      port: 8080

  extraVolumes:
    - name: templates
      emptyDir: {}
    - name: output
      emptyDir: {}

  sidecars:
    - name: k8s-sidecar
      image: kiwigrid/k8s-sidecar:1.27.5
      env:
        - name: LABEL
          value: "terraform-parse-service/template"
        - name: FOLDER
          value: "/templates"
```

### Istio routing (`gateway.*`)

All values under `gateway:` are forwarded to the `gateway` subchart. See [`../gateway/README.md`](../gateway/README.md) for the full reference.

Gateway and VirtualService are enabled by default. DestinationRule is disabled by default; enable it in `values-prod.yaml` to configure traffic policy.

```yaml
gateway:
  istio:
    enabled: true
    gateway:
      create: true
      hosts:
        - "terraform-parse-service.example.com"
    virtualService:
      enabled: true
      hosts:
        - "terraform-parse-service.example.com"
```

## Tests

```sh
helm unittest --with-subchart=false terraform-parse-service/
```

21 test cases: 8 covering ConfigMap template rendering (slug derivation, labels, data keys, multi-entry), 13 integration cases covering the full rendered stack (sidecar, volumes, probes, volumeMounts, annotations, HPA with staging overlay).
