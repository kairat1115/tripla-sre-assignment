# Plan 8: S3 Bucket CRUD Endpoints

## New Endpoints

| Method | Path | Description | Success |
|--------|------|-------------|---------|
| `GET` | `/api/aws/v1/s3/buckets` | List all configured buckets | 200 |
| `GET` | `/api/aws/v1/s3/buckets/{bucket_name}` | Return rendered `main.tf` content | 200 |
| `PUT` | `/api/aws/v1/s3/buckets/{bucket_name}` | Create or update bucket (re-render + write) | 200 |
| `DELETE` | `/api/aws/v1/s3/buckets/{bucket_name}` | Delete bucket directory from output | 204 |

Existing `POST /api/aws/v1/s3/buckets` is unchanged.

---

## Storage Layout

```
BaseDir/
  s3/
    {bucket_name}/
      main.tf
```

List = `ReadDir(BaseDir + "/s3/")` → dir names.
Read = `ReadFile(BaseDir + "/s3/{name}/main.tf")`.
Delete = `RemoveAll(BaseDir + "/s3/{name}")`.
Write (PUT) = existing `Write` method (idempotent).

---

## Changes

### 1. Extend `storage.Writer` interface and `FSWriter`

**`internal/storage/storage.go`** — add methods:

```go
type Writer interface {
    Write(ctx context.Context, name string, content []byte) (string, error)
    Read(ctx context.Context, name string) ([]byte, error)
    List(ctx context.Context, prefix string) ([]string, error)
    Delete(ctx context.Context, name string) error
}
```

**`internal/storage/filesystem.go`** — implement:

```go
func (w *FSWriter) Read(ctx context.Context, name string) ([]byte, error) {
    _, span := otel.Tracer(tracerName).Start(ctx, "storage.read",
        trace.WithAttributes(
            attribute.String("storage.name", name),
            attribute.String("storage.base_dir", w.BaseDir),
        ),
    )
    defer span.End()

    path := filepath.Join(w.BaseDir, name, "main.tf")
    if !isWithinBase(w.BaseDir, path) {
        err := fmt.Errorf("storage path %q escapes base directory", name)
        span.SetStatus(codes.Error, err.Error())
        span.SetAttributes(
            attribute.String("exception.slug", "err-storage-path-traversal"),
            attribute.Bool("error", true),
        )
        return nil, err
    }
    content, err := os.ReadFile(path)
    if err != nil {
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        span.SetAttributes(
            attribute.String("exception.slug", "err-storage-read-file"),
            attribute.Bool("error", true),
        )
        return nil, fmt.Errorf("read %s: %w", path, err)
    }
    span.SetStatus(codes.Ok, "")
    return content, nil
}

func (w *FSWriter) List(ctx context.Context, prefix string) ([]string, error) {
    _, span := otel.Tracer(tracerName).Start(ctx, "storage.list",
        trace.WithAttributes(
            attribute.String("storage.prefix", prefix),
            attribute.String("storage.base_dir", w.BaseDir),
        ),
    )
    defer span.End()

    dir := filepath.Join(w.BaseDir, prefix)
    entries, err := os.ReadDir(dir)
    if err != nil {
        if os.IsNotExist(err) {
            span.SetStatus(codes.Ok, "")
            return []string{}, nil
        }
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        span.SetAttributes(
            attribute.String("exception.slug", "err-storage-list-dir"),
            attribute.Bool("error", true),
        )
        return nil, fmt.Errorf("list %s: %w", dir, err)
    }
    names := make([]string, 0, len(entries))
    for _, e := range entries {
        if e.IsDir() {
            names = append(names, e.Name())
        }
    }
    span.SetStatus(codes.Ok, "")
    span.SetAttributes(attribute.Int("storage.count", len(names)))
    return names, nil
}

func (w *FSWriter) Delete(ctx context.Context, name string) error {
    _, span := otel.Tracer(tracerName).Start(ctx, "storage.delete",
        trace.WithAttributes(
            attribute.String("storage.name", name),
            attribute.String("storage.base_dir", w.BaseDir),
        ),
    )
    defer span.End()

    dir := filepath.Join(w.BaseDir, name)
    if !isWithinBase(w.BaseDir, dir) {
        err := fmt.Errorf("storage path %q escapes base directory", name)
        span.SetStatus(codes.Error, err.Error())
        span.SetAttributes(
            attribute.String("exception.slug", "err-storage-path-traversal"),
            attribute.Bool("error", true),
        )
        return err
    }
    if err := os.RemoveAll(dir); err != nil {
        span.SetStatus(codes.Error, err.Error())
        span.RecordError(err)
        span.SetAttributes(
            attribute.String("exception.slug", "err-storage-delete"),
            attribute.Bool("error", true),
        )
        return fmt.Errorf("delete %s: %w", dir, err)
    }
    span.SetStatus(codes.Ok, "")
    return nil
}
```

