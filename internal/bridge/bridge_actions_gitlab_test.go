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
	"testing"
)

func TestGetIntSliceInput(t *testing.T) {
	tests := []struct {
		name     string
		inputs   map[string]interface{}
		key      string
		expected []int
	}{
		{
			name: "int slice",
			inputs: map[string]interface{}{
				"test": []int{1, 2, 3},
			},
			key:      "test",
			expected: []int{1, 2, 3},
		},
		{
			name: "interface slice with mixed types",
			inputs: map[string]interface{}{
				"test": []interface{}{1, 2.0, "3"},
			},
			key:      "test",
			expected: []int{1, 2, 3},
		},
		{
			name: "interface slice with invalid string",
			inputs: map[string]interface{}{
				"test": []interface{}{1, 2.0, "invalid"},
			},
			key:      "test",
			expected: []int{1, 2},
		},
		{
			name: "missing key",
			inputs: map[string]interface{}{
				"other": []int{1, 2, 3},
			},
			key:      "test",
			expected: nil,
		},
		{
			name: "empty slice",
			inputs: map[string]interface{}{
				"test": []interface{}{},
			},
			key:      "test",
			expected: []int{},
		},
		{
			name: "wrong type",
			inputs: map[string]interface{}{
				"test": "not a slice",
			},
			key:      "test",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getIntSliceInput(tt.inputs, tt.key)

			if len(result) != len(tt.expected) {
				t.Errorf("Expected length %d, got: %d", len(tt.expected), len(result))
				return
			}

			for i, expected := range tt.expected {
				if result[i] != expected {
					t.Errorf("Expected result[%d]=%d, got: %d", i, expected, result[i])
				}
			}
		})
	}
}
