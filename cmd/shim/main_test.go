// Copyright 2026 Brian Bouterse
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "test-secret-token"

func newTestServer() *shimServer {
	return &shimServer{token: testToken, maxTimeout: 600}
}

func newTestMux(s *shimServer) http.Handler {
	return newMux(s)
}

// execAndCollect performs a POST /exec request and collects all NDJSON lines.
func execAndCollect(t *testing.T, mux http.Handler, body string) (*http.Response, []streamLine) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/exec", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	resp := w.Result()

	var lines []streamLine
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var sl streamLine
		if err := json.Unmarshal([]byte(line), &sl); err != nil {
			t.Fatalf("failed to parse NDJSON line %q: %v", line, err)
		}
		lines = append(lines, sl)
	}
	return resp, lines
}

func TestHealthz(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", result["status"])
	}
}

func TestExecValidCommand(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	_, lines := execAndCollect(t, mux, `{"cmd":"echo hello"}`)

	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d", len(lines))
	}

	// Find stdout line
	foundStdout := false
	for _, l := range lines {
		if l.Stream == "stdout" && strings.Contains(l.Data, "hello") {
			foundStdout = true
		}
	}
	if !foundStdout {
		t.Fatal("expected stdout line containing 'hello'")
	}

	// Last line should be exit
	last := lines[len(lines)-1]
	if last.Stream != "exit" {
		t.Fatalf("expected last line stream=exit, got %q", last.Stream)
	}
	if last.Code == nil || *last.Code != 0 {
		t.Fatalf("expected exit code 0, got %v", last.Code)
	}
}

func TestExecUnauthorized(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	req := httptest.NewRequest(http.MethodPost, "/exec", strings.NewReader(`{"cmd":"echo hi"}`))
	// No auth header
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Result().StatusCode)
	}
}

func TestExecInvalidJSON(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	req := httptest.NewRequest(http.MethodPost, "/exec", strings.NewReader(`{bad json`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Result().StatusCode)
	}
}

func TestExecTimeout(t *testing.T) {
	s := &shimServer{token: testToken, maxTimeout: 5}
	mux := newTestMux(s)

	start := time.Now()
	_, lines := execAndCollect(t, mux, `{"cmd":"sleep 10","timeout":1}`)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}

	last := lines[len(lines)-1]
	if last.Stream != "exit" {
		t.Fatalf("expected exit stream, got %q", last.Stream)
	}
	if last.Code == nil || *last.Code != -1 {
		t.Fatalf("expected exit code -1, got %v", last.Code)
	}
	if !strings.Contains(last.Error, "timeout") {
		t.Fatalf("expected timeout error, got %q", last.Error)
	}
}

func TestExecFailingCommand(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	_, lines := execAndCollect(t, mux, `{"cmd":"exit 42"}`)

	last := lines[len(lines)-1]
	if last.Stream != "exit" {
		t.Fatalf("expected exit stream, got %q", last.Stream)
	}
	if last.Code == nil || *last.Code != 42 {
		t.Fatalf("expected exit code 42, got %v", last.Code)
	}
}

func TestExecConcurrentSerialized(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)

	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/exec", strings.NewReader(`{"cmd":"sleep 0.3"}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	if elapsed < 500*time.Millisecond {
		t.Fatalf("expected serialized execution >= 500ms, got %v", elapsed)
	}
}

func TestExecMissingCmd(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	req := httptest.NewRequest(http.MethodPost, "/exec", strings.NewReader(`{"cmd":""}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Result().StatusCode)
	}
}

func TestExecWrongMethod(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	req := httptest.NewRequest(http.MethodGet, "/exec", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Result().StatusCode)
	}
}

func TestExecWrongToken(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	req := httptest.NewRequest(http.MethodPost, "/exec", strings.NewReader(`{"cmd":"echo hi"}`))
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Result().StatusCode)
	}
}

func TestExecDefaultTimeout(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	// Omit timeout, command should run with default (60s)
	_, lines := execAndCollect(t, mux, `{"cmd":"echo default"}`)

	foundStdout := false
	for _, l := range lines {
		if l.Stream == "stdout" && strings.Contains(l.Data, "default") {
			foundStdout = true
		}
	}
	if !foundStdout {
		t.Fatal("expected stdout line containing 'default'")
	}

	last := lines[len(lines)-1]
	if last.Code == nil || *last.Code != 0 {
		t.Fatalf("expected exit code 0, got %v", last.Code)
	}
}

func TestExecTimeoutClamped(t *testing.T) {
	s := &shimServer{token: testToken, maxTimeout: 2}
	mux := newTestMux(s)

	start := time.Now()
	_, lines := execAndCollect(t, mux, `{"cmd":"sleep 10","timeout":9999}`)
	elapsed := time.Since(start)

	// Should be killed at ~2s, not 9999s
	if elapsed > 5*time.Second {
		t.Fatalf("expected clamped timeout ~2s, took %v", elapsed)
	}

	last := lines[len(lines)-1]
	if last.Code == nil || *last.Code != -1 {
		t.Fatalf("expected exit code -1, got %v", last.Code)
	}
	if !strings.Contains(last.Error, "timeout") {
		t.Fatalf("expected timeout error, got %q", last.Error)
	}
}

func TestExecMixedOutput(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	_, lines := execAndCollect(t, mux, `{"cmd":"echo out && echo err >&2 && echo out2"}`)

	foundStdout := false
	foundStderr := false
	for _, l := range lines {
		if l.Stream == "stdout" {
			foundStdout = true
		}
		if l.Stream == "stderr" {
			foundStderr = true
		}
	}
	if !foundStdout {
		t.Fatal("expected stdout stream")
	}
	if !foundStderr {
		t.Fatal("expected stderr stream")
	}
}

func TestExecStderr(t *testing.T) {
	s := newTestServer()
	mux := newTestMux(s)

	_, lines := execAndCollect(t, mux, `{"cmd":"echo errout >&2"}`)

	foundStderr := false
	for _, l := range lines {
		if l.Stream == "stderr" && strings.Contains(l.Data, "errout") {
			foundStderr = true
		}
	}
	if !foundStderr {
		t.Fatal("expected stderr line containing 'errout'")
	}

	last := lines[len(lines)-1]
	if last.Stream != "exit" {
		t.Fatalf("expected exit stream, got %q", last.Stream)
	}
	if last.Code == nil || *last.Code != 0 {
		t.Fatalf("expected exit code 0, got %v", last.Code)
	}
}
