# gateway

Chart that renders Istio networking resources: Gateway, VirtualService, and DestinationRule. Each resource is independently toggled. All three are disabled by default.

Intended to be used as a dependency (`file://../gateway`) rather than deployed standalone, but it works either way.

## Resources rendered

| Resource | Condition |
|---|---|
| Gateway | `istio.enabled: true` AND `istio.gateway.create: true` |
| VirtualService | `istio.enabled: true` AND `istio.virtualService.enabled: true` |
| DestinationRule | `istio.enabled: true` AND `istio.destinationRule.enabled: true` |

## Install

```sh
helm install my-gateway ./gateway \
  --set istio.enabled=true \
  --set istio.gateway.create=true \
  --set istio.virtualService.enabled=true \
  --set 'istio.virtualService.hosts[0]=my-service.example.com'
```

## Values

### Top-level toggle

```yaml
istio:
  enabled: false   # master switch — disables all resources when false
```

### Gateway

```yaml
istio:
  gateway:
    create: false      # set true to create the Gateway resource
    name: ""           # external gateway name — required when create: false and a VirtualService references it
    port: 80
    hosts:
      - "*"
```

When `gateway.create: false`, any VirtualService that is enabled must provide `istio.gateway.name` — rendering fails with `"istio.gateway.name is required when gateway.create is false"` if it is empty.

### VirtualService

```yaml
istio:
  virtualService:
    enabled: false
    hosts: []
    routes: []   # list of Istio HTTPRoute objects; supports Go template expressions via tpl
```

Example with a header-match route:

```yaml
istio:
  virtualService:
    enabled: true
    hosts:
      - "my-service.example.com"
    routes:
      - match:
          - headers:
              release:
                exact: '{{ .Release.Name }}'
        route:
          - destination:
              host: '{{ .Release.Name }}'
              port:
                number: 8080
```

`routes` is passed through `tpl`, so `{{ .Release.Name }}` and other Helm expressions resolve at render time.

### DestinationRule

```yaml
istio:
  destinationRule:
    enabled: false
    host: ""           # required when enabled — rendering fails if empty
    trafficPolicy: {}  # empty map omits spec.trafficPolicy entirely
```

Example with connection pool settings:

```yaml
istio:
  destinationRule:
    enabled: true
    host: '{{ .Release.Name }}'
    trafficPolicy:
      connectionPool:
        tcp:
          maxConnections: 100
```

`host` is passed through `tpl`.

## Tests

```sh
helm unittest gateway/
```

21 test cases covering: all three resources disabled by default, individual enable flags, required-field guards, `spec.selector`, port/host values, `spec.gateways[0]` for both create and external modes, absent vs present `trafficPolicy`.
