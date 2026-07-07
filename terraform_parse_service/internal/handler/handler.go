package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
)

type Terraform interface {
	Generate(ctx context.Context, g service.Generator) (string, error)
	Read(ctx context.Context, l service.Locator) ([]byte, error)
	List(ctx context.Context, l service.Locator) ([]string, error)
	Delete(ctx context.Context, l service.Locator) error
}

type Result struct {
	Code int
	Data any
	Err  error
	Msg  string
}

func Respond(w http.ResponseWriter, r Result) {
	if r.Err != nil {
		WriteError(w, r.Code, r.Msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.Code)
	_ = json.NewEncoder(w).Encode(r.Data)
}

func WriteError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

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
