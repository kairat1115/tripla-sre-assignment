# Plan 7: Input Sanitization — Path Traversal

## Vulnerability

`bucket-name` from request body flows unsanitized into the filesystem path:

```
BucketName → StoragePath() → "s3/" + BucketName → FSWriter.Write(name)
           → filepath.Join(BaseDir, name) → MkdirAll + WriteFile
```

`filepath.Join` cleans `..` segments but does not confine the result to the base
directory. `filepath.Join("/output/aws", "s3/../../etc/passwd")` resolves to
`/output/etc/passwd`.

`Region` and `ACL` only flow into template data — no filesystem path involvement.

---

## Defense Strategy

Two layers, independent of each other:

| Layer | Where | Mechanism | Error |
|-------|-------|-----------|-------|
| Primary | `bucketProperties.Validate()` | Reject on invalid name format | 422 "Invalid name" |
| Defense-in-depth | `FSWriter.Write` | Verify resolved path stays within base dir | storage error |

Primary layer: fail at validation with a clear 422 before anything reaches storage.  
Defense-in-depth: `FSWriter.Write` is called by any provider, not just S3 — confinement
check here protects all future callers regardless of whether their input was validated.

---

## Changes

### 1. Bucket name validation in `bucketProperties.Validate()`

AWS S3 bucket naming rules are well-defined and inherently exclude traversal characters:

- Length: 3–63 characters
- Characters: lowercase letters (`a-z`), digits (`0-9`), hyphens (`-`), dots (`.`)
- Must start and end with letter or digit
- No consecutive dots (`..`)
- Must not be formatted as an IP address (e.g. `192.168.1.1`)

These rules eliminate `/`, `\`, `..` as standalone path components, null bytes, and
every other traversal vector.

**`internal/handler/aws/s3/bucket.go`** — add a package-level compiled regexp and
extend `Validate()`:

```go
import "regexp"

var bucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9\-\.]*[a-z0-9]$`)

func (p bucketProperties) Validate() error {
    switch {
    case p.Region == "":
        return fmt.Errorf("missing required property: aws-region")
    case p.ACL == "":
        return fmt.Errorf("missing required property: acl")
    case p.BucketName == "":
        return fmt.Errorf("missing required property: bucket-name")
    case len(p.BucketName) < 3 || len(p.BucketName) > 63:
        return fmt.Errorf("invalid bucket-name: must be 3–63 characters")
    case !bucketNameRE.MatchString(p.BucketName):
        return fmt.Errorf("invalid bucket-name: must contain only lowercase letters, digits, hyphens, and dots, and start/end with a letter or digit")
    case strings.Contains(p.BucketName, ".."):
        return fmt.Errorf("invalid bucket-name: must not contain consecutive dots")
    default:
        return nil
    }
}
```

`strings` is already imported. `regexp` needs to be added.

