# app

Generic chart that renders a Deployment, Service, optional HPA, and zero or more ConfigMaps. Designed to be used directly or as a dependency via `file://` reference.

## Resources rendered

| Resource | Condition |
|---|---|
| Deployment | always |
| Service | always |
| HorizontalPodAutoscaler | `hpa.enabled: true` |
| ConfigMap (per entry) | `configMaps` list non-empty |

## Install

```sh
helm install my-app ./app \
  --set image.repository=my-image \
  --set image.tag=1.0.0
```

## Values

### Image

```yaml
image:
  repository: ""   # required
  tag: ""          # required
  pullPolicy: IfNotPresent
```

### Replicas and HPA

When `hpa.enabled` is `true`, `spec.replicas` is omitted from the Deployment so the HPA has sole control.

```yaml
replicaCount: 1

hpa:
  enabled: false
  minReplicas: 1
  maxReplicas: 5
  targetCPUUtilizationPercentage: 50
  targetMemoryUtilizationPercentage: 0   # 0 disables memory metric
```

### Service

```yaml
service:
  type: ClusterIP
  port: 80
```

### Probes

Two probe types supported: `httpGet` (default) and `tcpSocket`. Use `tcpSocket` for services that have no HTTP health endpoint.

```yaml
probes:
  type: httpGet          # or tcpSocket
  readiness:
    path: /              # httpGet only
    port: 80
    initialDelaySeconds: 5
    periodSeconds: 10
  liveness:
    path: /              # httpGet only
    port: 80
    initialDelaySeconds: 15
    periodSeconds: 20
```

### ConfigMaps

Each entry renders a separate ConfigMap named `<release>-<name>`. A `checksum/<name>` annotation is added to the pod template so pods restart on config changes.

```yaml
configMaps:
  - name: config
    data:
      config.yaml:
        key: value
        nested:
          field: example
```

### Extra ports, volumes, and env

```yaml
extraPorts:
  - name: metrics
    containerPort: 9091
    protocol: TCP

extraVolumes:
  - name: my-volume
    emptyDir: {}

extraVolumeMounts:
  - name: my-volume
    mountPath: /data

env:
  - name: APP_ENV
    value: production

args:
  - --flag=value
```

Go template expressions are supported in `extraVolumes`, `extraVolumeMounts`, `env`, and `sidecars` values — they are passed through `tpl` at render time.

### Sidecars

Additional containers appended after the main container:

```yaml
sidecars:
  - name: my-sidecar
    image: busybox:latest
    args: ["/bin/sh", "-c", "sleep infinity"]
```

### Pod annotations

```yaml
podAnnotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "9091"
```

### Name overrides

```yaml
nameOverride: ""       # overrides app.kubernetes.io/name label
fullnameOverride: ""   # overrides all resource names
```

## Tests

```sh
helm unittest app/
```

30 test cases covering: Deployment defaults, HPA replicas omission, tcpSocket probes, args, env, extraPorts, extraVolumes/Mounts, sidecars, podAnnotations, checksum annotation, nameOverride, fullnameOverride, Service defaults and overrides, HPA metrics.
