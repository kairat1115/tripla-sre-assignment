# Plan: Revamp Logging in BucketHandler

## Problem Analysis

Three concrete issues observed in production logs:

### 1. Decode error — message is a raw Go error string

```json
{"msg":"unexpected EOF","status":400,...}
{"msg":"invalid character 'i' in literal true (expecting 'u')","status":400,...}
```

**What's wrong:**
- The log message is `err.Error()` from the JSON decoder — Go-internal language, not API language
- The message is unstable: it changes with Go version and encoder implementation
- There is no indication of what the caller sent — the body is consumed by the decoder before anything is logged
- An engineer seeing this log cannot tell what payload triggered the error without reproducing the request

### 2. Success log — message describes mechanism, not outcome

```json
{"msg":"request handled","status":201,...}
```

**What's wrong:**
- `"request handled"` is true of every path, including errors — it carries no information about what happened
- The outcome (a bucket was created) is only recoverable by correlating `status: 201`
- Filtering logs for successful bucket creations requires a status filter, not a message filter

### 3. Validate error path is acceptable as-is

```json
{"msg":"missing required property: bucket-name","status":422,...}
```

The message here is already the `err.Error()` from `Validate()` — but unlike the decoder errors, these messages are authored by us, are stable, and are semantically meaningful. No change needed.

---

## Root Cause

**Decode errors:** `json.NewDecoder(r.Body).Decode(&req)` consumes `r.Body`. By the time the error is returned, the body is gone. The only information available is the decoder's error string.

**Success message:** `"request handled"` was a placeholder that was never made specific.

---

## Fix

### Change 1 — Read body before decoding

Replace streaming decode with `io.ReadAll` + `json.Unmarshal`. This preserves the raw bytes so they can be logged on failure.


```go
body, err := io.ReadAll(r.Body)
if err != nil {
    h.logger.Info("request body read failed", append(base,
        zap.Int("status", http.StatusBadRequest),
        zap.String("error", err.Error()),
        zap.Int64("duration_ms", time.Since(start).Milliseconds()),
    )...)
    handler.Respond(w, handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
    return
}

var req bucketRequest
if err := json.Unmarshal(body, &req); err != nil {
    h.logger.Info("request body decode failed", append(base,
        zap.Int("status", http.StatusBadRequest),
        zap.String("error", err.Error()),
        zap.Int64("duration_ms", time.Since(start).Milliseconds()),
    )...)
    handler.Respond(w, handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
    return
}
```

**Result for `unexpected EOF`:**
```json
{"msg":"request body decode failed","status":400,"error":"unexpected EOF"}
```

**Result for `invalid character`:**
```json
{"msg":"request body decode failed","status":400,"error":"invalid character 'i' in literal true (expecting 'u')"}
```

The message is now stable and human-readable. Body inspection, if needed, is handled at the infrastructure layer (e.g. access logs, API gateway, network capture).

### Change 2 — Rename success message

```go
// before
h.logger.Info("request handled", ...)

// after
h.logger.Info("terraform config generated", ...)
```

**Result:**
```json
{"msg":"terraform config generated","status":201,"aws-region":"eu-west-1","acl":"private","bucket-name":"tripla-bucket","output_path":"output/aws/s3/tripla-bucket/main.tf","duration_ms":1}
```

Filtering `msg = "terraform config generated"` now returns exactly successful creation events.

---

## Updated `ServeHTTP`

Full updated function after both changes:

```go
func (h *BucketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    start := time.Now()

    base := []zap.Field{
        zap.String("method", r.Method),
        zap.String("path", r.URL.Path),
        zap.String("remote_addr", r.RemoteAddr),
    }

    body, err := io.ReadAll(r.Body)
    if err != nil {
        h.logger.Info("request body read failed", append(base,
            zap.Int("status", http.StatusBadRequest),
            zap.String("error", err.Error()),
            zap.Int64("duration_ms", time.Since(start).Milliseconds()),
        )...)
        handler.Respond(w, handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
        return
    }

    var req bucketRequest
    if err := json.Unmarshal(body, &req); err != nil {
        h.logger.Info("request body decode failed", append(base,
            zap.Int("status", http.StatusBadRequest),
            zap.String("error", err.Error()),
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

    h.logger.Info("terraform config generated", append(base,
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

---

## Log message summary after changes

| Path | Level | Message | Key added fields |
|---|---|---|---|
| Body read failure | Info | `request body read failed` | `error` |
| JSON decode failure | Info | `request body decode failed` | `error` |
| Validation failure | Info | `<err.Error()>` e.g. `missing required property: bucket-name` | `aws-region`, `acl`, `bucket-name` |
| Generation failure | Error | `generation failed` | `error`, `aws-region`, `acl`, `bucket-name` |
| Success | Info | `terraform config generated` | `aws-region`, `acl`, `bucket-name`, `output_path` |

---

## Files changed

- `internal/handler/aws/s3/bucket.go` — `ServeHTTP` only; add `io` import

## Tests to update

`TestBucketHandler_BadJSON` in `internal/handler/aws/s3/bucket_test.go` passes `bytes.NewBufferString("{bad")` directly to `httptest.NewRequest` — this still works with `io.ReadAll` since `httptest.NewRequest` sets `r.Body` to a readable `io.NopCloser`. No test infrastructure changes needed.


---

## TODO

### Phase 1 — Update `bucket.go` ✅

- [x] Add `"io"` to imports
- [x] Replace `json.NewDecoder(r.Body).Decode(&req)` with `io.ReadAll(r.Body)` + `json.Unmarshal`
- [x] Add body read failure path: `h.logger.Info("request body read failed", ...)` with `error` field
- [x] Change decode failure path: `h.logger.Info("request body decode failed", ...)` with `error` field
- [x] Rename success log message from `"request handled"` to `"terraform config generated"`

### Phase 2 — Update unit tests (`internal/handler/aws/s3/bucket_test.go`) ✅

- [x] Verify `TestBucketHandler_BadJSON` still passes — no structural change needed since `httptest.NewRequest` wraps the body in `io.NopCloser`

### Phase 3 — Verification ✅

- [x] `go vet ./internal/handler/...` — no issues
- [x] `go test ./internal/handler/aws/s3/...` — all unit tests pass
- [x] `go test ./test/...` — all integration tests pass
- [ ] Manual smoke test: send truncated JSON (`missing closing brace`) → log shows `msg: "request body decode failed"` with `error` field
- [ ] Manual smoke test: send invalid JSON (`unquoted value`) → log shows `msg: "request body decode failed"` with `error` field
- [ ] Manual smoke test: send valid payload → log shows `msg: "terraform config generated"` with all property fields and `output_path`
