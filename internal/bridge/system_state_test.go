package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleSystemStateRequiresAdmin(t *testing.T) {
	a := &API{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/system-state", nil)
	w := httptest.NewRecorder()

	a.handleSystemState(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestHandleSystemStateMethodNotAllowed(t *testing.T) {
	a := &API{}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/system-state", nil)
	req.Header.Set("X-Alcove-Admin", "true")
	w := httptest.NewRecorder()

	a.handleSystemState(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleSystemStatePutInvalidMode(t *testing.T) {
	a := &API{}
	body := strings.NewReader(`{"mode": "invalid"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/system-state", body)
	req.Header.Set("X-Alcove-Admin", "true")
	w := httptest.NewRecorder()

	a.handleSystemState(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "mode must be 'active' or 'paused'" {
		t.Errorf("unexpected error message: %s", resp["error"])
	}
}

func TestHandleSystemStatePutInvalidBody(t *testing.T) {
	a := &API{}
	body := strings.NewReader(`not json`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/system-state", body)
	req.Header.Set("X-Alcove-Admin", "true")
	w := httptest.NewRecorder()

	a.handleSystemState(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
