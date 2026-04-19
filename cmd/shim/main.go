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

// Command shim runs a small HTTP server inside dev containers, providing
// an authenticated endpoint for executing commands with streaming output.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Version is set at build time via -ldflags.
var Version = "dev"

type shimServer struct {
	token      string
	maxTimeout int
	mu         sync.Mutex // serializes command execution
}

type execRequest struct {
	Cmd     string `json:"cmd"`
	Timeout int    `json:"timeout"`
}

type streamLine struct {
	Stream  string  `json:"stream"`
	Data    string  `json:"data,omitempty"`
	Code    *int    `json:"code,omitempty"`
	Error   string  `json:"error,omitempty"`
	Elapsed float64 `json:"elapsed,omitempty"`
}

func intPtr(i int) *int {
	return &i
}

func (s *shimServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
	fmt.Fprint(w, "\n")
}

func (s *shimServer) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Auth check
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Parse request body
	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Cmd == "" {
		writeError(w, http.StatusBadRequest, "cmd is required")
		return
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 60
	}
	if timeout > s.maxTimeout {
		timeout = s.maxTimeout
	}

	// Serialize command execution
	s.mu.Lock()
	defer s.mu.Unlock()

	// Set up streaming response
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", req.Cmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stdout pipe: "+err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stderr pipe: "+err.Error())
		return
	}

	start := time.Now()

	if err := cmd.Start(); err != nil {
		writeError(w, http.StatusInternalServerError, "start: "+err.Error())
		return
	}

	var writeMu sync.Mutex
	clientGone := false

	writeLine := func(line streamLine) bool {
		writeMu.Lock()
		defer writeMu.Unlock()
		if clientGone {
			return false
		}
		data, _ := json.Marshal(line)
		data = append(data, '\n')
		_, writeErr := w.Write(data)
		if writeErr != nil {
			clientGone = true
			cancel()
			return false
		}
		flusher.Flush()
		return true
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Stream stdout
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if !writeLine(streamLine{Stream: "stdout", Data: string(buf[:n])}) {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Stream stderr
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				if !writeLine(streamLine{Stream: "stderr", Data: string(buf[:n])}) {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	wg.Wait()

	waitErr := cmd.Wait()
	elapsed := time.Since(start).Seconds()

	if waitErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeLine(streamLine{
				Stream:  "exit",
				Code:    intPtr(-1),
				Error:   fmt.Sprintf("timeout after %ds", timeout),
				Elapsed: elapsed,
			})
			return
		}
		// Try to extract exit code
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			writeLine(streamLine{
				Stream:  "exit",
				Code:    intPtr(exitErr.ExitCode()),
				Elapsed: elapsed,
			})
			return
		}
		// Unknown error
		writeLine(streamLine{
			Stream:  "exit",
			Code:    intPtr(-1),
			Error:   waitErr.Error(),
			Elapsed: elapsed,
		})
		return
	}

	writeLine(streamLine{
		Stream:  "exit",
		Code:    intPtr(0),
		Elapsed: elapsed,
	})
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func newMux(s *shimServer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/exec", s.handleExec)
	return mux
}

func main() {
	token := os.Getenv("SHIM_TOKEN")
	if token == "" {
		log.Fatal("SHIM_TOKEN is required")
	}

	port := os.Getenv("SHIM_PORT")
	if port == "" {
		port = "9090"
	}

	maxTimeout := 600
	if v := os.Getenv("SHIM_MAX_TIMEOUT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			log.Fatalf("invalid SHIM_MAX_TIMEOUT: %v", err)
		}
		maxTimeout = n
	}

	s := &shimServer{
		token:      token,
		maxTimeout: maxTimeout,
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      newMux(s),
		WriteTimeout: 0, // streaming needs no write deadline
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		log.Printf("shim %s listening on :%s (maxTimeout=%d)", Version, port, maxTimeout)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-stop
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("shutdown error: %v", err)
	}
	log.Println("stopped")
}
