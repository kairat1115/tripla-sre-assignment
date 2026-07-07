# Plan 9: Generic Storage Access on TerraformService

## Problem

`TerraformService` has resource-specific methods:

```go
func (s *TerraformService) ListBuckets(ctx, provider) ([]string, error)
func (s *TerraformService) ReadBucket(ctx, provider, bucketName) ([]byte, error)
func (s *TerraformService) DeleteBucket(ctx, provider, bucketName) error
```

Every new resource type (RDS, VPC, IAM role) adds another 3 methods. The service has no business encoding resource types — it is a thin delegation layer to `storage.Writer`. The handler already owns path construction via `bucketGenerator.StoragePath() = "s3/" + bucketName`. The path logic belongs there.

## Solution

Introduce a `Locator` interface in the `service` package. The three generic methods accept `Locator` instead of raw strings. `Generator` embeds `Locator`, so `bucketGenerator` already satisfies both interfaces with no changes.

### `Locator` interface

Adding `ctx` to `Generate` as a parameter (per note below) makes `Locator.Context()` redundant — context is passed explicitly to every service method. `Context()` is removed from `Locator`, and `bucketGenerator.ctx` field plus its `Context()` method are deleted. This follows Go convention: context travels via function arguments, never embedded in structs or interfaces.

```go
type Locator interface {
    Provider() string
    StoragePath() string
}
```

`Generator` is redefined to embed `Locator`:

```go
type Generator interface {
    Locator
    TemplateName() string
    TemplateData() any
}
```

`bucketGenerator` drops the `ctx` field and `Context()` method. `bucketGenerator` is now a pure value struct with no context coupling.

### Service method signatures

`Generate` receives `ctx` as a first argument — consistent with `Read`, `List`, `Delete`:

```go
func (s *TerraformService) Generate(ctx context.Context, g Generator) (string, error)
func (s *TerraformService) Read(ctx context.Context, l Locator) ([]byte, error)
func (s *TerraformService) List(ctx context.Context, l Locator) ([]string, error)
func (s *TerraformService) Delete(ctx context.Context, l Locator) error
```

`List` derives the storage prefix from `path.Dir(l.StoragePath())`:
- `StoragePath()` = `"s3/my-bucket"` → `path.Dir(...)` = `"s3"` ✓
- Adding RDS: `StoragePath()` = `"rds/my-db"` → `path.Dir(...)` = `"rds"` ✓

`Read` and `Delete` use `l.StoragePath()` directly.

No provider string argument — `l.Provider()` supplies it.

### Invariant

The handler creates a `bucketGenerator` (or a lighter struct satisfying `Locator`) and passes it to any of the three service methods. The service derives provider and path from the same source it already uses for `Generate`. Consistent, no duplication.

For `List`, the handler does not need to pass a bucket name — it passes a `Locator` whose `StoragePath()` returns any path under the prefix. `path.Dir("s3/")` = `"s3"` ✓ — trailing slash stripped by `path.Dir`, leaving the directory name.

**Chosen approach**: define a `resourceLocator` helper in the handler package (unexported) that the handler constructs for Read/List/Delete calls. It carries provider and path only — no context, since ctx is now a parameter. `bucketGenerator` continues to be used for `Generate` calls only.

```go
type resourceLocator struct {
    provider string
    path     string
}

func (l *resourceLocator) Provider() string    { return l.provider }
func (l *resourceLocator) StoragePath() string { return l.path }
```

Handler call sites:

```go
// List: path = "s3/" so Dir = "s3"
buckets, err := h.svc.List(ctx, &resourceLocator{provider: "aws", path: "s3/"})

// Get:
content, err := h.svc.Read(ctx, &resourceLocator{provider: "aws", path: "s3/" + bucketName})

// Delete:
err := h.svc.Delete(ctx, &resourceLocator{provider: "aws", path: "s3/" + bucketName})
```

---

## Changes

### 1. `internal/service/terraform.go`

Add `Locator` interface. Refactor `Generator` to embed it. Add `ctx` param to `Generate`. Remove `Context()` from both interfaces. Drop `ctx` field from `bucketGenerator`. Add `Read`, `List`, `Delete`. Remove `ListBuckets`, `ReadBucket`, `DeleteBucket`.

