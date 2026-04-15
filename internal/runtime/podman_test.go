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

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// fakeExecCommand returns a function that creates exec.Cmd pointing to the
// test binary itself with a special env var, so we can control the output.
// The "args" passed to the command are recorded for later inspection.
func fakeExecCommand(t *testing.T, stdout string, exitCode int) (func(ctx context.Context, name string, args ...string) *exec.Cmd, *[][]string) {
	t.Helper()
	var calls [][]string

	fn := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))

		cs := []string{"-test.run=TestHelperProcess", "--", name}
		cs = append(cs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = []string{
			"GO_WANT_HELPER_PROCESS=1",
			fmt.Sprintf("GO_HELPER_STDOUT=%s", stdout),
			fmt.Sprintf("GO_HELPER_EXIT_CODE=%d", exitCode),
		}
		return cmd
	}
	return fn, &calls
}

// fakeExecCommandMulti returns a function that returns different outputs
// based on the call index.
func fakeExecCommandMulti(t *testing.T, responses []fakeResponse) (func(ctx context.Context, name string, args ...string) *exec.Cmd, *[][]string) {
	t.Helper()
	var calls [][]string
	callIdx := 0

	fn := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		resp := responses[0]
		if callIdx < len(responses) {
			resp = responses[callIdx]
		}
		callIdx++

		cs := []string{"-test.run=TestHelperProcess", "--", name}
		cs = append(cs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = []string{
			"GO_WANT_HELPER_PROCESS=1",
			fmt.Sprintf("GO_HELPER_STDOUT=%s", resp.stdout),
			fmt.Sprintf("GO_HELPER_EXIT_CODE=%d", resp.exitCode),
		}
		return cmd
	}
	return fn, &calls
}

type fakeResponse struct {
	stdout   string
	exitCode int
}

// TestHelperProcess is not a real test. It is used as a fake podman binary
// by the fakeExecCommand helper. See https://npf.io/2015/06/testing-exec-command/
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, os.Getenv("GO_HELPER_STDOUT"))
	exitCode := 0
	if code := os.Getenv("GO_HELPER_EXIT_CODE"); code != "" && code != "0" {
		exitCode = 1
	}
	os.Exit(exitCode)
}

func TestContainerNaming(t *testing.T) {
	tests := []struct {
		taskID     string
		wantSkiff  string
		wantGate   string
	}{
		{"abc123", "skiff-abc123", "gate-abc123"},
		{"task-uuid-1234", "skiff-task-uuid-1234", "gate-task-uuid-1234"},
		{"", "skiff-", "gate-"},
	}

	for _, tt := range tests {
		if got := SkiffContainerName(tt.taskID); got != tt.wantSkiff {
			t.Errorf("SkiffContainerName(%q) = %q, want %q", tt.taskID, got, tt.wantSkiff)
		}
		if got := GateContainerName(tt.taskID); got != tt.wantGate {
			t.Errorf("GateContainerName(%q) = %q, want %q", tt.taskID, got, tt.wantGate)
		}
	}
}

func TestRunTask_CommandConstruction(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "container-id-123\n", 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	spec := TaskSpec{
		TaskID:      "task-1",
		Image:       "quay.io/alcove/skiff:latest",
		GateImage:   "quay.io/alcove/gate:latest",
		Env:         map[string]string{"TASK_ID": "task-1"},
		GateEnv:     map[string]string{"GATE_SCOPE": "read"},
		Network:     "test-internal",
		ExternalNet: "test-external",
	}

	handle, err := p.RunTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}
	if handle.ID != "task-1" {
		t.Errorf("handle.ID = %q, want %q", handle.ID, "task-1")
	}
	if handle.PodName != "skiff-task-1" {
		t.Errorf("handle.PodName = %q, want %q", handle.PodName, "skiff-task-1")
	}

	if len(*calls) < 2 {
		t.Fatalf("expected at least 2 podman calls, got %d", len(*calls))
	}

	// First call: gate sidecar — should be on BOTH internal and external networks.
	gateCall := (*calls)[0]
	gateArgs := strings.Join(gateCall, " ")
	if !strings.Contains(gateArgs, "--name gate-task-1") {
		t.Errorf("gate call missing --name gate-task-1: %s", gateArgs)
	}
	if !strings.Contains(gateArgs, "--network test-internal,test-external") {
		t.Errorf("gate call missing dual network (test-internal,test-external): %s", gateArgs)
	}
	if !strings.Contains(gateArgs, "quay.io/alcove/gate:latest") {
		t.Errorf("gate call missing gate image: %s", gateArgs)
	}
	if !strings.Contains(gateArgs, "GATE_SCOPE=read") {
		t.Errorf("gate call missing GATE_SCOPE env: %s", gateArgs)
	}

	// Second call: skiff container — should be on internal network ONLY.
	skiffCall := (*calls)[1]
	skiffArgs := strings.Join(skiffCall, " ")
	if !strings.Contains(skiffArgs, "--name skiff-task-1") {
		t.Errorf("skiff call missing --name skiff-task-1: %s", skiffArgs)
	}
	if !strings.Contains(skiffArgs, "--network test-internal") {
		t.Errorf("skiff call missing --network test-internal: %s", skiffArgs)
	}
	// Skiff must NOT be on the external network.
	if strings.Contains(skiffArgs, "test-external") {
		t.Errorf("skiff call must NOT include external network: %s", skiffArgs)
	}
	if !strings.Contains(skiffArgs, "quay.io/alcove/skiff:latest") {
		t.Errorf("skiff call missing skiff image: %s", skiffArgs)
	}
	if !strings.Contains(skiffArgs, "TASK_ID=task-1") {
		t.Errorf("skiff call missing TASK_ID env: %s", skiffArgs)
	}
	// Verify proxy env vars are injected.
	if !strings.Contains(skiffArgs, "HTTP_PROXY=http://gate-task-1:8443") {
		t.Errorf("skiff call missing HTTP_PROXY: %s", skiffArgs)
	}
	if !strings.Contains(skiffArgs, "HTTPS_PROXY=http://gate-task-1:8443") {
		t.Errorf("skiff call missing HTTPS_PROXY: %s", skiffArgs)
	}
}

