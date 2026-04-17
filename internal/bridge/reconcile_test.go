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
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bmbouter/alcove/internal/runtime"
)

// mockRuntime is a minimal Runtime implementation for testing reconciliation.
type mockRuntime struct {
	statuses map[string]string // taskID -> status
	mu       sync.Mutex
}

func (m *mockRuntime) RunTask(_ context.Context, _ runtime.TaskSpec) (runtime.TaskHandle, error) {
	return runtime.TaskHandle{}, nil
}

func (m *mockRuntime) CancelTask(_ context.Context, _ runtime.TaskHandle) error {
	return nil
}

func (m *mockRuntime) TaskStatus(_ context.Context, handle runtime.TaskHandle) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if status, ok := m.statuses[handle.ID]; ok {
		return status, nil
	}
	return "not_found", nil
}

func (m *mockRuntime) EnsureService(_ context.Context, _ runtime.ServiceSpec) error {
	return nil
}

func (m *mockRuntime) StopService(_ context.Context, _ string) error {
	return nil
}

func (m *mockRuntime) CreateVolume(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockRuntime) Info(_ context.Context) (runtime.RuntimeInfo, error) {
	return runtime.RuntimeInfo{Type: "mock"}, nil
}

func (m *mockRuntime) CleanupOrphanedContainers(_ context.Context, _ string) (int, error) {
	return 0, nil
}

// TestReconcileLoop_ContextCancellation verifies that ReconcileLoop exits
// when its context is cancelled.
func TestReconcileLoop_ContextCancellation(t *testing.T) {
	d := &Dispatcher{
		rt:      &mockRuntime{statuses: map[string]string{}},
		handles: make(map[string]runtime.TaskHandle),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.ReconcileLoop(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// ReconcileLoop exited as expected.
	case <-time.After(5 * time.Second):
		t.Fatal("ReconcileLoop did not exit after context cancellation")
	}
}

// TestMockRuntime_TaskStatus_NotFound verifies that the mock runtime
// returns "not_found" for unknown task IDs, matching the real runtime
// behavior that RecoverHandles depends on.
func TestMockRuntime_TaskStatus_NotFound(t *testing.T) {
	rt := &mockRuntime{statuses: map[string]string{}}

	status, err := rt.TaskStatus(context.Background(), runtime.TaskHandle{ID: "nonexistent"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "not_found" {
		t.Errorf("status = %q, want %q", status, "not_found")
	}
}

// TestMockRuntime_TaskStatus_Running verifies the mock runtime returns
// "running" when configured, matching the status values used by
// RecoverHandles.
func TestMockRuntime_TaskStatus_Running(t *testing.T) {
	rt := &mockRuntime{statuses: map[string]string{"task-1": "running"}}

	status, err := rt.TaskStatus(context.Background(), runtime.TaskHandle{ID: "task-1"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want %q", status, "running")
	}
}

// TestMockRuntime_TaskStatus_Exited verifies the mock runtime returns
// "exited" when configured, matching the status that triggers
// RecoverHandles to mark sessions as completed.
func TestMockRuntime_TaskStatus_Exited(t *testing.T) {
	rt := &mockRuntime{statuses: map[string]string{"task-2": "exited"}}

	status, err := rt.TaskStatus(context.Background(), runtime.TaskHandle{ID: "task-2"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "exited" {
		t.Errorf("status = %q, want %q", status, "exited")
	}
}

// TestRecoverHandles_NoDBConnection verifies that RecoverHandles
// gracefully handles a nil DB pool (logs and returns without panic).
func TestRecoverHandles_NoDBConnection(t *testing.T) {
	d := &Dispatcher{
		rt:      &mockRuntime{statuses: map[string]string{}},
		handles: make(map[string]runtime.TaskHandle),
		// db is nil — RecoverHandles should log an error and return.
	}

	// This should not panic.
	d.RecoverHandles(context.Background())

	// Verify handles map is still empty.
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.handles) != 0 {
		t.Errorf("expected empty handles map, got %d entries", len(d.handles))
	}
}
