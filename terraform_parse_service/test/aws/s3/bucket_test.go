package s3_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createBucket(t *testing.T, srv *httptest.Server, name string) {
	t.Helper()
	body := strings.NewReader(`{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":"` + name + `"}}}`)
	resp, err := http.Post(srv.URL+"/api/aws/v1/s3/buckets", "application/json", body)
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create bucket: want 201, got %d", resp.StatusCode)
	}
}

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
	if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
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

func TestIntegration_ListBuckets_Empty(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/aws/v1/s3/buckets")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var result map[string][]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["buckets"] == nil || len(result["buckets"]) != 0 {
		t.Fatalf("want empty buckets, got %v", result["buckets"])
	}
}

func TestIntegration_ListBuckets_AfterCreate(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createBucket(t, srv, "tripla-bucket")

	resp, err := http.Get(srv.URL + "/api/aws/v1/s3/buckets")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var result map[string][]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result["buckets"]) != 1 || result["buckets"][0] != "tripla-bucket" {
		t.Fatalf("want [tripla-bucket], got %v", result["buckets"])
	}
}

func TestIntegration_GetBucket_Success(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createBucket(t, srv, "tripla-bucket")

	resp, err := http.Get(srv.URL + "/api/aws/v1/s3/buckets/tripla-bucket")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(bodyBytes), `resource "aws_s3_bucket"`) {
		t.Fatalf("body missing expected terraform resource: %s", bodyBytes)
	}
}

func TestIntegration_GetBucket_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/aws/v1/s3/buckets/does-not-exist")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestIntegration_PutBucket_Create(t *testing.T) {
	srv, storageDir := newTestServer(t)
	defer srv.Close()

	body := strings.NewReader(`{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private"}}}`)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/aws/v1/s3/buckets/new-bucket", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	mainTF := filepath.Join(storageDir, "s3", "new-bucket", "main.tf")
	if _, err := os.Stat(mainTF); os.IsNotExist(err) {
		t.Fatalf("expected file at %s, not found", mainTF)
	}
}

func TestIntegration_PutBucket_Update(t *testing.T) {
	srv, storageDir := newTestServer(t)
	defer srv.Close()

	createBucket(t, srv, "tripla-bucket")

	body := strings.NewReader(`{"payload":{"properties":{"aws-region":"us-east-1","acl":"public-read"}}}`)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/aws/v1/s3/buckets/tripla-bucket", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	content, err := os.ReadFile(filepath.Join(storageDir, "s3", "tripla-bucket", "main.tf"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(content), "us-east-1") {
		t.Fatalf("expected updated region in file: %s", content)
	}
}

func TestIntegration_DeleteBucket_Success(t *testing.T) {
	srv, storageDir := newTestServer(t)
	defer srv.Close()

	createBucket(t, srv, "tripla-bucket")

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/aws/v1/s3/buckets/tripla-bucket", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
	bucketDir := filepath.Join(storageDir, "s3", "tripla-bucket")
	if _, err := os.Stat(bucketDir); !os.IsNotExist(err) {
		t.Fatalf("expected dir to be deleted")
	}
}

func TestIntegration_DeleteBucket_Idempotent(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/aws/v1/s3/buckets/does-not-exist", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
}