func TestRunTask_DefaultNetwork(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "ok\n", 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	spec := TaskSpec{
		TaskID:    "task-2",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		// Network and ExternalNet intentionally omitted to test defaults.
	}

	_, err := p.RunTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	// Gate (first call) should use both default networks.
	gateArgs := strings.Join((*calls)[0], " ")
	if !strings.Contains(gateArgs, "--network alcove-internal,alcove-external") {
		t.Errorf("gate call missing default dual network: %s", gateArgs)
	}

	// Skiff (second call) should use only the internal default network.
	skiffArgs := strings.Join((*calls)[1], " ")
	if !strings.Contains(skiffArgs, "--network alcove-internal") {
		t.Errorf("skiff call missing default internal network: %s", skiffArgs)
	}
	if strings.Contains(skiffArgs, "alcove-external") {
		t.Errorf("skiff call must NOT include external network: %s", skiffArgs)
	}
}

func TestCancelTask_CommandConstruction(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "", 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	handle := TaskHandle{ID: "task-1", PodName: "skiff-task-1"}
	err := p.CancelTask(context.Background(), handle)
	if err != nil {
		t.Fatalf("CancelTask() error: %v", err)
	}

	// Should have 4 calls: stop skiff, rm skiff, stop gate, rm gate.
	if len(*calls) != 4 {
		t.Fatalf("expected 4 podman calls, got %d", len(*calls))
	}

	// Verify stop calls include --time 10.
	stopSkiff := strings.Join((*calls)[0], " ")
	if !strings.Contains(stopSkiff, "stop --time 10 skiff-task-1") {
		t.Errorf("expected stop skiff call, got: %s", stopSkiff)
	}
	rmSkiff := strings.Join((*calls)[1], " ")
	if !strings.Contains(rmSkiff, "rm -f skiff-task-1") {
		t.Errorf("expected rm skiff call, got: %s", rmSkiff)
	}
	stopGate := strings.Join((*calls)[2], " ")
	if !strings.Contains(stopGate, "stop --time 10 gate-task-1") {
		t.Errorf("expected stop gate call, got: %s", stopGate)
	}
	rmGate := strings.Join((*calls)[3], " ")
	if !strings.Contains(rmGate, "rm -f gate-task-1") {
		t.Errorf("expected rm gate call, got: %s", rmGate)
	}
}