```go
type Locator interface {
    Provider() string
    StoragePath() string
}

type Generator interface {
    Locator
    TemplateName() string
    TemplateData() any
}

func (s *TerraformService) Generate(ctx context.Context, g Generator) (string, error) {
    // existing body unchanged except: replace g.Context() with ctx
}

func (s *TerraformService) Read(ctx context.Context, l Locator) ([]byte, error) {
    writer, ok := s.writers[l.Provider()]
    if !ok {
        return nil, fmt.Errorf("no writer registered for provider %s", l.Provider())
    }
    return writer.Read(ctx, l.StoragePath())
}

func (s *TerraformService) List(ctx context.Context, l Locator) ([]string, error) {
    writer, ok := s.writers[l.Provider()]
    if !ok {
        return nil, fmt.Errorf("no writer registered for provider %s", l.Provider())
    }
    return writer.List(ctx, path.Dir(l.StoragePath()))
}

func (s *TerraformService) Delete(ctx context.Context, l Locator) error {
    writer, ok := s.writers[l.Provider()]
    if !ok {
        return fmt.Errorf("no writer registered for provider %s", l.Provider())
    }
    return writer.Delete(ctx, l.StoragePath())
}
```

Add `"path"` import (standard library `path`, not `path/filepath` — storage paths use forward slashes as logical separators).

### 2. `internal/handler/handler.go`

Update `Terraform` interface — `Generate` receives `ctx` as first arg, consistent with all other methods:

```go
type Terraform interface {
    Generate(ctx context.Context, g service.Generator) (string, error)
    Read(ctx context.Context, l service.Locator) ([]byte, error)
    List(ctx context.Context, l service.Locator) ([]string, error)
    Delete(ctx context.Context, l service.Locator) error
}
```

### 3. `internal/handler/aws/s3/bucket.go`

Add `resourceLocator` struct (unexported, package-level). No context field — ctx is passed via function argument. Drop `bucketGenerator.ctx` field and `Context()` method. Update `Create()` and `Update()` handlers to pass `ctx` to `h.svc.Generate(ctx, gen)`.

```go
type resourceLocator struct {
    provider string
    path     string
}

func (l *resourceLocator) Provider() string    { return l.provider }
func (l *resourceLocator) StoragePath() string { return l.path }
```

Update `List()` handler:
```go
buckets, err := h.svc.List(ctx, &resourceLocator{provider: "aws", path: "s3/"})
```

Update `Get()` handler:
```go
content, err := h.svc.Read(ctx, &resourceLocator{provider: "aws", path: "s3/" + bucketName})
```

Update `Delete()` handler:
```go
err := h.svc.Delete(ctx, &resourceLocator{provider: "aws", path: "s3/" + bucketName})
```

Update `Create()` and `Update()` handlers — `gen` no longer carries ctx; pass ctx explicitly:
```go
gen := &bucketGenerator{props: p}
outputPath, err := h.svc.Generate(ctx, gen)
```

### 4. `internal/handler/aws/s3/bucket_test.go`

Update `stubTerraform`:

Update `stubTerraform.Generate` signature to match new interface, and replace the three old stub methods:

```go
func (s *stubTerraform) Generate(_ context.Context, _ service.Generator) (string, error) {
    return s.path, s.err
}

func (s *stubTerraform) Read(_ context.Context, _ service.Locator) ([]byte, error) {
    return s.content, s.err
}

func (s *stubTerraform) List(_ context.Context, _ service.Locator) ([]string, error) {
    if s.err != nil {
        return nil, s.err
    }
    if s.buckets == nil {
        return []string{}, nil
    }
    return s.buckets, nil
}

func (s *stubTerraform) Delete(_ context.Context, _ service.Locator) error {
    return s.err
}
```

`service` import already present for `service.Generator`.

---

## File Change Summary

| File | Change |
|------|--------|
| `internal/service/terraform.go` | Add `Locator` interface (no `Context()`); embed in `Generator`; add `ctx` param to `Generate`, replace `g.Context()` with it; remove `ListBuckets`/`ReadBucket`/`DeleteBucket`; add `Read`, `List`, `Delete` accepting `Locator`; add `"path"` import |
| `internal/handler/handler.go` | Add `ctx` to `Generate` in `Terraform` interface; replace 3 specific methods with `Read`, `List`, `Delete` accepting `service.Locator` |
| `internal/handler/aws/s3/bucket.go` | Drop `ctx` field and `Context()` from `bucketGenerator`; add `resourceLocator` (no ctx field); update `Create()` and `Update()` to pass ctx to `Generate`; update `List()`, `Get()`, `Delete()` call sites |
| `internal/handler/aws/s3/bucket_test.go` | Update `Generate` stub signature; replace 3 old stub methods with `Read`, `List`, `Delete` accepting `service.Locator` |

No changes to `storage.go`, `filesystem.go`, `main.go`, or integration tests.

---

## TODO

### Phase 1 — `internal/service/terraform.go` ✅