`Read` and `Delete` run path confinement check same as `Write`. `List` returns empty slice (not error) when directory doesn't exist yet.

---

### 2. Extend `TerraformService`

**`internal/service/terraform.go`** — add methods. These delegate directly to storage; no template rendering needed for read/list/delete.

```go
func (s *TerraformService) ListBuckets(ctx context.Context, provider string) ([]string, error) {
    writer, ok := s.writers[provider]
    if !ok {
        return nil, fmt.Errorf("no writer registered for provider %s", provider)
    }
    return writer.List(ctx, "s3")
}

func (s *TerraformService) ReadBucket(ctx context.Context, provider, bucketName string) ([]byte, error) {
    writer, ok := s.writers[provider]
    if !ok {
        return nil, fmt.Errorf("no writer registered for provider %s", provider)
    }
    return writer.Read(ctx, "s3/"+bucketName)
}

func (s *TerraformService) DeleteBucket(ctx context.Context, provider, bucketName string) error {
    writer, ok := s.writers[provider]
    if !ok {
        return fmt.Errorf("no writer registered for provider %s", provider)
    }
    return writer.Delete(ctx, "s3/"+bucketName)
}
```

`Generate` already handles create and update (idempotent write) — PUT reuses it.

---

### 3. Extend `handler.Terraform` interface

**`internal/handler/handler.go`** — add methods:

```go
type Terraform interface {
    Generate(g service.Generator) (string, error)
    ListBuckets(ctx context.Context, provider string) ([]string, error)
    ReadBucket(ctx context.Context, provider, bucketName string) ([]byte, error)
    DeleteBucket(ctx context.Context, provider, bucketName string) error
}
```

Add `"context"` import.

---

### 4. Add handler methods to `BucketHandler`

**`internal/handler/aws/s3/bucket.go`** — all five verbs are methods returning `http.HandlerFunc`. `BucketHandler` no longer implements `http.Handler` directly; the existing `ServeHTTP` body moves into `Post()`. All handlers are dispatched via method calls from `main.go` for consistency.

Add `Post()`, `List()`, `Get()`, `Put()`, `Delete()` as methods returning `http.HandlerFunc`. `Post()` contains the existing `ServeHTTP` logic unchanged; remove `ServeHTTP`:

