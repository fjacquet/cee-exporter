package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReadBodyOversized proves that readBody does not panic when a request body
// exceeds 64 MiB and that it returns a non-nil error in that case.
func TestReadBodyOversized(t *testing.T) {
	big := bytes.Repeat([]byte("x"), (64<<20)+1)
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(big))
	rec := httptest.NewRecorder()

	_, err := readBody(rec, req)
	if err == nil {
		t.Error("expected error for oversized body, got nil")
	}
}

// TestReadBodyNormal proves that normal payloads are read correctly.
func TestReadBodyNormal(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 1024)
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	got, err := readBody(rec, req)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(got) != 1024 {
		t.Errorf("body len = %d, want 1024", len(got))
	}
}
