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
	"log"
	"net/http"
	"strings"
	"time"
)

// responseWriter wraps http.ResponseWriter to capture status code and response size
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	size       int
}

func (rw *responseWriter) WriteHeader(statusCode int) {
	rw.statusCode = statusCode
	rw.ResponseWriter.WriteHeader(statusCode)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}
	size, err := rw.ResponseWriter.Write(b)
	rw.size += size
	return size, err
}

// RequestLoggingMiddleware logs HTTP request details for debugging
func RequestLoggingMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Create wrapped response writer to capture status and size
			wrapped := &responseWriter{ResponseWriter: w, statusCode: 0, size: 0}

			// Log request details
			logRequest(r)

			// Process request
			next.ServeHTTP(wrapped, r)

			// Log response details
			duration := time.Since(start)
			logResponse(r, wrapped.statusCode, wrapped.size, duration)
		})
	}
}

// logRequest logs incoming HTTP request details
func logRequest(r *http.Request) {
	// Get basic request info
	method := r.Method
	path := r.URL.Path
	query := r.URL.RawQuery
	userAgent := r.Header.Get("User-Agent")
	contentType := r.Header.Get("Content-Type")
	contentLength := r.Header.Get("Content-Length")

	// Get authentication context if present
	user := r.Header.Get("X-Alcove-User")
	team := r.Header.Get("X-Alcove-Team-ID")
	isAdmin := r.Header.Get("X-Alcove-Admin") == "true"

	// Build log message
	logMsg := []string{
		"request",
		"method=" + method,
		"path=" + path,
	}

	if query != "" {
		logMsg = append(logMsg, "query="+query)
	}

	if user != "" {
		logMsg = append(logMsg, "user="+user)
	}

	if team != "" {
		logMsg = append(logMsg, "team="+team)
	}

	if isAdmin {
		logMsg = append(logMsg, "admin=true")
	}

	if contentType != "" {
		logMsg = append(logMsg, "content_type="+contentType)
	}

	if contentLength != "" {
		logMsg = append(logMsg, "content_length="+contentLength)
	}

	if userAgent != "" {
		logMsg = append(logMsg, "user_agent="+userAgent)
	}

	// Add remote address
	remoteAddr := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		remoteAddr = forwarded
	}
	logMsg = append(logMsg, "remote_addr="+remoteAddr)

	log.Printf("http: %s", strings.Join(logMsg, " "))
}

// logResponse logs HTTP response details
func logResponse(r *http.Request, statusCode, size int, duration time.Duration) {
	// Build log message
	logMsg := []string{
		"response",
		"method=" + r.Method,
		"path=" + r.URL.Path,
		"status=" + http.StatusText(statusCode) + "(" + itoa(statusCode) + ")",
		"size=" + itoa(size) + "b",
		"duration=" + duration.String(),
	}

	// Add user context if present
	if user := r.Header.Get("X-Alcove-User"); user != "" {
		logMsg = append(logMsg, "user="+user)
	}

	log.Printf("http: %s", strings.Join(logMsg, " "))
}

// itoa converts int to string (simple alternative to strconv.Itoa)
func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	// Handle negative numbers
	negative := i < 0
	if negative {
		i = -i
	}

	// Convert to string
	var buf [20]byte // enough for 64-bit int
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte(i%10) + '0'
		i /= 10
	}

	if negative {
		pos--
		buf[pos] = '-'
	}

	return string(buf[pos:])
}