```go
func (h *BucketHandler) Post() http.HandlerFunc {
    return h.ServeHTTP // existing logic moved here verbatim; ServeHTTP removed
}

func (h *BucketHandler) List() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
            trace.WithSpanKind(trace.SpanKindServer),
            trace.WithAttributes(
                attribute.String("http.request.method", r.Method),
                attribute.String("http.route", "GET /api/aws/v1/s3/buckets"),
                attribute.String("network.peer.address", r.RemoteAddr),
            ),
        )
        defer span.End()

        buckets, err := h.svc.ListBuckets(ctx, "aws")
        if err != nil {
            span.SetStatus(codes.Error, err.Error())
            span.SetAttributes(
                attribute.String("exception.slug", "err-handler-list-buckets"),
                attribute.Bool("error", true),
            )
            handler.WriteError(w, http.StatusInternalServerError, "list failed")
            return
        }
        span.SetStatus(codes.Ok, "")
        span.SetAttributes(attribute.Int("aws.s3.bucket_count", len(buckets)))
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        _ = json.NewEncoder(w).Encode(map[string][]string{"buckets": buckets})
    }
}

func (h *BucketHandler) Get() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        bucketName := r.PathValue("bucket_name")
        ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
            trace.WithSpanKind(trace.SpanKindServer),
            trace.WithAttributes(
                attribute.String("http.request.method", r.Method),
                attribute.String("http.route", "GET /api/aws/v1/s3/buckets/{bucket_name}"),
                attribute.String("network.peer.address", r.RemoteAddr),
                attribute.String("aws.s3.bucket_name", bucketName),
            ),
        )
        defer span.End()

        if err := validateBucketName(bucketName); err != nil {
            span.SetStatus(codes.Error, err.Error())
            span.SetAttributes(
                attribute.String("exception.slug", "err-handler-validation"),
                attribute.Bool("error", true),
            )
            handler.WriteError(w, http.StatusUnprocessableEntity, err.Error())
            return
        }

        content, err := h.svc.ReadBucket(ctx, "aws", bucketName)
        if err != nil {
            if os.IsNotExist(errors.Unwrap(err)) {
                span.SetStatus(codes.Error, err.Error())
                span.SetAttributes(
                    attribute.String("exception.slug", "err-handler-bucket-not-found"),
                    attribute.Bool("error", true),
                )
                handler.WriteError(w, http.StatusNotFound, "bucket not found")
                return
            }
            span.SetStatus(codes.Error, err.Error())
            span.SetAttributes(
                attribute.String("exception.slug", "err-handler-read-bucket"),
                attribute.Bool("error", true),
            )
            handler.WriteError(w, http.StatusInternalServerError, "read failed")
            return
        }
        span.SetStatus(codes.Ok, "")
        w.Header().Set("Content-Type", "text/plain")
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(content)
    }
}

func (h *BucketHandler) Put() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        bucketName := r.PathValue("bucket_name")
        // reuse existing ServeHTTP body with bucketName injected —
        // but PUT path param must match body bucket-name.
        // Implementation: decode body, override BucketName from path param,
        // validate, generate.
        start := time.Now()
        path := r.URL.Path

        h.m.HTTPInFlight.WithLabelValues(r.Method, path).Inc()
        defer func() {
            h.m.HTTPInFlight.WithLabelValues(r.Method, path).Dec()
            h.m.HTTPDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
        }()

        ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
            trace.WithSpanKind(trace.SpanKindServer),
            trace.WithAttributes(
                attribute.String("http.request.method", r.Method),
                attribute.String("http.route", "PUT /api/aws/v1/s3/buckets/{bucket_name}"),
                attribute.String("network.peer.address", r.RemoteAddr),
                attribute.String("aws.s3.bucket_name", bucketName),
            ),
        )
        defer span.End()

        body, err := io.ReadAll(r.Body)
        if err != nil {
            span.SetStatus(codes.Error, err.Error())
            span.SetAttributes(
                attribute.String("exception.slug", "err-handler-body-read"),
                attribute.Bool("error", true),
            )
            h.m.HTTPRequestsTotal.WithLabelValues(r.Method, path, "400").Inc()
            handler.WriteError(w, http.StatusBadRequest, "invalid JSON")
            return
        }

        var req bucketRequest
        if err := json.Unmarshal(body, &req); err != nil {
            span.SetStatus(codes.Error, err.Error())
            span.SetAttributes(
                attribute.String("exception.slug", "err-handler-json-decode"),
                attribute.Bool("error", true),
            )
            h.m.HTTPRequestsTotal.WithLabelValues(r.Method, path, "400").Inc()
            handler.WriteError(w, http.StatusBadRequest, "invalid JSON")
            return
        }

        // Path param is authoritative; body bucket-name must match or be absent.
        p := req.Payload.Properties
        if p.BucketName != "" && p.BucketName != bucketName {
            span.SetAttributes(
                attribute.String("exception.slug", "err-handler-bucket-name-mismatch"),
                attribute.Bool("error", true),
            )
            h.m.HTTPRequestsTotal.WithLabelValues(r.Method, path, "422").Inc()
            handler.WriteError(w, http.StatusUnprocessableEntity, "bucket-name in body must match path parameter")
            return
        }
        p.BucketName = bucketName

        if err := p.Validate(); err != nil {
            span.SetStatus(codes.Error, err.Error())
            span.SetAttributes(
                attribute.String("exception.slug", "err-handler-validation"),
                attribute.Bool("error", true),
            )
            h.m.HTTPRequestsTotal.WithLabelValues(r.Method, path, "422").Inc()
            handler.WriteError(w, http.StatusUnprocessableEntity, err.Error())
            return
        }
        span.SetAttributes(
            attribute.String("aws.s3.region", p.Region),
            attribute.String("aws.s3.acl", p.ACL),
        )

        gen := &bucketGenerator{props: p, ctx: ctx}
        outputPath, err := h.svc.Generate(gen)
        if err != nil {
            span.SetStatus(codes.Error, err.Error())
            span.SetAttributes(
                attribute.String("exception.slug", "err-handler-generate"),
                attribute.Bool("error", true),
            )
            h.m.HTTPRequestsTotal.WithLabelValues(r.Method, path, "500").Inc()
            handler.WriteError(w, http.StatusInternalServerError, "generation failed")
            return
        }
        span.SetStatus(codes.Ok, "")
        span.SetAttributes(attribute.String("output.path", outputPath))
        h.m.HTTPRequestsTotal.WithLabelValues(r.Method, path, "200").Inc()
        handler.Respond(w, handler.Result{Code: http.StatusOK, Data: bucketResponse{OutputPath: outputPath}})
    }
}

func (h *BucketHandler) Delete() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        bucketName := r.PathValue("bucket_name")
        ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
            trace.WithSpanKind(trace.SpanKindServer),
            trace.WithAttributes(
                attribute.String("http.request.method", r.Method),
                attribute.String("http.route", "DELETE /api/aws/v1/s3/buckets/{bucket_name}"),
                attribute.String("network.peer.address", r.RemoteAddr),
                attribute.String("aws.s3.bucket_name", bucketName),
            ),
        )
        defer span.End()

        if err := validateBucketName(bucketName); err != nil {
            span.SetStatus(codes.Error, err.Error())
            span.SetAttributes(
                attribute.String("exception.slug", "err-handler-validation"),
                attribute.Bool("error", true),
            )
            handler.WriteError(w, http.StatusUnprocessableEntity, err.Error())
            return
        }

        if err := h.svc.DeleteBucket(ctx, "aws", bucketName); err != nil {
            span.SetStatus(codes.Error, err.Error())
            span.SetAttributes(
                attribute.String("exception.slug", "err-handler-delete-bucket"),
                attribute.Bool("error", true),
            )
            handler.WriteError(w, http.StatusInternalServerError, "delete failed")
            return
        }
        span.SetStatus(codes.Ok, "")
        w.WriteHeader(http.StatusNoContent)
    }
}
```

