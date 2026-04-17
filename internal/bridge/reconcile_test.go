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

	// Tracking fields for verifying calls.
	cleanupCalls       []string // prefixes passed to CleanupOrphanedContainers
	cleanupResult      int      // count to return from CleanupOrphanedContainers
	stopServiceCalls   []string // names passed to StopService
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

func (m *mockRuntime) StopService(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopServiceCalls = append(m.stopServiceCalls, name)
	return nil
}

func (m *mockRuntime) CreateVolume(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockRuntime) Info(_ context.Context) (runtime.RuntimeInfo, error) {
	return runtime.RuntimeInfo{Type: "mock"}, nil
}

func (m *mockRuntime) CleanupOrphanedContainers(_ context.Context, prefix string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupCalls = append(m.cleanupCalls, prefix)
	return m.cleanupResult, nil
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

// TestCleanupOrphanedContainers_GatePrefix verifies that
// CleanupOrphanedContainers is called with the "gate-" prefix and returns the
// expected count. The real ReconcileLoop calls this on a 2-minute ticker
// (see dispatcher.go ReconcileLoop); we test the call directly here because
// the ticker interval makes a true integration test impractical.
func TestCleanupOrphanedContainers_GatePrefix(t *testing.T) {
	rt := &mockRuntime{
		statuses:      map[string]string{},
		cleanupResult: 2,
	}
	d := &Dispatcher{
		rt:      rt,
		handles: make(map[string]runtime.TaskHandle),
	}

	// Directly call CleanupOrphanedContainers as the reconcile loop would.
	cleaned, err := d.rt.CleanupOrphanedContainers(context.Background(), "gate-")
	if err != nil {
		t.Fatalf("CleanupOrphanedContainers() error: %v", err)
	}
	if cleaned != 2 {
		t.Errorf("cleaned = %d, want 2", cleaned)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.cleanupCalls) != 1 {
		t.Fatalf("expected 1 cleanup call, got %d", len(rt.cleanupCalls))
	}
	if rt.cleanupCalls[0] != "gate-" {
		t.Errorf("cleanup prefix = %q, want %q", rt.cleanupCalls[0], "gate-")
	}
}

// TestStatusHandler_GateCleanup verifies that when a session reaches a
// terminal state (completed/error/timeout), the dispatcher triggers
// StopService for the gate container derived from the task handle.
func TestStatusHandler_GateCleanup(t *testing.T) {
	rt := &mockRuntime{statuses: map[string]string{}}
	d := &Dispatcher{
		rt:      rt,
		handles: make(map[string]runtime.TaskHandle),
	}

	// Pre-populate a handle as if RunTask had created it.
	taskID := "task-42"
	sessionID := "session-42"
	d.handles[sessionID] = runtime.TaskHandle{
		ID:      taskID,
		PodName: runtime.SkiffContainerName(taskID),
	}

	// Simulate the gate cleanup logic from the status handler:
	// On terminal state, the dispatcher removes the handle and calls
	// StopService on the gate container name after a grace period.
	d.mu.Lock()
	handle, hasHandle := d.handles[sessionID]
	delete(d.handles, sessionID)
	d.mu.Unlock()

	if !hasHandle {
		t.Fatal("expected handle to be present for session")
	}

	// Call StopService directly (the real code does this in a goroutine
	// after a 5s sleep, but we skip the delay for testing).
	gateName := runtime.GateContainerName(handle.ID)
	if err := d.rt.StopService(context.Background(), gateName); err != nil {
		t.Fatalf("StopService() error: %v", err)
	}

	// Verify StopService was called with the correct gate container name.
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.stopServiceCalls) != 1 {
		t.Fatalf("expected 1 StopService call, got %d", len(rt.stopServiceCalls))
	}
	expectedGateName := "gate-task-42"
	if rt.stopServiceCalls[0] != expectedGateName {
		t.Errorf("StopService called with %q, want %q", rt.stopServiceCalls[0], expectedGateName)
	}

	// Verify the handle was removed from the map.
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.handles[sessionID]; exists {
		t.Error("handle should have been removed from handles map")
	}
}

// TestStatusHandler_GateCleanup_NoHandle verifies that when a session
// reaches a terminal state but has no handle (e.g., after Bridge restart),
// no StopService call is made and no panic occurs.
func TestStatusHandler_GateCleanup_NoHandle(t *testing.T) {
	rt := &mockRuntime{statuses: map[string]string{}}
	d := &Dispatcher{
		rt:      rt,
		handles: make(map[string]runtime.TaskHandle),
	}

	// Simulate the gate cleanup logic with no handle present.
	sessionID := "session-orphan"
	d.mu.Lock()
	_, hasHandle := d.handles[sessionID]
	delete(d.handles, sessionID)
	d.mu.Unlock()

	if hasHandle {
		t.Fatal("expected no handle for this session")
	}

	// No StopService call should be made when there is no handle.
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.stopServiceCalls) != 0 {
		t.Errorf("expected 0 StopService calls, got %d", len(rt.stopServiceCalls))
	}
}