The consecutive-dots check is separate from the regex because it is simpler and more
readable than a negative lookahead (Go's `regexp` package does not support lookaheads).

IP address format is not checked — an S3 bucket named `192.168.1.1` does not present
a path traversal risk and is an uncommon edge case not worth complicating validation for.

**Error response**: 422 Unprocessable Entity, existing `err-handler-validation` slug.
No new exception slug needed — validation already covers this path.

---

### 2. Path confinement check in `FSWriter.Write`

Defense-in-depth: verify the resolved path is inside `BaseDir` regardless of input
source.

**`internal/storage/filesystem.go`** — add check before `MkdirAll`:

```go
import "strings"

func (w *FSWriter) Write(ctx context.Context, name string, content []byte) (string, error) {
    // ... span setup ...

    dir := filepath.Join(w.BaseDir, name)
    if !isWithinBase(w.BaseDir, dir) {
        err := fmt.Errorf("storage path %q escapes base directory", name)
        span.SetStatus(codes.Error, err.Error())
        span.SetAttributes(
            attribute.String("exception.slug", "err-storage-path-traversal"),
            attribute.Bool("error", true),
        )
        return "", err
    }

    // ... existing MkdirAll + WriteFile ...
}

func isWithinBase(base, target string) bool {
    base = filepath.Clean(base) + string(filepath.Separator)
    return strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), base)
}
```

The `+ string(filepath.Separator)` suffix on both sides prevents `/output/aws-evil`
from being accepted as a child of `/output/aws`.

`strings` needs to be added to imports.

No `span.RecordError` here — this is a security event, not an I/O failure. The slug
`err-storage-path-traversal` is distinct from I/O slugs so it is queryable in Honeycomb.

---

### 3. Tests

**`internal/handler/aws/s3/bucket_test.go`** — add validation cases:

```go
func TestBucketHandler_InvalidBucketName(t *testing.T) {
    cases := []struct {
        name       string
        bucketName string
    }{
        {"path traversal with dots", "../../etc/passwd"},
        {"absolute path", "/etc/passwd"},
        {"slash in name", "foo/bar"},
        {"uppercase", "MyBucket"},
        {"too short", "ab"},
        {"too long", strings.Repeat("a", 64)},
        {"consecutive dots", "foo..bar"},
        {"trailing dot", "foo."},
        {"leading hyphen", "-foo"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            body := fmt.Sprintf(
                `{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":%q}}}`,
                tc.bucketName,
            )
            h := NewBucketHandler(&stubTerraform{}, zap.NewNop(), testMetrics())
            rec := httptest.NewRecorder()
            req := httptest.NewRequest(http.MethodPost, "/api/aws/v1/s3/buckets", bytes.NewBufferString(body))
            h.ServeHTTP(rec, req)
            if rec.Code != http.StatusUnprocessableEntity {
                t.Fatalf("%s: want 422, got %d", tc.name, rec.Code)
            }
        })
    }
}
```

**`internal/storage/filesystem_test.go`** (new file) — test confinement:

```go
package storage

import (
    "context"
    "os"
    "testing"
)

func TestFSWriter_PathTraversal(t *testing.T) {
    dir := t.TempDir()
    w, err := NewFSWriter(dir)
    if err != nil {
        t.Fatal(err)
    }
    _, err = w.Write(context.Background(), "../../etc/passwd", []byte("x"))
    if err == nil {
        t.Fatal("expected error for path traversal, got nil")
    }
}
```

---

## File Change Summary

| File | Change |
|------|--------|
| `internal/handler/aws/s3/bucket.go` | Add `bucketNameRE`; extend `Validate()` with length + regex + consecutive-dots checks; add `regexp` import |
| `internal/storage/filesystem.go` | Add `isWithinBase` helper; call before `MkdirAll`; add `err-storage-path-traversal` slug; add `strings` import |
| `internal/handler/aws/s3/bucket_test.go` | Add `TestBucketHandler_InvalidBucketName` table test |
| `internal/storage/filesystem_test.go` | New — `TestFSWriter_PathTraversal` |

---

## TODO

### Phase 1 — Bucket name validation

- [x] Add `regexp` import to `internal/handler/aws/s3/bucket.go`
- [x] Add `var bucketNameRE = regexp.MustCompile(...)` at package level
- [x] Add length check (`< 3 || > 63`) to `Validate()`
- [x] Add regex check (`!bucketNameRE.MatchString`) to `Validate()`
- [x] Add consecutive-dots check (`strings.Contains(p.BucketName, "..")`) to `Validate()`

### Phase 2 — Storage path confinement

- [x] Add `strings` import to `internal/storage/filesystem.go`
- [x] Add `isWithinBase(base, target string) bool` helper
- [x] Call `isWithinBase` in `Write` before `MkdirAll`; return `err-storage-path-traversal` on failure

### Phase 3 — Tests

- [x] Add `TestBucketHandler_InvalidBucketName` table test in `internal/handler/aws/s3/bucket_test.go`
- [x] Add `strings` import to `bucket_test.go`
- [x] Create `internal/storage/filesystem_test.go` with `TestFSWriter_PathTraversal`

### Phase 4 — Typecheck and verify

- [x] `go build ./...`
- [x] `go vet ./...`
- [x] `go test ./...`
