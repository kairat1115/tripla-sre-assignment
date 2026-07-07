package integration_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestIntegration_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/aws/v1/s3/buckets", nil)
	resp, err := http.DefaultClient.Do(req)
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
