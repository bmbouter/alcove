package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	// Parse --sleep flag
	sleepDuration := 0
	for i, arg := range os.Args[1:] {
		if arg == "--sleep" && i+1 < len(os.Args)-1 {
			sleepDuration, _ = strconv.Atoi(os.Args[i+2])
		}
	}

	events := []map[string]any{
		{"type": "text", "content": "test-agent: starting", "timestamp": time.Now().UTC().Format(time.RFC3339)},
		{"type": "text", "content": "test-agent: working", "timestamp": time.Now().UTC().Format(time.RFC3339)},
		{"type": "text", "content": "test-agent: ALCOVE_TEST_MARKER", "timestamp": time.Now().UTC().Format(time.RFC3339)},
	}
	for _, e := range events {
		b, _ := json.Marshal(e)
		fmt.Println(string(b))
	}

	// Write to stderr so transcript captures both streams
	stderrEvents := []map[string]any{
		{"type": "text", "content": "test-agent: stderr-line-1", "timestamp": time.Now().UTC().Format(time.RFC3339)},
		{"type": "text", "content": "test-agent: STDERR_MARKER", "timestamp": time.Now().UTC().Format(time.RFC3339)},
	}
	for _, e := range stderrEvents {
		b, _ := json.Marshal(e)
		fmt.Fprintln(os.Stderr, string(b))
	}

	outputs := map[string]string{"test_status": "passed", "agent_version": "1.0.0"}
	data, _ := json.Marshal(outputs)
	_ = os.WriteFile("/tmp/alcove-outputs.json", data, 0644)

	if sleepDuration > 0 {
		// Handle SIGTERM gracefully
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		select {
		case <-time.After(time.Duration(sleepDuration) * time.Second):
		case <-sig:
		}
	}
}