Extract bucket name validation into a standalone function to share across methods:

```go
func validateBucketName(name string) error {
    switch {
    case name == "":
        return fmt.Errorf("missing bucket-name")
    case len(name) < 3 || len(name) > 63:
        return fmt.Errorf("invalid bucket-name: must be 3–63 characters")
    case !bucketNameRE.MatchString(name):
        return fmt.Errorf("invalid bucket-name: must contain only lowercase letters, digits, hyphens, and dots, and start/end with a letter or digit")
    case strings.Contains(name, ".."):
        return fmt.Errorf("invalid bucket-name: must not contain consecutive dots")
    default:
        return nil
    }
}
```

`bucketProperties.Validate()` delegates to `validateBucketName` for the name checks to avoid duplication.

---

### 5. Register routes in `main.go`

```go
s3 := s3handler.NewBucketHandler(tfSvc, l, m)
mux.Handle("GET /api/aws/v1/s3/buckets", s3.List())
mux.Handle("POST /api/aws/v1/s3/buckets", s3.Post())
mux.Handle("GET /api/aws/v1/s3/buckets/{bucket_name}", s3.Get())
mux.Handle("PUT /api/aws/v1/s3/buckets/{bucket_name}", s3.Put())
mux.Handle("DELETE /api/aws/v1/s3/buckets/{bucket_name}", s3.Delete())
```

