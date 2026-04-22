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

package bridge

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRequestLoggingMiddleware(t *testing.T) {
	// Capture log output
	var logOutput bytes.Buffer
	log.SetOutput(&logOutput)
	defer func() {
		log.SetOutput(os.Stderr)
	}()

	// Create test handler
	handler := RequestLoggingMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	}))

	// Test basic request
	t.Run("basic request", func(t *testing.T) {
		logOutput.Reset()

		req := httptest.NewRequest("GET", "/api/v1/health", nil)
		req.Header.Set("User-Agent", "test-agent")
		req.Header.Set("Content-Type", "application/json")

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		logStr := logOutput.String()

		// Check request log
		if !strings.Contains(logStr, "http: request method=GET path=/api/v1/health") {
			t.Errorf("Expected request log, got: %s", logStr)
		}

		// Check response log
		if !strings.Contains(logStr, "http: response method=GET path=/api/v1/health status=OK(200)") {
			t.Errorf("Expected response log, got: %s", logStr)
		}

		// Check that user agent is logged
		if !strings.Contains(logStr, "user_agent=test-agent") {
			t.Errorf("Expected user agent in log, got: %s", logStr)
		}
	})

	// Test request with authentication context
	t.Run("authenticated request", func(t *testing.T) {
		logOutput.Reset()

		req := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"prompt":"test"}`))
		req.Header.Set("X-Alcove-User", "testuser")
		req.Header.Set("X-Alcove-Team-ID", "team123")
		req.Header.Set("X-Alcove-Admin", "true")
		req.Header.Set("Content-Type", "application/json")

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		logStr := logOutput.String()

		// Check authentication context
		if !strings.Contains(logStr, "user=testuser") {
			t.Errorf("Expected user in log, got: %s", logStr)
		}

		if !strings.Contains(logStr, "team=team123") {
			t.Errorf("Expected team in log, got: %s", logStr)
		}

		if !strings.Contains(logStr, "admin=true") {
			t.Errorf("Expected admin flag in log, got: %s", logStr)
		}
	})

	// Test request with query parameters
	t.Run("request with query", func(t *testing.T) {
		logOutput.Reset()

		req := httptest.NewRequest("GET", "/api/v1/sessions?status=running&page=1", nil)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		logStr := logOutput.String()

		// Check query parameters are logged
		if !strings.Contains(logStr, "query=status=running&page=1") {
			t.Errorf("Expected query params in log, got: %s", logStr)
		}
	})

	// Test forwarded headers
	t.Run("forwarded headers", func(t *testing.T) {
		logOutput.Reset()

		req := httptest.NewRequest("GET", "/api/v1/health", nil)
		req.Header.Set("X-Forwarded-For", "192.168.1.100")

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		logStr := logOutput.String()

		// Check forwarded IP is logged
		if !strings.Contains(logStr, "remote_addr=192.168.1.100") {
			t.Errorf("Expected forwarded IP in log, got: %s", logStr)
		}
	})
}

func TestResponseWriter(t *testing.T) {
	t.Run("captures status code and size", func(t *testing.T) {
		rr := httptest.NewRecorder()
		rw := &responseWriter{ResponseWriter: rr}

		rw.WriteHeader(http.StatusNotFound)
		n, err := rw.Write([]byte("not found"))

		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		if n != 9 {
			t.Errorf("Expected 9 bytes written, got %d", n)
		}

		if rw.statusCode != http.StatusNotFound {
			t.Errorf("Expected status code 404, got %d", rw.statusCode)
		}

		if rw.size != 9 {
			t.Errorf("Expected size 9, got %d", rw.size)
		}
	})

	t.Run("defaults to 200 OK", func(t *testing.T) {
		rr := httptest.NewRecorder()
		rw := &responseWriter{ResponseWriter: rr}

		rw.Write([]byte("ok"))

		if rw.statusCode != http.StatusOK {
			t.Errorf("Expected status code 200, got %d", rw.statusCode)
		}
	})
}

func TestItoa(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{123, "123"},
		{-1, "-1"},
		{-42, "-42"},
		{-123, "-123"},
	}

	for _, test := range tests {
		result := itoa(test.input)
		if result != test.expected {
			t.Errorf("itoa(%d) = %s, expected %s", test.input, result, test.expected)
		}
	}
}