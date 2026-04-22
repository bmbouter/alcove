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
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{
			name:     "seconds only",
			duration: 30 * time.Second,
			expected: "30s",
		},
		{
			name:     "less than a second",
			duration: 500 * time.Millisecond,
			expected: "1s", // rounds up to 1s
		},
		{
			name:     "minutes only",
			duration: 5 * time.Minute,
			expected: "5m",
		},
		{
			name:     "minutes and seconds",
			duration: 5*time.Minute + 30*time.Second,
			expected: "5m30s",
		},
		{
			name:     "hours only",
			duration: 2 * time.Hour,
			expected: "2h",
		},
		{
			name:     "hours and minutes",
			duration: 2*time.Hour + 30*time.Minute,
			expected: "2h30m",
		},
		{
			name:     "complex duration",
			duration: 1*time.Hour + 23*time.Minute + 45*time.Second,
			expected: "1h23m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDuration(tt.duration)
			if result != tt.expected {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.duration, result, tt.expected)
			}
		})
	}
}

func TestFormatDurationForDisplay(t *testing.T) {
	now := time.Now()
	past := now.Add(-30 * time.Minute)
	pastRFC3339 := past.Format(time.RFC3339)
	pastRFC3339Nano := past.Format(time.RFC3339Nano)

	tests := []struct {
		name      string
		duration  string
		status    string
		startedAt string
		expected  string
	}{
		{
			name:     "completed session with duration",
			duration: "1h30m45s",
			status:   "completed",
			expected: "1h30m",
		},
		{
			name:     "completed session with short duration",
			duration: "2m15s",
			status:   "completed",
			expected: "2m15s",
		},
		{
			name:     "completed session with very short duration",
			duration: "30s",
			status:   "completed",
			expected: "30s",
		},
		{
			name:      "running session with RFC3339 timestamp",
			duration:  "",
			status:    "running",
			startedAt: pastRFC3339,
			expected:  "30m*", // approximate, depends on timing
		},
		{
			name:      "running session with RFC3339Nano timestamp",
			duration:  "",
			status:    "running",
			startedAt: pastRFC3339Nano,
			expected:  "30m*", // approximate, depends on timing
		},
		{
			name:     "error session without duration",
			duration: "",
			status:   "error",
			expected: "-",
		},
		{
			name:     "invalid duration format",
			duration: "invalid",
			status:   "completed",
			expected: "invalid",
		},
		{
			name:      "running session with invalid timestamp",
			duration:  "",
			status:    "running",
			startedAt: "invalid-timestamp",
			expected:  "-",
		},
		{
			name:      "running session with empty timestamp",
			duration:  "",
			status:    "running",
			startedAt: "",
			expected:  "-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDurationForDisplay(tt.duration, tt.status, tt.startedAt)

			// For running sessions, we can't predict exact timing, so check pattern
			if tt.status == "running" && tt.duration == "" && tt.startedAt != "" && tt.startedAt != "invalid-timestamp" {
				if len(result) == 0 || result[len(result)-1] != '*' {
					t.Errorf("formatDurationForDisplay() for running session should end with '*', got %q", result)
				}
			} else if result != tt.expected {
				t.Errorf("formatDurationForDisplay(%q, %q, %q) = %q, want %q",
					tt.duration, tt.status, tt.startedAt, result, tt.expected)
			}
		})
	}
}