All routes use method calls for consistency. `BucketHandler` no longer needs to implement `http.Handler`.

---

### 6. Error handling — 404 vs 500 on Read

`os.ReadFile` wraps the OS error in `fmt.Errorf("read %s: %w", ...)`. To detect not-found:
`errors.Is(err, os.ErrNotExist)` after unwrapping — use `errors.Is` not `os.IsNotExist` on wrapped errors.

**`internal/handler/aws/s3/bucket.go`** — add `"errors"` and `"os"` imports for this check in `Get()`.

---

## File Change Summary

| File | Change |
|------|--------|
| `internal/storage/storage.go` | Add `Read`, `List`, `Delete` to `Writer` interface |
| `internal/storage/filesystem.go` | Implement `Read`, `List`, `Delete` on `FSWriter` |
| `internal/service/terraform.go` | Add `ListBuckets`, `ReadBucket`, `DeleteBucket` methods |
| `internal/handler/handler.go` | Add `ListBuckets`, `ReadBucket`, `DeleteBucket` to `Terraform` interface; add `"context"` import |
| `internal/handler/aws/s3/bucket.go` | Extract `validateBucketName`; refactor `Validate()` to delegate; move `ServeHTTP` body into `Post()`; remove `ServeHTTP`; add `List()`, `Get()`, `Put()`, `Delete()` methods returning `http.HandlerFunc`; add `"errors"`, `"os"` imports |
| `cmd/server/main.go` | Replace `s3` direct handle with `s3.Post()`; register 4 new routes |

---

## TODO

### Phase 1 — Storage interface and FSWriter ✅

- [x] Add `Read(ctx context.Context, name string) ([]byte, error)` to `Writer` interface in `internal/storage/storage.go`
- [x] Add `List(ctx context.Context, prefix string) ([]string, error)` to `Writer` interface
- [x] Add `Delete(ctx context.Context, name string) error` to `Writer` interface
- [x] Implement `FSWriter.Read`: `filepath.Join(BaseDir, name, "main.tf")`, confinement check, `os.ReadFile`, wrap error with `fmt.Errorf("read %s: %w", path, err)`, span attrs `storage.name` + `storage.base_dir`, slug `err-storage-read-file`
- [x] Implement `FSWriter.List`: `filepath.Join(BaseDir, prefix)`, `os.ReadDir`, return `[]string{}` (not nil, not error) when dir not exist, filter only `IsDir()` entries, span attr `storage.count`, slug `err-storage-list-dir`
- [x] Implement `FSWriter.Delete`: `filepath.Join(BaseDir, name)`, confinement check, `os.RemoveAll`, span attrs `storage.name` + `storage.base_dir`, slug `err-storage-delete`

### Phase 2 — Service methods ✅