- [x] Add `Locator` interface with `Provider() string` and `StoragePath() string` — no `Context()` method
- [x] Redefine `Generator` to embed `Locator` and drop the now-redundant `Provider() string` and `StoragePath() string` declarations; remove `Context() context.Context` from `Generator`
- [x] Change `Generate` signature from `Generate(g Generator) (string, error)` to `Generate(ctx context.Context, g Generator) (string, error)`
- [x] Inside `Generate` body: replace `g.Context()` (line 70: `otel.Tracer(...).Start(g.Context(), ...)`) with the new `ctx` parameter
- [x] Remove `ListBuckets(ctx context.Context, provider string) ([]string, error)` method from `TerraformService`
- [x] Remove `ReadBucket(ctx context.Context, provider, bucketName string) ([]byte, error)` method from `TerraformService`
- [x] Remove `DeleteBucket(ctx context.Context, provider, bucketName string) error` method from `TerraformService`
- [x] Add `Read(ctx context.Context, l Locator) ([]byte, error)` — looks up writer by `l.Provider()`, calls `writer.Read(ctx, l.StoragePath())`
- [x] Add `List(ctx context.Context, l Locator) ([]string, error)` — looks up writer by `l.Provider()`, calls `writer.List(ctx, path.Dir(l.StoragePath()))`
- [x] Add `Delete(ctx context.Context, l Locator) error` — looks up writer by `l.Provider()`, calls `writer.Delete(ctx, l.StoragePath())`
- [x] Add `"path"` to imports (stdlib `path`, not `path/filepath`)

### Phase 2 — `internal/handler/handler.go` ✅

- [x] Change `Generate(g service.Generator) (string, error)` to `Generate(ctx context.Context, g service.Generator) (string, error)` in the `Terraform` interface
- [x] Remove `ListBuckets(ctx context.Context, provider string) ([]string, error)` from `Terraform` interface
- [x] Remove `ReadBucket(ctx context.Context, provider, bucketName string) ([]byte, error)` from `Terraform` interface
- [x] Remove `DeleteBucket(ctx context.Context, provider, bucketName string) error` from `Terraform` interface
- [x] Add `Read(ctx context.Context, l service.Locator) ([]byte, error)` to `Terraform` interface
- [x] Add `List(ctx context.Context, l service.Locator) ([]string, error)` to `Terraform` interface
- [x] Add `Delete(ctx context.Context, l service.Locator) error` to `Terraform` interface

### Phase 3 — `internal/handler/aws/s3/bucket.go` ✅

- [x] Remove `ctx context.Context` field from `bucketGenerator` struct
- [x] Remove `Context() context.Context` method from `bucketGenerator`
- [x] Add `resourceLocator` struct with `provider string` and `path string` fields
- [x] Add `Provider() string` and `StoragePath() string` methods on `*resourceLocator`
- [x] In `Create()` handler: change `gen := &bucketGenerator{props: p, ctx: ctx}` to `gen := &bucketGenerator{props: p}`; change `h.svc.Generate(gen)` to `h.svc.Generate(ctx, gen)`
- [x] In `Update()` handler: same two changes as Create
- [x] In `List()` handler: replace `h.svc.ListBuckets(ctx, "aws")` with `h.svc.List(ctx, &resourceLocator{provider: "aws", path: "s3/"})`
- [x] In `Get()` handler: replace `h.svc.ReadBucket(ctx, "aws", bucketName)` with `h.svc.Read(ctx, &resourceLocator{provider: "aws", path: "s3/" + bucketName})`
- [x] In `Delete()` handler: replace `h.svc.DeleteBucket(ctx, "aws", bucketName)` with `h.svc.Delete(ctx, &resourceLocator{provider: "aws", path: "s3/" + bucketName})`
- [x] Verify compile-time check `var _ service.Generator = (*bucketGenerator)(nil)` still compiles — it will, since `bucketGenerator` still has `Provider()`, `StoragePath()`, `TemplateName()`, `TemplateData()`
- [x] Remove `"context"` import if it becomes unused after dropping the `ctx` field (it won't — `ctx` is still used in handler closures; verify)

### Phase 4 — `internal/handler/aws/s3/bucket_test.go` ✅

- [x] Change `stubTerraform.Generate` signature from `Generate(_ service.Generator) (string, error)` to `Generate(_ context.Context, _ service.Generator) (string, error)`
- [x] Remove `ListBuckets`, `ReadBucket`, `DeleteBucket` methods from `stubTerraform`
- [x] Add `Read(_ context.Context, _ service.Locator) ([]byte, error)` to `stubTerraform` — returns `s.content, s.err`
- [x] Add `List(_ context.Context, _ service.Locator) ([]string, error)` to `stubTerraform` — returns `nil, s.err` on error, `[]string{}` when `s.buckets == nil`, else `s.buckets`
- [x] Add `Delete(_ context.Context, _ service.Locator) error` to `stubTerraform` — returns `s.err`

### Phase 5 — Typecheck and verify ✅

- [x] `go build ./...`
- [x] `go vet ./...`
- [x] `go test ./...`
