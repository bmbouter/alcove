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
	"strings"
	"testing"
)

func TestDockerRunTask_CreatesContainers(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "container-id-123\n", 0)
	d := &DockerRuntime{
		DockerBin:   "docker",
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

	handle, err := d.RunTask(context.Background(), spec)
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
		t.Fatalf("expected at least 2 docker calls, got %d", len(*calls))
	}

	// First call: gate sidecar — should be on BOTH internal and external networks.
	gateCall := (*calls)[0]
	gateArgs := strings.Join(gateCall, " ")
	if !strings.Contains(gateArgs, "--name gate-task-1") {
		t.Errorf("gate call missing --name gate-task-1: %s", gateArgs)
	}
	if !strings.Contains(gateArgs, "--network test-internal --network test-external") {
		t.Errorf("gate call missing dual network (--network test-internal --network test-external): %s", gateArgs)
	}
	if !strings.Contains(gateArgs, "quay.io/alcove/gate:latest") {
		t.Errorf("gate call missing gate image: %s", gateArgs)
	}
	if !strings.Contains(gateArgs, "GATE_SCOPE=read") {
		t.Errorf("gate call missing GATE_SCOPE env: %s", gateArgs)
	}
	// Verify --internal flag is NOT present.
	if strings.Contains(gateArgs, "--internal") {
		t.Errorf("gate call must NOT include --internal flag: %s", gateArgs)
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
	// Verify --internal flag is NOT present.
	if strings.Contains(skiffArgs, "--internal") {
		t.Errorf("skiff call must NOT include --internal flag: %s", skiffArgs)
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
	// Verify host.docker.internal is in NO_PROXY (not host.containers.internal).
	if !strings.Contains(skiffArgs, "host.docker.internal") {
		t.Errorf("skiff call missing host.docker.internal in NO_PROXY: %s", skiffArgs)
	}
	if strings.Contains(skiffArgs, "host.containers.internal") {
		t.Errorf("skiff call must NOT contain host.containers.internal: %s", skiffArgs)
	}
}

func TestDockerEnsureNetworks_NoInternalFlag(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "net-id\n", 0)
	d := &DockerRuntime{
		DockerBin:   "docker",
		execCommand: execFn,
	}

	err := d.EnsureNetworks(context.Background(), "my-internal", "my-external")
	if err != nil {
		t.Fatalf("EnsureNetworks() error: %v", err)
	}

	if len(*calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(*calls))
	}

	// First call: create internal network WITHOUT --internal flag.
	internalCall := strings.Join((*calls)[0], " ")
	if !strings.Contains(internalCall, "network create my-internal") {
		t.Errorf("expected internal network create, got: %s", internalCall)
	}
	if strings.Contains(internalCall, "--internal") {
		t.Errorf("internal network must NOT have --internal flag (Docker doesn't support it): %s", internalCall)
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

func TestDockerCancelTask_StopsContainers(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "", 0)
	d := &DockerRuntime{
		DockerBin:   "docker",
		execCommand: execFn,
	}

	handle := TaskHandle{ID: "task-1", PodName: "skiff-task-1"}
	err := d.CancelTask(context.Background(), handle)
	if err != nil {
		t.Fatalf("CancelTask() error: %v", err)
	}

	// Should have 4 calls: stop skiff, rm skiff, stop gate, rm gate.
	if len(*calls) != 4 {
		t.Fatalf("expected 4 docker calls, got %d", len(*calls))
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

func TestDockerTaskStatus_Running(t *testing.T) {
	inspectJSON, err := json.Marshal([]dockerInspect{
		{State: dockerContainerState{Status: "running", Running: true}},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	execFn, _ := fakeExecCommand(t, string(inspectJSON), 0)
	d := &DockerRuntime{
		DockerBin:   "docker",
		execCommand: execFn,
	}

	status, err := d.TaskStatus(context.Background(), TaskHandle{ID: "task-1"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want %q", status, "running")
	}
}

func TestDockerInfo_ReturnsDockerType(t *testing.T) {
	versionJSON := `{"Client":{"Version":"24.0.7"}}`
	execFn, _ := fakeExecCommand(t, versionJSON, 0)
	d := &DockerRuntime{
		DockerBin:   "docker",
		execCommand: execFn,
	}

	info, err := d.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error: %v", err)
	}
	if info.Type != "docker" {
		t.Errorf("info.Type = %q, want %q", info.Type, "docker")
	}
	if info.Version != "24.0.7" {
		t.Errorf("info.Version = %q, want %q", info.Version, "24.0.7")
	}
}

func TestDockerIsContainerRunning_ParsesLineFormat(t *testing.T) {
	// Docker ps --format json returns one JSON object per line (not an array).
	psOutput := `{"Names":"hail","State":"running"}
{"Names":"ledger","State":"running"}`

	execFn, _ := fakeExecCommand(t, psOutput, 0)
	d := &DockerRuntime{
		DockerBin:   "docker",
		execCommand: execFn,
	}

	running, err := d.isContainerRunning(context.Background(), "hail")
	if err != nil {
		t.Fatalf("isContainerRunning() error: %v", err)
	}
	if !running {
		t.Errorf("isContainerRunning() = false, want true")
	}
}

func TestDockerRunTask_DirectOutbound(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "container-id-123\n", 0)
	d := &DockerRuntime{
		DockerBin:   "docker",
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

	_, err := d.RunTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	if len(*calls) < 2 {
		t.Fatalf("expected at least 2 docker calls, got %d", len(*calls))
	}

	// Skiff (second call) should be on BOTH internal and external networks (separate --network flags for Docker).
	skiffArgs := strings.Join((*calls)[1], " ")
	if !strings.Contains(skiffArgs, "--network test-internal --network test-external") {
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

func TestDockerRunTask_NoDirectOutbound(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "container-id-123\n", 0)
	d := &DockerRuntime{
		DockerBin:   "docker",
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

	_, err := d.RunTask(context.Background(), spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	if len(*calls) < 2 {
		t.Fatalf("expected at least 2 docker calls, got %d", len(*calls))
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

func TestDockerIsContainerRunning_EmptyOutput(t *testing.T) {
	execFn, _ := fakeExecCommand(t, "", 0)
	d := &DockerRuntime{
		DockerBin:   "docker",
		execCommand: execFn,
	}

	running, err := d.isContainerRunning(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("isContainerRunning() error: %v", err)
	}
	if running {
		t.Errorf("isContainerRunning() = true, want false")
	}
}

func TestDockerCleanupOrphanedContainers_RemovesOrphans(t *testing.T) {
	// Docker ps returns line-delimited JSON (not an array).
	// gate-abc has no running skiff → should be removed.
	// gate-xyz has a running skiff → should NOT be removed.
	psOutput := `{"Names":"gate-abc","State":"running"}
{"Names":"gate-xyz","State":"running"}`

	inspectRunning, err := json.Marshal([]dockerInspect{
		{State: dockerContainerState{Status: "running", Running: true}},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	responses := []fakeResponse{
		{stdout: psOutput, exitCode: 0},                  // ps -a --filter name=^gate-
		{stdout: "", exitCode: 1},                        // inspect skiff-abc → not found
		{stdout: "", exitCode: 0},                        // stop gate-abc
		{stdout: "", exitCode: 0},                        // rm -f gate-abc
		{stdout: string(inspectRunning), exitCode: 0},    // inspect skiff-xyz → running
	}

	execFn, calls := fakeExecCommandMulti(t, responses)
	d := &DockerRuntime{
		DockerBin:   "docker",
		execCommand: execFn,
	}

	cleaned, cleanErr := d.CleanupOrphanedContainers(context.Background(), "gate-")
	if cleanErr != nil {
		t.Fatalf("CleanupOrphanedContainers() error: %v", cleanErr)
	}
	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1", cleaned)
	}

	// Verify the ps call was made with the correct filter.
	psCall := strings.Join((*calls)[0], " ")
	if !strings.Contains(psCall, "ps -a --filter name=^gate-") {
		t.Errorf("expected ps call with gate- filter, got: %s", psCall)
	}

	// Verify stop/rm were called for gate-abc.
	stopCall := strings.Join((*calls)[2], " ")
	if !strings.Contains(stopCall, "stop") || !strings.Contains(stopCall, "gate-abc") {
		t.Errorf("expected stop gate-abc, got: %s", stopCall)
	}
	rmCall := strings.Join((*calls)[3], " ")
	if !strings.Contains(rmCall, "rm -f gate-abc") {
		t.Errorf("expected rm -f gate-abc, got: %s", rmCall)
	}

	// Verify inspect was called for skiff-xyz (the running one).
	inspectXyz := strings.Join((*calls)[4], " ")
	if !strings.Contains(inspectXyz, "inspect") || !strings.Contains(inspectXyz, "skiff-xyz") {
		t.Errorf("expected inspect skiff-xyz, got: %s", inspectXyz)
	}
}

func TestDockerCleanupOrphanedContainers_SkipsRunningSkiff(t *testing.T) {
	psOutput := `{"Names":"gate-task1","State":"running"}`
	inspectRunning, err := json.Marshal([]dockerInspect{
		{State: dockerContainerState{Status: "running", Running: true}},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	responses := []fakeResponse{
		{stdout: psOutput, exitCode: 0},                  // ps
		{stdout: string(inspectRunning), exitCode: 0},    // inspect skiff-task1 → running
	}

	execFn, calls := fakeExecCommandMulti(t, responses)
	d := &DockerRuntime{
		DockerBin:   "docker",
		execCommand: execFn,
	}

	cleaned, cleanErr := d.CleanupOrphanedContainers(context.Background(), "gate-")
	if cleanErr != nil {
		t.Fatalf("CleanupOrphanedContainers() error: %v", cleanErr)
	}
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0 (skiff is still running)", cleaned)
	}

	// Should only have 2 calls: ps + inspect. No stop/rm.
	if len(*calls) != 2 {
		t.Errorf("expected 2 calls (ps + inspect), got %d", len(*calls))
	}
}

func TestDockerCleanupOrphanedContainers_EmptyList(t *testing.T) {
	execFn, calls := fakeExecCommand(t, "", 0)
	d := &DockerRuntime{
		DockerBin:   "docker",
		execCommand: execFn,
	}

	cleaned, err := d.CleanupOrphanedContainers(context.Background(), "gate-")
	if err != nil {
		t.Fatalf("CleanupOrphanedContainers() error: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0", cleaned)
	}

	// Only the ps call should be made.
	if len(*calls) != 1 {
		t.Errorf("expected 1 call (ps only), got %d", len(*calls))
	}
}