func TestTaskStatus_Running(t *testing.T) {
	inspectJSON := mustMarshal(t, []podmanInspect{
		{State: podmanContainerState{Status: "running", Running: true}},
	})
	execFn, _ := fakeExecCommand(t, string(inspectJSON), 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	status, err := p.TaskStatus(context.Background(), TaskHandle{ID: "task-1"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want %q", status, "running")
	}
}

func TestTaskStatus_Exited(t *testing.T) {
	inspectJSON := mustMarshal(t, []podmanInspect{
		{State: podmanContainerState{Status: "exited", ExitCode: 0}},
	})
	execFn, _ := fakeExecCommand(t, string(inspectJSON), 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	status, err := p.TaskStatus(context.Background(), TaskHandle{ID: "task-1"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "exited" {
		t.Errorf("status = %q, want %q", status, "exited")
	}
}

func TestTaskStatus_NotFound(t *testing.T) {
	// When inspect fails (container doesn't exist), return "not_found".
	execFn, _ := fakeExecCommand(t, "", 1)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	status, err := p.TaskStatus(context.Background(), TaskHandle{ID: "nonexistent"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "not_found" {
		t.Errorf("status = %q, want %q", status, "not_found")
	}
}

func TestEnsureService_AlreadyRunning(t *testing.T) {
	psJSON := mustMarshal(t, []podmanPsEntry{
		{Names: []string{"hail"}, State: "running"},
	})
	execFn, calls := fakeExecCommand(t, string(psJSON), 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	spec := ServiceSpec{
		Name:  "hail",
		Image: "nats:latest",
	}
	err := p.EnsureService(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureService() error: %v", err)
	}

	// Only the ps check should be called, not run.
	if len(*calls) != 1 {
		t.Errorf("expected 1 call (ps check only), got %d", len(*calls))
	}
	firstCall := strings.Join((*calls)[0], " ")
	if !strings.Contains(firstCall, "ps") {
		t.Errorf("expected ps call, got: %s", firstCall)
	}
}

func TestEnsureService_NotRunning(t *testing.T) {
	responses := []fakeResponse{
		{stdout: "[]", exitCode: 0},   // ps returns empty
		{stdout: "cid\n", exitCode: 0}, // run succeeds
	}
	execFn, calls := fakeExecCommandMulti(t, responses)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	spec := ServiceSpec{
		Name:    "ledger",
		Image:   "postgres:16",
		Network: "alcove-dev",
		Env:     map[string]string{"POSTGRES_PASSWORD": "dev"},
		Ports:   map[int]int{5432: 5432},
		Volumes: map[string]string{"ledger-data": "/var/lib/postgresql/data"},
	}
	err := p.EnsureService(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureService() error: %v", err)
	}

	if len(*calls) != 2 {
		t.Fatalf("expected 2 calls (ps + run), got %d", len(*calls))
	}

	runCall := strings.Join((*calls)[1], " ")
	if !strings.Contains(runCall, "--name ledger") {
		t.Errorf("run call missing --name ledger: %s", runCall)
	}
	if !strings.Contains(runCall, "--network alcove-dev") {
		t.Errorf("run call missing --network: %s", runCall)
	}
	if !strings.Contains(runCall, "POSTGRES_PASSWORD=dev") {
		t.Errorf("run call missing env var: %s", runCall)
	}
	if !strings.Contains(runCall, "postgres:16") {
		t.Errorf("run call missing image: %s", runCall)
	}
}

func TestStopService_CommandConstruction(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "", 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	err := p.StopService(context.Background(), "hail")
	if err != nil {
		t.Fatalf("StopService() error: %v", err)
	}

	if len(*calls) != 2 {
		t.Fatalf("expected 2 calls (stop + rm), got %d", len(*calls))
	}
	stopCall := strings.Join((*calls)[0], " ")
	if !strings.Contains(stopCall, "stop --time 10 hail") {
		t.Errorf("expected stop call, got: %s", stopCall)
	}
	rmCall := strings.Join((*calls)[1], " ")
	if !strings.Contains(rmCall, "rm -f hail") {
		t.Errorf("expected rm call, got: %s", rmCall)
	}
}

func TestCreateVolume_CommandConstruction(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "ledger-data\n", 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	name, err := p.CreateVolume(context.Background(), "ledger-data")
	if err != nil {
		t.Fatalf("CreateVolume() error: %v", err)
	}
	if name != "ledger-data" {
		t.Errorf("CreateVolume() = %q, want %q", name, "ledger-data")
	}

	if len(*calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*calls))
	}
	call := strings.Join((*calls)[0], " ")
	if !strings.Contains(call, "volume create ledger-data") {
		t.Errorf("expected volume create call, got: %s", call)
	}
}

func TestInfo_ParsesVersion(t *testing.T) {
	versionJSON := `{"Client":{"Version":"4.9.3"}}`
	execFn, _ := fakeExecCommand(t, versionJSON, 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	info, err := p.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error: %v", err)
	}
	if info.Type != "podman" {
		t.Errorf("info.Type = %q, want %q", info.Type, "podman")
	}
	if info.Version != "4.9.3" {
		t.Errorf("info.Version = %q, want %q", info.Version, "4.9.3")
	}
}

func TestRunTask_ProxyEnvNotOverridden(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "ok\n", 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	spec := TaskSpec{
		TaskID:    "task-3",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		Env: map[string]string{
			"HTTP_PROXY":  "http://custom-proxy:9999",
			"HTTPS_PROXY": "http://custom-proxy:9999",
		},
		Network:     "test-net",
		ExternalNet: "test-ext",
	}

	_, err := p.RunTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	// The skiff call (second call) should use the custom proxy, not the default.
	skiffCall := strings.Join((*calls)[1], " ")
	if !strings.Contains(skiffCall, "HTTP_PROXY=http://custom-proxy:9999") {
		t.Errorf("custom HTTP_PROXY was overridden: %s", skiffCall)
	}
}

func TestEnsureNetworks_CreatesInternalAndExternal(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "net-id\n", 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	err := p.EnsureNetworks(context.Background(), "my-internal", "my-external")
	if err != nil {
		t.Fatalf("EnsureNetworks() error: %v", err)
	}

	if len(*calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(*calls))
	}

	// First call: create internal network with --internal flag.
	internalCall := strings.Join((*calls)[0], " ")
	if !strings.Contains(internalCall, "network create --internal my-internal") {
		t.Errorf("expected internal network create, got: %s", internalCall)
	}

	// Second call: create external network without --internal flag.
	externalCall := strings.Join((*calls)[1], " ")
	if !strings.Contains(externalCall, "network create my-external") {
		t.Errorf("expected external network create, got: %s", externalCall)
	}
	if strings.Contains(externalCall, "--internal") {
		t.Errorf("external network must NOT have --internal flag: %s", externalCall)
	}
}

func TestEnsureNetworks_DefaultNames(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "net-id\n", 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	err := p.EnsureNetworks(context.Background(), "", "")
	if err != nil {
		t.Fatalf("EnsureNetworks() error: %v", err)
	}

	internalCall := strings.Join((*calls)[0], " ")
	if !strings.Contains(internalCall, "network create --internal alcove-internal") {
		t.Errorf("expected default internal name, got: %s", internalCall)
	}
	externalCall := strings.Join((*calls)[1], " ")
	if !strings.Contains(externalCall, "network create alcove-external") {
		t.Errorf("expected default external name, got: %s", externalCall)
	}
}

func TestRunTask_DirectOutbound(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "container-id-123\n", 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	spec := TaskSpec{
		TaskID:         "task-do",
		Image:          "quay.io/alcove/skiff:latest",
		GateImage:      "quay.io/alcove/gate:latest",
		Env:            map[string]string{"TASK_ID": "task-do"},
		GateEnv:        map[string]string{"GATE_SCOPE": "read"},
		Network:        "test-internal",
		ExternalNet:    "test-external",
		DirectOutbound: true,
	}

	_, err := p.RunTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	if len(*calls) < 2 {
		t.Fatalf("expected at least 2 podman calls, got %d", len(*calls))
	}

	// Skiff (second call) should be on BOTH internal and external networks.
	skiffArgs := strings.Join((*calls)[1], " ")
	if !strings.Contains(skiffArgs, "--network test-internal,test-external") {
		t.Errorf("skiff call should include both networks when DirectOutbound=true: %s", skiffArgs)
	}
	// HTTP_PROXY and HTTPS_PROXY must NOT be set.
	if strings.Contains(skiffArgs, "HTTP_PROXY=") {
		t.Errorf("skiff call must NOT include HTTP_PROXY when DirectOutbound=true: %s", skiffArgs)
	}
	if strings.Contains(skiffArgs, "HTTPS_PROXY=") {
		t.Errorf("skiff call must NOT include HTTPS_PROXY when DirectOutbound=true: %s", skiffArgs)
	}
}

func TestRunTask_NoDirectOutbound(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "container-id-123\n", 0)
	p := &PodmanRuntime{
		PodmanBin:   "podman",
		execCommand: execFn,
	}

	spec := TaskSpec{
		TaskID:         "task-ndo",
		Image:          "quay.io/alcove/skiff:latest",
		GateImage:      "quay.io/alcove/gate:latest",
		Env:            map[string]string{"TASK_ID": "task-ndo"},
		GateEnv:        map[string]string{"GATE_SCOPE": "read"},
		Network:        "test-internal",
		ExternalNet:    "test-external",
		DirectOutbound: false,
	}

	_, err := p.RunTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	if len(*calls) < 2 {
		t.Fatalf("expected at least 2 podman calls, got %d", len(*calls))
	}

	// Skiff (second call) should be on internal network ONLY.
	skiffArgs := strings.Join((*calls)[1], " ")
	if !strings.Contains(skiffArgs, "--network test-internal") {
		t.Errorf("skiff call should include internal network: %s", skiffArgs)
	}
	if strings.Contains(skiffArgs, "test-external") {
		t.Errorf("skiff call must NOT include external network when DirectOutbound=false: %s", skiffArgs)
	}
	// HTTP_PROXY and HTTPS_PROXY must be set.
	if !strings.Contains(skiffArgs, "HTTP_PROXY=http://gate-task-ndo:8443") {
		t.Errorf("skiff call missing HTTP_PROXY when DirectOutbound=false: %s", skiffArgs)
	}
	if !strings.Contains(skiffArgs, "HTTPS_PROXY=http://gate-task-ndo:8443") {
		t.Errorf("skiff call missing HTTPS_PROXY when DirectOutbound=false: %s", skiffArgs)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}
