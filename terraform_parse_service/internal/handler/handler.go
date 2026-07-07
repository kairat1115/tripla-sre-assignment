// Package handler contains shared HTTP handler contracts and response helpers.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
)

// Terraform is the handler-facing contract for rendering and managing generated
// Terraform resources. It keeps HTTP packages independent from the concrete
// service implementation.
type Terraform interface {
	Generate(ctx context.Context, g service.Generator) (string, error)
	Read(ctx context.Context, l service.Locator) ([]byte, error)
	List(ctx context.Context, l service.Locator) ([]string, error)
	Delete(ctx context.Context, l service.Locator) error
}

// Result describes a JSON response and optional error produced by a handler.
type Result struct {
	Code int
	Data any
	Err  error
	Msg  string
}

// Respond writes Result as JSON. When Err is set, Msg is returned as the error
// body and Data is ignored.
func Respond(w http.ResponseWriter, r Result) {
	if r.Err != nil {
		WriteError(w, r.Code, r.Msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.Code)
	_ = json.NewEncoder(w).Encode(r.Data)
}

// WriteError writes a consistent JSON error body.
func WriteError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Middleware normalizes standard-library routing errors into JSON so clients
// always receive the same response shape from API endpoints.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseRecorder{header: w.Header().Clone(), code: http.StatusOK}
		next.ServeHTTP(rw, r)
		if rw.code >= 400 && rw.header.Get("Content-Type") != "application/json" {
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
