package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devstroop/notalk/internal/model"
)

func TestHealth(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()

	Health(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %s", body["status"])
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusCreated, map[string]int{"count": 42})

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}

	var body map[string]int
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["count"] != 42 {
		t.Errorf("expected count 42, got %d", body["count"])
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "something broke")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "something broke" {
		t.Errorf("expected 'something broke', got %s", body["error"])
	}
}

func TestReadJSON(t *testing.T) {
	body := `{"phone_number":"919876543210","account_name":"test"}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))

	var got model.CreateAccountRequest
	err := readJSON(req, &got)
	if err != nil {
		t.Fatalf("readJSON: %v", err)
	}
	if got.PhoneNumber != "919876543210" {
		t.Errorf("expected phone 919876543210, got %s", got.PhoneNumber)
	}
	if got.AccountName != "test" {
		t.Errorf("expected name test, got %s", got.AccountName)
	}
}

func TestReadJSONInvalid(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader("not json"))
	var got model.CreateAccountRequest
	err := readJSON(req, &got)
	if err == nil {
		t.Error("expected error for invalid json, got nil")
	}
}
