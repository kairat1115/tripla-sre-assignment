# Implementation Plan: Terraform Parse Service

## 1. Project Layout

Following [golang-standards/project-layout](https://github.com/golang-standards/project-layout) and [12-factor app](https://12factor.net/) principles (config from env, logs as streams, stateless processes):

```
terraform_parse_service/
├── cmd/
│   └── server/
│       └── main.go                    # wires dependencies, registers routes, starts HTTP server
├── internal/
│   ├── config/
│   │   └── config.go                  # loads Config from YAML via go.uber.org/config
│   ├── logger/
│   │   └── logger.go                  # builds *zap.Logger from config.LoggerConfig
│   ├── handler/
│   │   ├── handler.go                 # Terraform interface, Result, Respond, WriteError, Middleware
│   │   └── aws/
│   │       └── s3/
│   │           ├── bucket.go          # BucketHandler: decode, validate, respond
│   │           └── bucket_test.go     # unit tests: handler logic with stub service
│   ├── service/
│   │   └── terraform.go               # template rendering + storage delegation
│   └── storage/
│       ├── storage.go                 # Writer interface
│       └── filesystem.go              # filesystem implementation
├── configs/
│   └── config.yaml                        # default configuration
├── templates/
│   └── aws/
│       └── s3/
│           └── bucket.tf.tmpl         # HCL template for AWS S3 bucket
├── test/
│   ├── helpers_test.go                # shared setup: moduleRoot, newTestServer (for middleware tests)
│   ├── middleware_test.go             # integration tests: routing + Middleware
│   └── aws/
│       └── s3/
│           ├── helpers_test.go        # shared setup: newTestServer for bucket tests
│           └── bucket_test.go         # integration tests: POST /api/aws/v1/s3/buckets
├── go.mod
├── go.sum
└── plan.md
```

All business logic under `internal/`. `internal/handler/handler.go` owns the `Terraform` interface, `Result`/`Respond`/`WriteError` helpers, and `Middleware`. Route registration lives in `main.go` — this keeps `handler.go` free of imports from its own sub-packages, so sub-packages can import `internal/handler` without a cycle. HTTP routing via `net/http` ServeMux using Go 1.22+ method+path patterns.

---

## 2. Dependencies

```bash
go get github.com/Masterminds/sprig/v3
go get go.uber.org/config
go get go.uber.org/zap
```

Everything else: standard library only (`net/http`, `encoding/json`, `text/template`, `os`, `path/filepath`, `io/fs`). External: `github.com/Masterminds/sprig/v3`, `go.uber.org/config`, `go.uber.org/zap`.

---

## 3. Data Flow

```
Startup:
  → config:   load configs/config.yaml into Config (logger, providers)
  → logger:   build *zap.Logger via logger.New(cfg.Logger), replace global with zap.ReplaceGlobals
  → service:  for each provider, load templates from TemplatesDir, init FSWriter at StorageDir

POST /api/aws/v1/s3/buckets
  → handler:  decode JSON body, validate required property keys
  → service:  look up aws writer + aws templates, render s3/bucket.tf.tmpl
  → storage:  write rendered bytes to <AWS_STORAGE_DIR>/s3/<bucket-name>/main.tf
  → handler:  respond 201 Created with { "output_path": "..." }
```

---

## 4. Component Design

### 4.1 Config

#### `configs/config.yaml`

```yaml
listen_addr: ":8080"
logger:
  level: "info"           # debug | info | warn | error
  metadata:
    service: "terraform-parse-service"
    env: "${APP_ENV}"     # resolved from environment at load time
providers:
  aws:
    templates_dir: "./templates/aws"
    storage_dir: "./output/aws"
```

Values may use `${VAR}` syntax — `config.Expand(os.LookupEnv)` resolves them at load time, enabling env-specific overrides without editing the file. Unset vars fall back to the literal value in the YAML.

#### `internal/config/config.go`

```go
package config

import (
    "fmt"
    "os"

    uberconfig "go.uber.org/config"
)

// ProviderConfig holds template source and storage output settings for one cloud provider.
type ProviderConfig struct {
    TemplatesDir string `yaml:"templates_dir"`
    StorageDir   string `yaml:"storage_dir"`
}

// LoggerConfig controls log verbosity and fixed metadata attached to every record.
type LoggerConfig struct {
    Level    string            `yaml:"level"`    // debug | info | warn | error; defaults to info
    Metadata map[string]string `yaml:"metadata"` // key-value pairs added to every log record
}

// Config is the top-level application configuration.
type Config struct {
    ListenAddr string                    `yaml:"listen_addr"`
    Logger     LoggerConfig              `yaml:"logger"`
    Providers  map[string]ProviderConfig `yaml:"providers"`
}

// Load reads configuration from the YAML file at CONFIG_PATH (default: configs/config.yaml).
// Values in the YAML may use ${VAR} syntax; os.LookupEnv resolves them at load time.
func Load() (Config, error) {
    path := os.Getenv("CONFIG_PATH")
    if path == "" {
        path = "configs/config.yaml"
    }
    provider, err := uberconfig.NewYAML(
        uberconfig.File(path),
        uberconfig.Expand(os.LookupEnv),
    )
    if err != nil {
        return Config{}, fmt.Errorf("load config %s: %w", path, err)
    }
    var cfg Config
    if err := provider.Get(uberconfig.Root).Populate(&cfg); err != nil {
        return Config{}, fmt.Errorf("populate config: %w", err)
    }
    return cfg, nil
}
```

`configs/config.yaml` is the single source of configuration structure and defaults. `Load()` reads it via `go.uber.org/config`, selecting the file path from `CONFIG_PATH` (defaults to `configs/config.yaml`). `config.Expand(os.LookupEnv)` enables `${VAR}` interpolation within YAML values for environment-specific overrides without file edits — satisfying 12-factor III at the value level. Adding a new provider requires one new entry under `providers:` in the YAML; no Go code changes.

---

### 4.2 Logger (`internal/logger/logger.go`)

Encapsulates `*zap.Logger` construction so `main.go` stays free of zap configuration details. Accepts `config.LoggerConfig` directly so the caller does not need to know which fields map to which zap options.

```go
package logger

import (
    "fmt"

    "go.uber.org/zap"
    "go.uber.org/zap/zapcore"

    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
)

// New builds a production JSON logger from cfg.
// Fixed metadata fields from cfg.Metadata are attached to every record.
func New(cfg config.LoggerConfig) (*zap.Logger, error) {
    var level zapcore.Level
    if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
        level = zapcore.InfoLevel
    }
    zapCfg := zap.NewProductionConfig()
    zapCfg.Level = zap.NewAtomicLevelWith(level)
    l, err := zapCfg.Build()
    if err != nil {
        return nil, fmt.Errorf("build logger: %w", err)
    }
    fields := make([]zap.Field, 0, len(cfg.Metadata))
    for k, v := range cfg.Metadata {
        fields = append(fields, zap.String(k, v))
    }
    return l.With(fields...), nil
}
```

`zap.NewProductionConfig()` emits JSON to stdout with timestamps and caller info — no additional configuration needed for log aggregator compatibility. `New` is the only exported symbol; callers hold a `*zap.Logger`. `main.go` calls `zap.ReplaceGlobals` for packages that use the global (init-time logs in `main.go` itself); resource handlers receive the logger via constructor injection instead of `zap.L()`. `l.Sync()` must be deferred by the caller to flush buffered log entries on exit.

---

### 4.3 Storage Interface (`internal/storage/storage.go`)

```go
package storage

// Writer persists rendered Terraform configurations.
type Writer interface {
    // Write stores content under a logical name and returns the physical path written.
    Write(name string, content []byte) (string, error)
}
```

`name` is the logical bucket name; the implementation decides the physical path. Returning the path surfaces it to the caller for logging and the HTTP response.

---

### 4.4 Filesystem Implementation (`internal/storage/filesystem.go`)

```go
package storage

import (
    "fmt"
    "os"
    "path/filepath"
)

type FSWriter struct {
    BaseDir string
}

func NewFSWriter(baseDir string) (*FSWriter, error) {
    if err := os.MkdirAll(baseDir, 0o755); err != nil {
        return nil, fmt.Errorf("create base dir: %w", err)
    }
    return &FSWriter{BaseDir: baseDir}, nil
}

// Write creates <BaseDir>/<name>/main.tf, creating intermediate directories as needed.
// Calling Write with the same name overwrites the existing file (idempotent).
func (w *FSWriter) Write(name string, content []byte) (string, error) {
    dir := filepath.Join(w.BaseDir, name)
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return "", fmt.Errorf("mkdir %s: %w", dir, err)
    }
    path := filepath.Join(dir, "main.tf")
    if err := os.WriteFile(path, content, 0o644); err != nil {
        return "", fmt.Errorf("write %s: %w", path, err)
    }
    return path, nil
}
```

Output structure: `<StorageDir>/<service>/<bucket-name>/main.tf` where `StorageDir` is the provider-specific value from `ProviderConfig` and `service` is the AWS service (e.g. `s3`). The service segment prevents collisions when multiple resource types share the same name. Repeated calls overwrite — no versioning at this layer.

---

### 4.5 Template (`templates/aws/s3/bucket.tf.tmpl`)

```hcl
{{- $region  := index .Properties "aws-region" -}}
{{- $bucket  := index .Properties "bucket-name" -}}
{{- $acl     := index .Properties "acl" -}}
{{- $resName := $bucket | replace "-" "_" -}}
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = {{ $region | quote }}
}

resource "aws_s3_bucket" {{ $resName | quote }} {
  bucket = {{ $bucket | quote }}
}

resource "aws_s3_bucket_acl" {{ printf "%s_acl" $resName | quote }} {
  bucket = aws_s3_bucket.{{ $resName }}.id
  acl    = {{ $acl | quote }}
}
```

**Sprig functions used:**
- `quote` — wraps values in double quotes, producing valid HCL string literals
- `replace` — converts `tripla-bucket` → `tripla_bucket` for valid Terraform resource identifiers (hyphens are not allowed in resource names)
- `printf` — composes the ACL resource name

**Extensibility:** future properties are accessed via `index .Properties "<new-key>"` in the template. No Go code changes needed unless server-side validation of the new key is required.

**Key note:** Go template dot notation (`{{ .Properties.aws-region }}`) cannot access hyphenated map keys — `index` is required for all keys in this map.

---

### 4.6 Terraform Service (`internal/service/terraform.go`)

```go
package service

import (
    "bytes"
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "text/template"

    "github.com/Masterminds/sprig/v3"

    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/storage"
)

// TerraformService renders HCL templates and delegates persistence to per-provider Writers.
type TerraformService struct {
    writers   map[string]storage.Writer
    templates map[string]*template.Template
}

// NewTerraformService constructs a service with pre-built per-provider writers and templates.
// Both maps must contain an entry for every provider the service is expected to handle.
func NewTerraformService(writers map[string]storage.Writer, templates map[string]*template.Template) *TerraformService {
    return &TerraformService{writers: writers, templates: templates}
}

// LoadTemplates walks dir, parses every *.tmpl file, and returns the compiled template set.
// Each template is registered under its path relative to dir with forward-slash separators,
// e.g. "s3_bucket.tf.tmpl" (the provider directory is the caller's scope, not part of the name).
func LoadTemplates(dir string) (*template.Template, error) {
    tmpl := template.New("").Funcs(sprig.TxtFuncMap())
    err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
        if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
            return err
        }
        content, readErr := os.ReadFile(path)
        if readErr != nil {
            return fmt.Errorf("read template %s: %w", path, readErr)
        }
        rel, _ := filepath.Rel(dir, path)
        rel = filepath.ToSlash(rel)
        if _, parseErr := tmpl.New(rel).Parse(string(content)); parseErr != nil {
            return fmt.Errorf("parse template %s: %w", rel, parseErr)
        }
        return nil
    })
    if err != nil {
        return nil, fmt.Errorf("load templates from %s: %w", dir, err)
    }
    return tmpl, nil
}

// Generator is implemented by each provider-specific handler to supply the template name,
// storage path, template data, and provider key without constraining the naming structure
// used by any individual provider.
type Generator interface {
    Provider() string     // selects writer + template set; e.g. "aws"
    TemplateName() string // relative path within provider templates; e.g. "s3/bucket.tf.tmpl"
    StoragePath() string  // logical path passed to Writer.Write; e.g. "s3/tripla-bucket"
    TemplateData() any    // data passed to ExecuteTemplate
}

// Generate resolves the provider-scoped writer and template set from g, renders the template,
// and writes the result to storage. Returns the physical path of the written file.
func (s *TerraformService) Generate(g Generator) (string, error) {
    tmpl, ok := s.templates[g.Provider()]
    if !ok {
        return "", fmt.Errorf("no templates registered for provider %s", g.Provider())
    }
    writer, ok := s.writers[g.Provider()]
    if !ok {
        return "", fmt.Errorf("no writer registered for provider %s", g.Provider())
    }
    var buf bytes.Buffer
    if err := tmpl.ExecuteTemplate(&buf, g.TemplateName(), g.TemplateData()); err != nil {
        return "", fmt.Errorf("render template %s: %w", g.TemplateName(), err)
    }
    path, err := writer.Write(g.StoragePath(), buf.Bytes())
    if err != nil {
        return "", fmt.Errorf("write storage: %w", err)
    }
    return path, nil
}
```

Templates are loaded per-provider at startup via `LoadTemplates(pcfg.TemplatesDir)` and cached in the `TerraformService` struct. Each template is registered under its path relative to the provider's `TemplatesDir` (e.g. `s3/bucket.tf.tmpl`). `Generate` accepts a `Generator` interface — the handler is fully responsible for deciding `TemplateName()`, `StoragePath()`, and `TemplateData()`. This means each provider package can use whatever path and naming conventions suit its own resource hierarchy without being constrained by a shared struct. `Generate` returns a clear error if the provider is not registered. To update a template: edit the file on disk and restart — no rebuild.

---

### 4.7 Handler

#### `internal/handler/handler.go`

Owns the `Terraform` service interface, the `Result`/`Respond`/`WriteError` response helpers, and the `Middleware` function. Route registration lives in `main.go` — this means `handler.go` imports nothing from its own sub-packages, so sub-packages can import `internal/handler` freely. The cycle that previously forced `internal/response` to exist is gone.

`Result`/`Respond`/`WriteError` are the same primitives that were in `internal/response`, now co-located with the interface they serve. Inspired by the [Prometheus API pattern](https://github.com/prometheus/prometheus/blob/351447a44b0887c959b74996d2d3367f31293cba/web/api/v1/api.go): handler logic returns a pure `Result`; `ServeHTTP` dispatches it via `Respond`.

```go
package handler

import (
    "bytes"
    "encoding/json"
    "net/http"

    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
)

// Terraform is the service interface used by all resource handlers.
type Terraform interface {
    Generate(g service.Generator) (string, error)
}

// Result is the value returned by handler logic functions.
// ServeHTTP calls Respond to write it to the wire.
type Result struct {
    Code int    // HTTP status to write
    Data any    // serialised as JSON on success (ignored when Err != nil)
    Err  error  // non-nil triggers WriteError; message is logged by the caller
    Msg  string // human-readable error sent to the client when Err != nil
}

// Respond writes r to w. On success it encodes r.Data as JSON with r.Code.
// On error it calls WriteError with r.Code and r.Msg.
func Respond(w http.ResponseWriter, r Result) {
    if r.Err != nil {
        WriteError(w, r.Code, r.Msg)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(r.Code)
    _ = json.NewEncoder(w).Encode(r.Data)
}

// WriteError writes a {"error": msg} JSON body with the given HTTP status code.
func WriteError(w http.ResponseWriter, code int, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(code)
    _ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Middleware intercepts any 4xx/5xx response that lacks Content-Type: application/json
// (e.g. 404 Not Found and 405 Method Not Allowed emitted by ServeMux) and rewrites it
// as a JSON error body, ensuring the API never returns plain-text error responses.
func Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        rw := &responseRecorder{header: w.Header().Clone(), code: http.StatusOK}
        next.ServeHTTP(rw, r)
        if rw.code >= 400 && w.Header().Get("Content-Type") != "application/json" {
            WriteError(w, rw.code, http.StatusText(rw.code))
            return
        }
        for k, v := range rw.header {
            w.Header()[k] = v
        }
        w.WriteHeader(rw.code)
        _, _ = w.Write(rw.body.Bytes())
    })
}

type responseRecorder struct {
    header http.Header
    code   int
    body   bytes.Buffer
}

func (r *responseRecorder) Header() http.Header         { return r.header }
func (r *responseRecorder) WriteHeader(code int)        { r.code = code }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
```

#### `internal/handler/aws/s3/bucket.go`

`bucket.go` imports `internal/handler` directly — no local interface copy, no separate response package. `handler.Terraform` is the single interface declaration; `handler.Result` and `handler.Respond` are the shared response primitives. All request logic lives inline in `ServeHTTP`. `*zap.Logger` is injected via `NewBucketHandler` — no global `zap.L()` calls in the handler. Base log fields (`method`, `path`, `remote_addr`, `status`, `duration_ms`) are built once; path-specific fields are appended before the single `logger.Info`/`logger.Error` call. The log message is the outcome description — `err.Error()` for client errors, `"generation failed"` for 5xx, `"request handled"` for 201 — rather than a fixed `"request handled"` string on all paths. Properties are decoded into a typed `bucketProperties` struct — the three supported fields (`aws-region`, `acl`, `bucket-name`) are explicit in the type; unknown keys are silently ignored by `encoding/json`. `bucketProperties` has a `Validate()` method that returns a descriptive `error` for the first invalid or missing field, or `nil`. Each case returns its own `fmt.Errorf(...)` message so future rules (format checks, allowlist checks, etc.) can carry arbitrary detail without changing the call site. `ServeHTTP` calls `p.Validate()` immediately after decode — decode errors and validation errors are handled in two adjacent `if` blocks, keeping all input-handling logic close together without a separate loop or external helper.

```go
package s3

import (
    "encoding/json"
    "fmt"
    "net/http"
    "time"

    "go.uber.org/zap"

    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler"
    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
)

// bucketGenerator implements service.Generator for AWS S3 bucket creation.
type bucketGenerator struct {
    props bucketProperties
}

func (g *bucketGenerator) Provider() string     { return "aws" }
func (g *bucketGenerator) TemplateName() string { return "s3/bucket.tf.tmpl" }
func (g *bucketGenerator) StoragePath() string  { return "s3/" + g.props.BucketName }
func (g *bucketGenerator) TemplateData() any {
    return map[string]string{
        "aws-region":  g.props.Region,
        "acl":         g.props.ACL,
        "bucket-name": g.props.BucketName,
    }
}

type bucketProperties struct {
    Region     string `json:"aws-region"`
    ACL        string `json:"acl"`
    BucketName string `json:"bucket-name"`
}

// Validate returns a descriptive error for the first invalid or missing field, or nil.
func (p bucketProperties) Validate() error {
    switch {
    case p.Region == "":
        return fmt.Errorf("missing required property: aws-region")
    case p.ACL == "":
        return fmt.Errorf("missing required property: acl")
    case p.BucketName == "":
        return fmt.Errorf("missing required property: bucket-name")
    default:
        return nil
    }
}

type bucketRequest struct {
    Payload struct {
        Properties bucketProperties `json:"properties"`
    } `json:"payload"`
}

type bucketResponse struct {
    OutputPath string `json:"output_path"`
}

// BucketHandler handles POST /api/aws/v1/s3/buckets.
type BucketHandler struct {
    svc    handler.Terraform
    logger *zap.Logger
}

func NewBucketHandler(svc handler.Terraform, logger *zap.Logger) *BucketHandler {
    return &BucketHandler{svc: svc, logger: logger}
}

func (h *BucketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    start := time.Now()

    base := []zap.Field{
        zap.String("method", r.Method),
        zap.String("path", r.URL.Path),
        zap.String("remote_addr", r.RemoteAddr),
    }

    var req bucketRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        h.logger.Info(err.Error(), append(base,
            zap.Int("status", http.StatusBadRequest),
            zap.Int64("duration_ms", time.Since(start).Milliseconds()),
        )...)
        handler.Respond(w, handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
        return
    }

    p := req.Payload.Properties
    if err := p.Validate(); err != nil {
        msg := err.Error()
        h.logger.Info(msg, append(base,
            zap.Int("status", http.StatusUnprocessableEntity),
            zap.String("aws-region", p.Region),
            zap.String("acl", p.ACL),
            zap.String("bucket-name", p.BucketName),
            zap.Int64("duration_ms", time.Since(start).Milliseconds()),
        )...)
        handler.Respond(w, handler.Result{Code: http.StatusUnprocessableEntity, Msg: msg, Err: err})
        return
    }

    gen := &bucketGenerator{props: p}
    outputPath, err := h.svc.Generate(gen)
    if err != nil {
        h.logger.Error("generation failed", append(base,
            zap.Int("status", http.StatusInternalServerError),
            zap.String("aws-region", p.Region),
            zap.String("acl", p.ACL),
            zap.String("bucket-name", p.BucketName),
            zap.String("error", err.Error()),
            zap.Int64("duration_ms", time.Since(start).Milliseconds()),
        )...)
        handler.Respond(w, handler.Result{Code: http.StatusInternalServerError, Msg: "generation failed", Err: err})
        return
    }

    h.logger.Info("request handled", append(base,
        zap.Int("status", http.StatusCreated),
        zap.String("aws-region", p.Region),
        zap.String("acl", p.ACL),
        zap.String("bucket-name", p.BucketName),
        zap.String("output_path", outputPath),
        zap.Int64("duration_ms", time.Since(start).Milliseconds()),
    )...)
    handler.Respond(w, handler.Result{Code: http.StatusCreated, Data: bucketResponse{OutputPath: outputPath}})
}
```

**Wide log event:** one `h.logger` call per terminal code path in `ServeHTTP`. The log message is the primary description of the outcome — `err.Error()` on client errors, `"generation failed"` on 5xx, `"request handled"` on success. Each property field is logged by name (`aws-region`, `acl`, `bucket-name`) — typed struct fields mean no dynamic key lookup:

| Field | Present on | Purpose |
|---|---|---|
| `method` | all paths | route/verb identification |
| `path` | all paths | endpoint identification |
| `remote_addr` | all paths | client tracing |
| `status` | all paths | outcome filtering |
| `aws-region`, `acl`, `bucket-name` | 422 + 5xx + success | individual typed property fields; named explicitly so log queries filter by field |
| `error` | 5xx paths | internal error string from the service |
| `output_path` | success only | where the file landed |
| `duration_ms` | all paths | latency tracking |

**Log level:** `Info` for 400 and 422 (client-caused; the message is the error description) and for 201 success; `Error` for 5xx (unexpected internal failure).

---

### 4.8 Entrypoint (`cmd/server/main.go`)

```go
package main

import (
    "net/http"
    "os"
    "text/template"

    "go.uber.org/zap"

    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler"
    s3handler "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler/aws/s3"
    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/logger"
    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/storage"
)

func main() {
    // bootstrap a minimal JSON logger for config-load errors
    bootstrap, _ := zap.NewProduction()
    defer bootstrap.Sync()

    cfg, err := config.Load()
    if err != nil {
        bootstrap.Error("config load failed", zap.String("error", err.Error()))
        os.Exit(1)
    }

    l, err := logger.New(cfg.Logger)
    if err != nil {
        bootstrap.Error("logger init failed", zap.String("error", err.Error()))
        os.Exit(1)
    }
    defer l.Sync()
    zap.ReplaceGlobals(l)

    writers := make(map[string]storage.Writer)
    templates := make(map[string]*template.Template)
    for provider, pcfg := range cfg.Providers {
        w, err := storage.NewFSWriter(pcfg.StorageDir)
        if err != nil {
            zap.L().Error("storage init failed", zap.String("provider", provider), zap.String("error", err.Error()))
            os.Exit(1)
        }
        tmpl, err := service.LoadTemplates(pcfg.TemplatesDir)
        if err != nil {
            zap.L().Error("template load failed", zap.String("provider", provider), zap.String("error", err.Error()))
            os.Exit(1)
        }
        writers[provider] = w
        templates[provider] = tmpl
    }

    tfSvc := service.NewTerraformService(writers, templates)

    mux := http.NewServeMux()
    mux.Handle("POST /api/aws/v1/s3/buckets", s3handler.NewBucketHandler(tfSvc, l))

    providerNames := make([]string, 0, len(cfg.Providers))
    for p := range cfg.Providers {
        providerNames = append(providerNames, p)
    }
    zap.L().Info("server starting",
        zap.String("addr", cfg.ListenAddr),
        zap.Strings("providers", providerNames),
    )
    if err := http.ListenAndServe(cfg.ListenAddr, handler.Middleware(mux)); err != nil {
        zap.L().Error("server exited", zap.String("error", err.Error()))
        os.Exit(1)
    }
}
```

`config.Load()` is the single entry point for all configuration. `main.go` contains no raw `os.Getenv` calls (only `CONFIG_PATH` is read inside `config.Load()`). `logger.New` constructs the production zap logger from `cfg.Logger`; `zap.ReplaceGlobals` sets the global for `main.go`'s own startup/error logs; resource handlers receive the logger via constructor injection (`NewBucketHandler(tfSvc, l)`). `defer l.Sync()` ensures buffered entries are flushed on exit. Per-provider initialization loops over `cfg.Providers` silently (errors exit immediately). Route registration lives in `main.go` via direct `mux.Handle` calls — adding a new resource handler means adding one line here. `handler.Middleware` wraps the mux. A single wide `"server starting"` event is emitted after all providers are wired, carrying `addr` and `providers`.

---

### 4.9 Tests

Two test layers, scoped exclusively to business logic — no tests for `config.Load()`, logger init, or `FSWriter` in isolation.

#### Unit tests (`internal/handler/aws/s3/bucket_test.go`)

Test `BucketHandler.ServeHTTP` via `httptest.NewRecorder`. The stub satisfies `handler.Terraform` — no real filesystem or templates.

```go
package s3

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "testing"

    "go.uber.org/zap"

    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
)

// stubTerraform satisfies handler.Terraform for testing.
type stubTerraform struct {
    path string
    err  error
}

func (s *stubTerraform) Generate(_ service.Generator) (string, error) {
    return s.path, s.err
}

func TestBucketHandler_BadJSON(t *testing.T) {
    h := NewBucketHandler(&stubTerraform{}, zap.NewNop())
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/api/aws/v1/s3/buckets", bytes.NewBufferString("{bad"))

    h.ServeHTTP(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Fatalf("want 400, got %d", rec.Code)
    }
    var body map[string]string
    _ = json.NewDecoder(rec.Body).Decode(&body)
    if body["error"] != "invalid JSON" {
        t.Fatalf("unexpected error: %s", body["error"])
    }
}

func TestBucketHandler_MissingProperty(t *testing.T) {
    h := NewBucketHandler(&stubTerraform{}, zap.NewNop())
    body := `{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private"}}}`
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/api/aws/v1/s3/buckets", bytes.NewBufferString(body))

    h.ServeHTTP(rec, req)

    if rec.Code != http.StatusUnprocessableEntity {
        t.Fatalf("want 422, got %d", rec.Code)
    }
}

func TestBucketHandler_GenerationError(t *testing.T) {
    h := NewBucketHandler(&stubTerraform{err: fmt.Errorf("render failed")}, zap.NewNop())
    body := `{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":"b"}}}`
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/api/aws/v1/s3/buckets", bytes.NewBufferString(body))

    h.ServeHTTP(rec, req)

    if rec.Code != http.StatusInternalServerError {
        t.Fatalf("want 500, got %d", rec.Code)
    }
}

func TestBucketHandler_Success(t *testing.T) {
    h := NewBucketHandler(&stubTerraform{path: "/out/s3/b/main.tf"}, zap.NewNop())
    body := `{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":"b"}}}`
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/api/aws/v1/s3/buckets", bytes.NewBufferString(body))

    h.ServeHTTP(rec, req)

    if rec.Code != http.StatusCreated {
        t.Fatalf("want 201, got %d", rec.Code)
    }
    var resp bucketResponse
    _ = json.NewDecoder(rec.Body).Decode(&resp)
    if resp.OutputPath != "/out/s3/b/main.tf" {
        t.Fatalf("unexpected output_path: %s", resp.OutputPath)
    }
}
```

**Stub note:** `stubTerraform.Generate` returns the preconfigured `path`/`err` pair. For the 422 path, the validation loop returns before `Generate` is called — the stub is never reached.

**Cases covered:**

| Test | Input | Expected |
|---|---|---|
| `TestBucketHandler_BadJSON` | malformed JSON body | 400 + `{"error":"invalid JSON"}` |
| `TestBucketHandler_MissingProperty` | missing `bucket-name` | 422 |
| `TestBucketHandler_GenerationError` | stub returns error | 500 |
| `TestBucketHandler_Success` | valid payload, stub succeeds | 201 + `{"output_path":"/out/s3/b/main.tf"}` |

---

#### Integration tests (`test/`)

Located in `test/` per [golang-standards/project-layout#test](https://github.com/golang-standards/project-layout#test). Split to mirror the `internal/handler/aws/s3/` pattern: middleware concerns at `test/`, bucket concerns at `test/aws/s3/`. Each directory is a separate Go package with its own shared setup.

`go test` sets the working directory to the package directory, so relative paths do not resolve. Both `helpers_test.go` files use `runtime.Caller(0)` to derive the module root from the file's own compile-time path.

- `test/` — package `integration_test`; tests routing and `handler.Middleware` behavior
- `test/aws/s3/` — package `s3_test`; tests the bucket endpoint HTTP contract and rendered output

##### `test/helpers_test.go`

Shared setup for `test/middleware_test.go`. No test functions.

```go
package integration_test

import (
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "runtime"
    "testing"
    "text/template"

    "go.uber.org/zap"

    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler"
    s3handler "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler/aws/s3"
    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/storage"
)

func moduleRoot() string {
    _, file, _, _ := runtime.Caller(0)
    return filepath.Join(filepath.Dir(file), "..")
}

func newTestServer(t *testing.T) *httptest.Server {
    t.Helper()
    writer, err := storage.NewFSWriter(t.TempDir())
    if err != nil {
        t.Fatalf("storage init: %v", err)
    }
    tmpl, err := service.LoadTemplates(filepath.Join(moduleRoot(), "templates", "aws"))
    if err != nil {
        t.Fatalf("template load: %v", err)
    }
    tfSvc := service.NewTerraformService(
        map[string]storage.Writer{"aws": writer},
        map[string]*template.Template{"aws": tmpl},
    )
    mux := http.NewServeMux()
    mux.Handle("POST /api/aws/v1/s3/buckets", s3handler.NewBucketHandler(tfSvc, zap.NewNop()))
    return httptest.NewServer(handler.Middleware(mux))
}
```

##### `test/middleware_test.go`

Tests routing and `handler.Middleware` behavior — independent of any specific resource handler.

```go
package integration_test

import (
    "encoding/json"
    "net/http"
    "testing"
)

func TestIntegration_MethodNotAllowed(t *testing.T) {
    srv := newTestServer(t)
    defer srv.Close()

    resp, err := http.Get(srv.URL + "/api/aws/v1/s3/buckets")
    if err != nil {
        t.Fatalf("request: %v", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusMethodNotAllowed {
        t.Fatalf("want 405, got %d", resp.StatusCode)
    }
    if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
        t.Fatalf("want Content-Type application/json, got %s", ct)
    }
    var result map[string]string
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if result["error"] != "Method Not Allowed" {
        t.Fatalf("unexpected error: %s", result["error"])
    }
}

func TestIntegration_NotFound(t *testing.T) {
    srv := newTestServer(t)
    defer srv.Close()

    resp, err := http.Get(srv.URL + "/api/aws/v1/s3/unknown")
    if err != nil {
        t.Fatalf("request: %v", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusNotFound {
        t.Fatalf("want 404, got %d", resp.StatusCode)
    }
    if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
        t.Fatalf("want Content-Type application/json, got %s", ct)
    }
    var result map[string]string
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if result["error"] != "Not Found" {
        t.Fatalf("unexpected error: %s", result["error"])
    }
}
```

##### `test/aws/s3/helpers_test.go`

Shared setup for `test/aws/s3/bucket_test.go`. Returns `storageDir` so bucket tests can assert `output_path` independently. No test functions.

```go
package s3_test

import (
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "runtime"
    "testing"
    "text/template"

    "go.uber.org/zap"

    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler"
    s3handler "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler/aws/s3"
    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
    "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/storage"
)

func moduleRoot() string {
    _, file, _, _ := runtime.Caller(0)
    // test/aws/s3/ is three levels below the module root
    return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func newTestServer(t *testing.T) (*httptest.Server, string) {
    t.Helper()
    storageDir := t.TempDir()
    writer, err := storage.NewFSWriter(storageDir)
    if err != nil {
        t.Fatalf("storage init: %v", err)
    }
    tmpl, err := service.LoadTemplates(filepath.Join(moduleRoot(), "templates", "aws"))
    if err != nil {
        t.Fatalf("template load: %v", err)
    }
    tfSvc := service.NewTerraformService(
        map[string]storage.Writer{"aws": writer},
        map[string]*template.Template{"aws": tmpl},
    )
    mux := http.NewServeMux()
    mux.Handle("POST /api/aws/v1/s3/buckets", s3handler.NewBucketHandler(tfSvc, zap.NewNop()))
    return httptest.NewServer(handler.Middleware(mux)), storageDir
}
```

##### `test/aws/s3/bucket_test.go`

Tests for `POST /api/aws/v1/s3/buckets` — HTTP contract, validation, and rendered file content.

```go
package s3_test

import (
    "encoding/json"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestIntegration_CreateBucket_Success(t *testing.T) {
    srv, storageDir := newTestServer(t)
    defer srv.Close()

    body := strings.NewReader(`{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":"tripla-bucket"}}}`)
    resp, err := http.Post(srv.URL+"/api/aws/v1/s3/buckets", "application/json", body)
    if err != nil {
        t.Fatalf("request: %v", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusCreated {
        t.Fatalf("want 201, got %d", resp.StatusCode)
    }

    var result struct {
        OutputPath string `json:"output_path"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        t.Fatalf("decode response: %v", err)
    }

    wantPath := filepath.Join(storageDir, "s3", "tripla-bucket", "main.tf")
    if result.OutputPath != wantPath {
        t.Fatalf("want output_path %s, got %s", wantPath, result.OutputPath)
    }

    got, err := os.ReadFile(result.OutputPath)
    if err != nil {
        t.Fatalf("read output file: %v", err)
    }

    const want = `terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "eu-west-1"
}

resource "aws_s3_bucket" "tripla_bucket" {
  bucket = "tripla-bucket"
}

resource "aws_s3_bucket_acl" "tripla_bucket_acl" {
  bucket = aws_s3_bucket.tripla_bucket.id
  acl    = "private"
}
`
    if string(got) != want {
        t.Fatalf("file content mismatch\nwant:\n%s\ngot:\n%s", want, got)
    }
}

func TestIntegration_CreateBucket_MissingProperty(t *testing.T) {
    srv, _ := newTestServer(t)
    defer srv.Close()

    body := strings.NewReader(`{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private"}}}`)
    resp, err := http.Post(srv.URL+"/api/aws/v1/s3/buckets", "application/json", body)
    if err != nil {
        t.Fatalf("request: %v", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusUnprocessableEntity {
        t.Fatalf("want 422, got %d", resp.StatusCode)
    }

    var result map[string]string
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if result["error"] != "missing required property: bucket-name" {
        t.Fatalf("unexpected error: %s", result["error"])
    }
}
```

**Cases covered:**

| Package | File | Test | Verifies |
|---|---|---|---|
| `integration_test` | `middleware_test.go` | `TestIntegration_MethodNotAllowed` | 405 response; `handler.Middleware` rewrites Content-Type and body to JSON |
| `integration_test` | `middleware_test.go` | `TestIntegration_NotFound` | 404 response; `handler.Middleware` rewrites Content-Type and body to JSON |
| `s3_test` | `bucket_test.go` | `TestIntegration_CreateBucket_Success` | 201 response; `output_path` matches expected path; rendered file content equals expected HCL exactly |
| `s3_test` | `bucket_test.go` | `TestIntegration_CreateBucket_MissingProperty` | 422 response; JSON error body contains the exact missing property name |

`moduleRoot()` in `test/aws/s3/helpers_test.go` walks up three directory levels (`../../..`) to reach the module root — different from `test/helpers_test.go` which walks up one level. Both use `runtime.Caller(0)` so the path is resolved at compile time regardless of CWD.

Integration tests use real templates and real filesystem storage isolated to `t.TempDir()`. Run with `go test ./test/...` from the module root (covers both packages).

---

## 5. Build and Run

```bash
# install dependencies
go get github.com/Masterminds/sprig/v3

# build
go build -o server ./cmd/server

# run (uses configs/config.yaml defaults)
./server

# run with a different config file
CONFIG_PATH=/etc/terraform-parse/config.yaml ./server

# override individual values via ${VAR} interpolation in config.yaml
AWS_STORAGE_DIR=/mnt/output/aws ./server

# test
curl -s -X POST http://localhost:8080/api/aws/v1/s3/buckets \
  -H "Content-Type: application/json" \
  -d '{
    "payload": {
      "properties": {
        "aws-region": "eu-west-1",
        "acl": "private",
        "bucket-name": "tripla-bucket"
      }
    }
  }'

# expected response
# {"output_path":"./output/aws/s3/tripla-bucket/main.tf"}  # AWS_STORAGE_DIR/s3/bucket-name/main.tf

# expected file content at ./output/aws/s3/tripla-bucket/main.tf
```

---

## 6. Expected Output File

For the example request, `./output/aws/s3/tripla-bucket/main.tf` contains:

```hcl
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "eu-west-1"
}

resource "aws_s3_bucket" "tripla_bucket" {
  bucket = "tripla-bucket"
}

resource "aws_s3_bucket_acl" "tripla_bucket_acl" {
  bucket = aws_s3_bucket.tripla_bucket.id
  acl    = "private"
}
```

---

## 7. Key Design Decisions

| Decision | Rationale |
|---|---|
| YAML config via `go.uber.org/config` | Structure and defaults in `configs/config.yaml`; `config.Expand(os.LookupEnv)` enables `${VAR}` overrides per 12-factor III without file edits; file path from `CONFIG_PATH` env var |
| Per-provider `ProviderConfig` | Each provider is one entry under `providers:` in the YAML; adding a provider requires no Go code changes |
| `Generator` interface (not shared struct) | Each handler owns its `TemplateName()`, `StoragePath()`, `TemplateData()` — providers with different naming hierarchies (e.g. GCP) are not constrained by AWS conventions |
| Typed `bucketProperties` struct with `Validate()` | Explicit field names make the contract visible at the type level; unknown keys are dropped by `encoding/json`; `Validate()` returns a descriptive `error` (or `nil`) so each case carries its own message — future rules can vary the message without changing the call site; `TemplateData()` converts back to `map[string]string` for the template engine |
| Filesystem templates cached in memory | Templates loaded from per-provider `TemplatesDir` at startup; update a template by editing the file and restarting — no rebuild needed |
| `templates/<provider>/<service>/<resource>.tf.tmpl` layout | Mirrors URL structure `/api/<provider>/v1/<service>/<resource>`; service is first-class in URL, file path, template name, and storage subpath |
| Service-scoped storage path (`<service>/<name>`) | Prevents collisions when two resource types in the same provider share a name (e.g. S3 bucket `foo` vs future RDS instance `foo`) |
| `filepath.WalkDir` + `tmpl.New(rel).Parse(...)` | Explicit relative-path name registration ensures `ExecuteTemplate` names are stable regardless of working directory |
| `index .Properties "key"` in template | Hyphenated keys (`aws-region`) are incompatible with Go template dot notation |
| Sprig `quote` + `replace` | `quote` produces HCL-safe string literals; `replace` ensures valid Terraform resource identifiers |
| `internal/handler/<provider>/<service>/` layout | Each service under a provider gets its own sub-package; mirrors the URL path and template directory structure; adding a new service requires no changes to existing handler code |
| `storage.Writer` interface | Swapping filesystem → S3/GCS requires only a new struct implementing `Write` — handler and service are unchanged |
| Go 1.22 `ServeMux` method patterns | Eliminates router dependency; method mismatch handled natively with correct 405 response |
| `handler.Middleware` in `handler.go`; route registration in `main.go` | `Middleware` belongs in `handler` (it wraps the mux); routes belong in `main.go` (they compose the mux). Keeping routes in `main.go` is the trade-off: adding a route requires a `main.go` edit, but it eliminates the `handler→s3` import that previously forced a separate `response` package and a duplicate local interface in every sub-package. |
| `go.uber.org/zap` JSON logger | Production zap logger emits JSON to stdout; zero-allocation field types (`zap.String`, `zap.Int`, `zap.Any`) have lower CPU overhead than `slog` for high-throughput paths; resource handlers receive `*zap.Logger` via constructor injection — no global `zap.L()` in handler code; base fields built once per request, path-specific fields appended |
| `internal/logger` package | Isolates zap construction from `main.go`; `logger.New(cfg.LoggerConfig)` is the single place that maps config fields to zap options — level parsing, field attachment, and production defaults are not scattered across the entrypoint |
| Logger config (`level` + `metadata`) | Level set via YAML (default `info`); fixed metadata (e.g. `service`, `env`) attached via `l.With()` at construction — no per-call overhead; log aggregators filter and group by these fields |
| `handler.Terraform`, `handler.Result`, `handler.Respond`, `handler.WriteError` in `internal/handler/handler.go`; route registration in `main.go` | Moving `mux.Handle` calls to `main.go` severs the `handler→s3` import. Sub-packages can now import `internal/handler` freely — no cycle, no duplicate interface, no separate response package. All logic lives inline in `ServeHTTP`; `handler.Respond` writes the result. Unit tests use `httptest.NewRecorder` against `ServeHTTP`. |
| Integration tests in `test/` | Per golang-standards layout; `runtime.Caller(0)` derives module root so template paths resolve regardless of CWD; `t.TempDir()` isolates storage per run |

---

## 8. Extension Points

- **New properties:** add the field to `bucketProperties`, add the corresponding case to `Validate()` returning a descriptive `fmt.Errorf(...)`, add the field to the `TemplateData()` map in `bucketGenerator`, and add `index .Properties "<new-key>"` to the template
- **New providers:** add an entry under `providers:` in `configs/config.yaml`, add a handler sub-package; `main.go` loop picks it up automatically — no Go code changes
- **New storage backends:** implement `storage.Writer`; wire in `main.go` based on a new config field (e.g. `StorageType`)
- **New resource types under existing service:** add `<AWS_TEMPLATES_DIR>/s3/<resource>.tf.tmpl` and restart; no code changes
- **New services:** add a handler route `/api/aws/v1/<service>/...`, a template dir `<AWS_TEMPLATES_DIR>/<service>/`, and define a new concrete `Generator` type in the handler package — no changes to `TerraformService`
- **Validation:** add property-specific validation (e.g. region allow-list) in the handler before calling the service
- **New handler unit tests:** add cases to `bucket_test.go` using `stubTerraform` — no test infrastructure changes needed
- **New bucket integration test cases:** add test functions to `test/aws/s3/bucket_test.go`; `newTestServer` from `test/aws/s3/helpers_test.go` is shared across the package
- **New routing test cases:** add test functions to `test/middleware_test.go`
- **New S3 resource handler:** add `internal/handler/aws/s3/<resource>.go` importing `internal/handler` for `handler.Terraform`, `handler.Result`, and `handler.Respond`; add a `mux.Handle` call in `main.go`; add `test/aws/s3/<resource>_test.go` to the existing test package
- **New service handler (non-S3):** add `internal/handler/aws/<service>/<resource>.go` importing `internal/handler` for `handler.Terraform`, `handler.Result`, and `handler.Respond`; add a `mux.Handle` call in `main.go`; add `test/aws/<service>/helpers_test.go` + `test/aws/<service>/<resource>_test.go`

---

## 9. TODO

### Phase 1 — Module Scaffold ✅

- [x] Run `go mod init github.com/kairat1115/tripla-sre-assignment/terraform_parse_service`
- [x] Run `go get github.com/Masterminds/sprig/v3 go.uber.org/config go.uber.org/zap`
- [x] Create directory tree: `cmd/server/`, `internal/config/`, `internal/logger/`, `internal/handler/aws/s3/`, `internal/service/`, `internal/storage/`, `configs/`, `templates/aws/s3/`, `test/aws/s3/`

### Phase 2 — Configuration ✅

- [x] Write `configs/config.yaml` with `listen_addr`, `logger` (level + metadata), and `providers.aws` (templates_dir + storage_dir)
- [x] Write `internal/config/config.go`: `ProviderConfig`, `LoggerConfig`, `Config`, `Load()` with `go.uber.org/config` and `os.LookupEnv` expansion

### Phase 3 — Logger ✅

- [x] Write `internal/logger/logger.go`: `New(cfg config.LoggerConfig) (*zap.Logger, error)` — production JSON config, level parsing, `l.With(metadata fields)`

### Phase 4 — Storage ✅

- [x] Write `internal/storage/storage.go`: `Writer` interface with `Write(name string, content []byte) (string, error)`
- [x] Write `internal/storage/filesystem.go`: `FSWriter` struct, `NewFSWriter(baseDir string) (*FSWriter, error)`, `Write` creating `<BaseDir>/<name>/main.tf` via `os.MkdirAll` + `os.WriteFile`

### Phase 5 — Template ✅

- [x] Write `templates/aws/s3/bucket.tf.tmpl`: `terraform {}` block, `provider "aws"`, `aws_s3_bucket`, `aws_s3_bucket_acl` using sprig `quote`, `replace`, `printf` and `index .Properties`

### Phase 6 — Service ✅

- [x] Write `internal/service/terraform.go`:
  - [x] `Generator` interface (`Provider`, `TemplateName`, `StoragePath`, `TemplateData`)
  - [x] `TerraformService` struct with `writers` and `templates` maps
  - [x] `NewTerraformService` constructor
  - [x] `LoadTemplates(dir string) (*template.Template, error)` — `filepath.WalkDir`, sprig func map, relative-path name registration
  - [x] `Generate(g Generator) (string, error)` — provider lookup, `ExecuteTemplate`, `Writer.Write`

### Phase 7 — Handler ✅

- [x] Write `internal/handler/handler.go`:
  - [x] `Terraform` interface
  - [x] `Result` struct (`Code`, `Data`, `Err`, `Msg`)
  - [x] `Respond` function
  - [x] `WriteError` function
  - [x] `Middleware` function with `responseRecorder`
- [x] Write `internal/handler/aws/s3/bucket.go`:
  - [x] `bucketProperties` struct with JSON tags
  - [x] `Validate() error` method
  - [x] `bucketRequest` and `bucketResponse` structs
  - [x] `bucketGenerator` struct implementing `service.Generator`
  - [x] `BucketHandler` struct and `NewBucketHandler` constructor
  - [x] `ServeHTTP`: decode, validate, generate, respond — with wide log events per path

### Phase 8 — Entrypoint ✅

- [x] Write `cmd/server/main.go`:
  - [x] Bootstrap logger for pre-config errors
  - [x] `config.Load()`
  - [x] `logger.New` + `zap.ReplaceGlobals`
  - [x] Per-provider loop: `storage.NewFSWriter` + `service.LoadTemplates`
  - [x] `service.NewTerraformService`
  - [x] `mux.Handle("POST /api/aws/v1/s3/buckets", ...)`
  - [x] `handler.Middleware(mux)` passed to `http.ListenAndServe`

### Phase 9 — Unit Tests ✅

- [x] Write `internal/handler/aws/s3/bucket_test.go` (package `s3`):
  - [x] `stubTerraform` implementing `handler.Terraform`
  - [x] `TestBucketHandler_BadJSON` — 400 + `{"error":"invalid JSON"}`
  - [x] `TestBucketHandler_MissingProperty` — 422
  - [x] `TestBucketHandler_GenerationError` — 500
  - [x] `TestBucketHandler_Success` — 201 + correct `output_path`

### Phase 10 — Integration Tests ✅

- [x] Write `test/helpers_test.go` (package `integration_test`): `moduleRoot()`, `newTestServer(t)`
- [x] Write `test/middleware_test.go`:
  - [x] `TestIntegration_MethodNotAllowed` — 405 + JSON body
  - [x] `TestIntegration_NotFound` — 404 + JSON body
- [x] Write `test/aws/s3/helpers_test.go` (package `s3_test`): `moduleRoot()` (three levels up), `newTestServer(t) (*httptest.Server, string)`
- [x] Write `test/aws/s3/bucket_test.go`:
  - [x] `TestIntegration_CreateBucket_Success` — 201, correct `output_path`, exact HCL file content
  - [x] `TestIntegration_CreateBucket_MissingProperty` — 422 + `{"error":"missing required property: bucket-name"}`

### Phase 11 — Verification ✅

- [x] `go build ./...` — no compile errors
- [x] `go vet ./...` — no issues
- [x] `go test ./internal/...` — all unit tests pass
- [x] `go test ./test/...` — all integration tests pass
- [ ] Manual smoke test: `./server` + `curl POST /api/aws/v1/s3/buckets` with valid payload → 201 + file written to `./output/aws/s3/<bucket-name>/main.tf`
- [ ] Manual error test: missing property → 422 with correct error message; bad JSON → 400