- [x] Add `ListBuckets(ctx context.Context, provider string) ([]string, error)` to `TerraformService` — delegates to `writer.List(ctx, "s3")`
- [x] Add `ReadBucket(ctx context.Context, provider, bucketName string) ([]byte, error)` to `TerraformService` — delegates to `writer.Read(ctx, "s3/"+bucketName)`
- [x] Add `DeleteBucket(ctx context.Context, provider, bucketName string) error` to `TerraformService` — delegates to `writer.Delete(ctx, "s3/"+bucketName)`
- [x] Each method returns `fmt.Errorf("no writer registered for provider %s", provider)` when provider key missing

### Phase 3 — Handler interface ✅

- [x] Add `ListBuckets(ctx context.Context, provider string) ([]string, error)` to `handler.Terraform` interface in `internal/handler/handler.go`
- [x] Add `ReadBucket(ctx context.Context, provider, bucketName string) ([]byte, error)` to `handler.Terraform` interface
- [x] Add `DeleteBucket(ctx context.Context, provider, bucketName string) error` to `handler.Terraform` interface
- [x] Add `"context"` import to `internal/handler/handler.go`

### Phase 4 — Handler methods ✅

- [x] Extract `validateBucketName(name string) error` function in `bucket.go` with the same length + regex + consecutive-dots checks currently in `bucketProperties.Validate()`
- [x] Refactor `bucketProperties.Validate()`: replace inline name checks with a `validateBucketName(p.BucketName)` call; keep region and ACL checks inline
- [x] Move `ServeHTTP` body verbatim into `Post() http.HandlerFunc`; remove the `ServeHTTP` method from `BucketHandler`
- [x] Add `List() http.HandlerFunc`: span with `http.route = "GET /api/aws/v1/s3/buckets"`, call `h.svc.ListBuckets(ctx, "aws")`, respond 200 with `{"buckets": [...]}` JSON, span attr `aws.s3.bucket_count`, slug `err-handler-list-buckets`
- [x] Add `Get() http.HandlerFunc`: extract `bucket_name` via `r.PathValue`, `validateBucketName`, call `h.svc.ReadBucket(ctx, "aws", bucketName)`, respond 200 `text/plain` with file content; on error use `errors.Is(err, os.ErrNotExist)` for 404 with slug `err-handler-bucket-not-found`, else 500 with slug `err-handler-read-bucket`
- [x] Add `Put() http.HandlerFunc`: extract `bucket_name` via `r.PathValue`, decode body, if `body.bucket-name != ""` and mismatches path param return 422 with slug `err-handler-bucket-name-mismatch`, set `p.BucketName = bucketName`, validate, call `h.svc.Generate`, respond 200 with `{"output_path": ...}`; include metrics `HTTPInFlight` + `HTTPDuration` + `HTTPRequestsTotal` same as `Post()`
- [x] Add `Delete() http.HandlerFunc`: extract `bucket_name` via `r.PathValue`, `validateBucketName`, call `h.svc.DeleteBucket(ctx, "aws", bucketName)`, respond 204 No Content; slug `err-handler-delete-bucket` on error
- [x] Add `"errors"` and `"os"` imports to `bucket.go`

### Phase 5 — Route registration ✅

- [x] In `cmd/server/main.go`: replace `mux.Handle("POST /api/aws/v1/s3/buckets", s3handler.NewBucketHandler(tfSvc, l, m))` with local variable `s3 := s3handler.NewBucketHandler(tfSvc, l, m)`
- [x] Register `GET /api/aws/v1/s3/buckets` before POST
- [x] Register `POST /api/aws/v1/s3/buckets` as `s3.Post()`
- [x] Register `GET /api/aws/v1/s3/buckets/{bucket_name}` as `s3.Get()`
- [x] Register `PUT /api/aws/v1/s3/buckets/{bucket_name}` as `s3.Put()`
- [x] Register `DELETE /api/aws/v1/s3/buckets/{bucket_name}` as `s3.Delete()`

### Phase 6 — Update existing tests ✅

