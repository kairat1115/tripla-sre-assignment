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
