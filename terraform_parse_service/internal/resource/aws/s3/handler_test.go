package s3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource"
)

type stubTerraform struct {
	path    string
	err     error
	buckets []string
	content []byte
}

func (s *stubTerraform) Generate(_ context.Context, _ resource.Generator) (string, error) {
	return s.path, s.err
}

func (s *stubTerraform) Read(_ context.Context, _ resource.Locator) ([]byte, error) {
	return s.content, s.err
}

func (s *stubTerraform) List(_ context.Context, _ resource.Locator) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.buckets == nil {
		return []string{}, nil
	}
	return s.buckets, nil
}

func (s *stubTerraform) Delete(_ context.Context, _ resource.Locator) error {
	return s.err
}

func TestBucketHandler_BadJSON(t *testing.T) {
	h := NewBucketHandler(&stubTerraform{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/aws/v1/s3/buckets", bytes.NewBufferString("{bad"))

	h.Create()(rec, req)

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
	h := NewBucketHandler(&stubTerraform{})
	body := `{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private"}}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/aws/v1/s3/buckets", bytes.NewBufferString(body))

	h.Create()(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
}

func TestBucketHandler_GenerationError(t *testing.T) {
	h := NewBucketHandler(&stubTerraform{err: fmt.Errorf("render failed")})
	body := `{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":"my-bucket"}}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/aws/v1/s3/buckets", bytes.NewBufferString(body))

	h.Create()(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

func TestBucketHandler_Success(t *testing.T) {
	h := NewBucketHandler(&stubTerraform{path: "/out/s3/my-bucket/main.tf"})
	body := `{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":"my-bucket"}}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/aws/v1/s3/buckets", bytes.NewBufferString(body))

	h.Create()(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	var resp BucketResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.OutputPath != "/out/s3/my-bucket/main.tf" {
		t.Fatalf("unexpected output_path: %s", resp.OutputPath)
	}
}

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
			h := NewBucketHandler(&stubTerraform{})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/aws/v1/s3/buckets", bytes.NewBufferString(body))
			h.Create()(rec, req)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: want 422, got %d", tc.name, rec.Code)
			}
		})
	}
}

func TestBucketHandler_List_Empty(t *testing.T) {
	h := NewBucketHandler(&stubTerraform{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/aws/v1/s3/buckets", nil)
	h.List()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body map[string][]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["buckets"] == nil || len(body["buckets"]) != 0 {
		t.Fatalf("want empty buckets array, got %v", body["buckets"])
	}
}

func TestBucketHandler_List_WithBuckets(t *testing.T) {
	h := NewBucketHandler(&stubTerraform{buckets: []string{"alpha", "beta"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/aws/v1/s3/buckets", nil)
	h.List()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body map[string][]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body["buckets"]) != 2 {
		t.Fatalf("want 2 buckets, got %v", body["buckets"])
	}
}

func TestBucketHandler_Get_NotFound(t *testing.T) {
	h := NewBucketHandler(&stubTerraform{err: fmt.Errorf("read %s: %w", "x", os.ErrNotExist)})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/aws/v1/s3/buckets/my-bucket", nil)
	req.SetPathValue("bucket_name", "my-bucket")
	h.Get()(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestBucketHandler_Get_Success(t *testing.T) {
	want := []byte("resource \"aws_s3_bucket\" {}")
	h := NewBucketHandler(&stubTerraform{content: want})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/aws/v1/s3/buckets/my-bucket", nil)
	req.SetPathValue("bucket_name", "my-bucket")
	h.Get()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if rec.Body.String() != string(want) {
		t.Fatalf("want %q, got %q", want, rec.Body.String())
	}
}

func TestBucketHandler_Put_NameMismatch(t *testing.T) {
	h := NewBucketHandler(&stubTerraform{})
	body := `{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":"other-bucket"}}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/aws/v1/s3/buckets/my-bucket", bytes.NewBufferString(body))
	req.SetPathValue("bucket_name", "my-bucket")
	h.Update()(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
}

func TestBucketHandler_Put_Success(t *testing.T) {
	h := NewBucketHandler(&stubTerraform{path: "/out/s3/my-bucket/main.tf"})
	body := `{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private"}}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/aws/v1/s3/buckets/my-bucket", bytes.NewBufferString(body))
	req.SetPathValue("bucket_name", "my-bucket")
	h.Update()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp BucketResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OutputPath != "/out/s3/my-bucket/main.tf" {
		t.Fatalf("unexpected output_path: %s", resp.OutputPath)
	}
}

func TestBucketHandler_Delete_InvalidName(t *testing.T) {
	h := NewBucketHandler(&stubTerraform{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/aws/v1/s3/buckets/AB", nil)
	req.SetPathValue("bucket_name", "AB")
	h.Delete()(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
}

func TestBucketHandler_Delete_Success(t *testing.T) {
	h := NewBucketHandler(&stubTerraform{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/aws/v1/s3/buckets/my-bucket", nil)
	req.SetPathValue("bucket_name", "my-bucket")
	h.Delete()(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
}
