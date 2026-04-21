package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	events := []map[string]any{
		{"type": "text", "content": "test-agent: starting", "timestamp": time.Now().UTC().Format(time.RFC3339)},
		{"type": "text", "content": "test-agent: working", "timestamp": time.Now().UTC().Format(time.RFC3339)},
		{"type": "text", "content": "test-agent: ALCOVE_TEST_MARKER", "timestamp": time.Now().UTC().Format(time.RFC3339)},
	}
	for _, e := range events {
		b, _ := json.Marshal(e)
		fmt.Println(string(b))
	}
	outputs := map[string]string{"test_status": "passed", "agent_version": "1.0.0"}
	data, _ := json.Marshal(outputs)
	_ = os.WriteFile("/tmp/alcove-outputs.json", data, 0644)
}