- [x] In `internal/handler/aws/s3/bucket_test.go`: add `ListBuckets`, `ReadBucket`, `DeleteBucket` methods to `stubTerraform` (return zero values + `s.err`)
- [x] In `internal/handler/aws/s3/bucket_test.go`: replace all `h.ServeHTTP(rec, req)` calls with `h.Post()(rec, req)` — affects `TestBucketHandler_BadJSON`, `TestBucketHandler_MissingProperty`, `TestBucketHandler_GenerationError`, `TestBucketHandler_Success`, `TestBucketHandler_InvalidBucketName`
- [x] In `test/helpers_test.go`: update route registration from `mux.Handle("POST /api/aws/v1/s3/buckets", s3handler.NewBucketHandler(...))` to `s3 := s3handler.NewBucketHandler(...); mux.Handle("POST /api/aws/v1/s3/buckets", s3.Post())` and add all 4 new routes
- [x] In `test/aws/s3/helpers_test.go`: same update as `test/helpers_test.go`; `newTestServer` already returns `storageDir` — ensure new routes are registered

### Phase 7 — New unit tests ✅

- [x] In `internal/storage/filesystem_test.go`: add `TestFSWriter_Read_Valid`
- [x] In `internal/storage/filesystem_test.go`: add `TestFSWriter_Read_NotFound`
- [x] In `internal/storage/filesystem_test.go`: add `TestFSWriter_Read_PathTraversal`
- [x] In `internal/storage/filesystem_test.go`: add `TestFSWriter_List_Empty`
- [x] In `internal/storage/filesystem_test.go`: add `TestFSWriter_List_AfterWrite`
- [x] In `internal/storage/filesystem_test.go`: add `TestFSWriter_Delete_Valid`
- [x] In `internal/storage/filesystem_test.go`: add `TestFSWriter_Delete_PathTraversal`
- [x] In `internal/handler/aws/s3/bucket_test.go`: add `TestBucketHandler_List_Empty`
- [x] In `internal/handler/aws/s3/bucket_test.go`: add `TestBucketHandler_List_WithBuckets`
- [x] In `internal/handler/aws/s3/bucket_test.go`: add `TestBucketHandler_Get_NotFound`
- [x] In `internal/handler/aws/s3/bucket_test.go`: add `TestBucketHandler_Get_Success`
- [x] In `internal/handler/aws/s3/bucket_test.go`: add `TestBucketHandler_Put_NameMismatch`
- [x] In `internal/handler/aws/s3/bucket_test.go`: add `TestBucketHandler_Put_Success`
- [x] In `internal/handler/aws/s3/bucket_test.go`: add `TestBucketHandler_Delete_InvalidName`
- [x] In `internal/handler/aws/s3/bucket_test.go`: add `TestBucketHandler_Delete_Success`

### Phase 8 — New integration tests ✅

- [x] In `test/aws/s3/bucket_test.go`: add `TestIntegration_ListBuckets_Empty`
- [x] In `test/aws/s3/bucket_test.go`: add `TestIntegration_ListBuckets_AfterCreate`
- [x] In `test/aws/s3/bucket_test.go`: add `TestIntegration_GetBucket_Success`
- [x] In `test/aws/s3/bucket_test.go`: add `TestIntegration_GetBucket_NotFound`
- [x] In `test/aws/s3/bucket_test.go`: add `TestIntegration_PutBucket_Create`
- [x] In `test/aws/s3/bucket_test.go`: add `TestIntegration_PutBucket_Update`
- [x] In `test/aws/s3/bucket_test.go`: add `TestIntegration_DeleteBucket_Success`
- [x] In `test/aws/s3/bucket_test.go`: add `TestIntegration_DeleteBucket_Idempotent` (RemoveAll is idempotent, 204 on missing bucket)

### Phase 9 — Typecheck and verify ✅

- [x] `go build ./...`
- [x] `go vet ./...`
- [x] `go test ./...`
- [x] `gofmt -w ./...`
