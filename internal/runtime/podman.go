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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// PodmanRuntime implements the Runtime interface using the podman CLI.
type PodmanRuntime struct {
	// PodmanBin is the path to the podman binary. Defaults to "podman".
	PodmanBin string

	// ShimBin is the host path to the shim binary. When a dev container is
	// started, this binary is volume-mounted into the container and executed
	// as a background process. Defaults to "./bin/shim".
	ShimBin string

	// execCommand is a hook for testing. If nil, exec.CommandContext is used.
	execCommand func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// Default network names for the dual-network isolation pattern.
const (
	DefaultInternalNetwork = "alcove-internal"
	DefaultExternalNetwork = "alcove-external"
)

// NewPodmanRuntime creates a new PodmanRuntime with dual-network isolation.
func NewPodmanRuntime() *PodmanRuntime {
	return &PodmanRuntime{
		PodmanBin: "podman",
	}
}

// EnsureNetworks creates the internal and external podman networks if they
// do not already exist. The internal network is created with --internal so
// containers attached only to it have no route to the internet.
func (p *PodmanRuntime) EnsureNetworks(ctx context.Context, internal, external string) error {
	if internal == "" {
		internal = DefaultInternalNetwork
	}
	if external == "" {
		external = DefaultExternalNetwork
	}

	// Create internal network (no external routing).
	if _, err := p.run(ctx, "network", "create", "--internal", internal); err != nil {
		// Ignore "already exists" errors.
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("creating internal network %s: %w", internal, err)
		}
	}

	// Create external network (normal bridge with internet access).
	if _, err := p.run(ctx, "network", "create", external); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("creating external network %s: %w", external, err)
		}
	}

	return nil
}

// cmd creates an *exec.Cmd, using the test hook if set.
func (p *PodmanRuntime) cmd(ctx context.Context, args ...string) *exec.Cmd {
	if p.execCommand != nil {
		return p.execCommand(ctx, p.podmanBin(), args...)
	}
	return exec.CommandContext(ctx, p.podmanBin(), args...)
}

func (p *PodmanRuntime) podmanBin() string {
	if p.PodmanBin != "" {
		return p.PodmanBin
	}
	return "podman"
}

func (p *PodmanRuntime) shimBin() string {
	if p.ShimBin != "" {
		return p.ShimBin
	}
	return "./bin/shim"
}

// run executes a podman command and returns its combined output.
func (p *PodmanRuntime) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := p.cmd(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("podman %s: %w: %s", redactEnvArgs(args), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// redactEnvArgs returns a string representation of podman args with --env
// values redacted to prevent leaking secrets (SHIM_TOKEN, etc.) in error messages.
func redactEnvArgs(args []string) string {
	redacted := make([]string, len(args))
	for i, arg := range args {
		if i > 0 && args[i-1] == "--env" && strings.Contains(arg, "=") {
			parts := strings.SplitN(arg, "=", 2)
			redacted[i] = parts[0] + "=REDACTED"
		} else {
			redacted[i] = arg
		}
	}
	return strings.Join(redacted, " ")
}

// SkiffContainerName returns the container name for the main skiff container.
func SkiffContainerName(taskID string) string {
	return "skiff-" + taskID
}

// GateContainerName returns the container name for the gate sidecar container.
func GateContainerName(taskID string) string {
	return "gate-" + taskID
}

// DevContainerName returns the container name for the dev container sidecar.
func DevContainerName(taskID string) string {
	return "dev-" + taskID
}

// WorkspaceVolumeName returns the volume name for the shared workspace.
func WorkspaceVolumeName(taskID string) string {
	return "workspace-" + taskID
}

// RunTask starts a skiff container and its gate sidecar using dual-network
// isolation. Gate is attached to both the internal and external networks so
// it can proxy traffic to external services. Skiff is attached ONLY to the
// internal network so it cannot reach the internet directly.
func (p *PodmanRuntime) RunTask(ctx context.Context, spec TaskSpec) (TaskHandle, error) {
	skiffName := SkiffContainerName(spec.TaskID)
	gateName := GateContainerName(spec.TaskID)
	internalNet := spec.Network
	if internalNet == "" {
		internalNet = DefaultInternalNetwork
	}
	externalNet := spec.ExternalNet
	if externalNet == "" {
		externalNet = DefaultExternalNetwork
	}

	if spec.Debug {
		log.Printf("debug mode: containers %s and %s will NOT be auto-removed", skiffName, gateName)
	}

	// Start gate sidecar first so it's available when skiff starts.
	// Gate joins BOTH internal (to be reachable by Skiff) and external
	// (to reach LLM APIs, GitHub, etc.).
	gateArgs := []string{
		"run", "-d",
	}
	if !spec.Debug {
		gateArgs = append(gateArgs, "--rm")
	}
	gateArgs = append(gateArgs,
		"--name", gateName,
		"--network", internalNet+","+externalNet,
	)
	for k, v := range spec.GateEnv {
		gateArgs = append(gateArgs, "--env", k+"="+v)
	}
	gateArgs = append(gateArgs, spec.GateImage)

	if _, err := p.run(ctx, gateArgs...); err != nil {
		return TaskHandle{}, fmt.Errorf("starting gate sidecar: %w", err)
	}

	// Start dev container sidecar if configured.
	devName := DevContainerName(spec.TaskID)
	workspaceVol := WorkspaceVolumeName(spec.TaskID)
	if spec.DevContainerImage != "" {
		// Create a shared workspace volume for Skiff ↔ dev container.
		if _, err := p.run(ctx, "volume", "create", workspaceVol); err != nil {
			_ = p.stopAndRemove(ctx, gateName)
			return TaskHandle{}, fmt.Errorf("creating workspace volume: %w", err)
		}

		// Start the dev container. By default it is on the internal network only
		// (no external access). When DevContainerNetworkAccess is "external", it
		// joins both the internal and external networks so it can reach the internet.
		devArgs := []string{
			"run", "-d",
		}
		if !spec.Debug {
			devArgs = append(devArgs, "--rm")
		}
		devNetworkArg := internalNet
		if spec.DevContainerNetworkAccess == "external" {
			devNetworkArg = internalNet + "," + externalNet
		}
		devArgs = append(devArgs,
			"--name", devName,
			"--network", devNetworkArg,
			"--security-opt", "label=disable",
			"-v", workspaceVol+":/workspace",
			"-v", p.shimBin()+":/usr/local/bin/alcove-shim:ro,z",
			"--entrypoint", "/usr/local/bin/alcove-shim",
		)
		for k, v := range spec.DevContainerEnv {
			devArgs = append(devArgs, "--env", k+"="+v)
		}
		devArgs = append(devArgs, spec.DevContainerImage)

		if _, err := p.run(ctx, devArgs...); err != nil {
			// Clean up gate + volume on dev container failure.
			_ = p.stopAndRemove(ctx, gateName)
			_, _ = p.run(ctx, "volume", "rm", workspaceVol)
			return TaskHandle{}, fmt.Errorf("starting dev container: %w", err)
		}
	}

	// Start the main skiff container on the internal network ONLY.
	// It can reach Gate, Hail, Ledger, Bridge but NOT the internet.
	skiffArgs := []string{
		"run", "-d",
	}
	if !spec.Debug {
		skiffArgs = append(skiffArgs, "--rm")
	}
	skiffArgs = append(skiffArgs,
		"--name", skiffName,
	)

	// Attach Skiff to internal network only, unless DirectOutbound is enabled.
	if spec.DirectOutbound {
		skiffArgs = append(skiffArgs, "--network", internalNet+","+externalNet)
	} else {
		skiffArgs = append(skiffArgs, "--network", internalNet)
	}

	// Mount workspace volume in Skiff if dev container is configured.
	if spec.DevContainerImage != "" {
		skiffArgs = append(skiffArgs, "-v", workspaceVol+":/workspace")
	}

	// Merge spec env with the proxy configuration.
	skiffEnv := make(map[string]string)
	for k, v := range spec.Env {
		skiffEnv[k] = v
	}
	// Point HTTP(S)_PROXY to the gate sidecar, unless DirectOutbound is enabled.
	if !spec.DirectOutbound {
		if _, ok := skiffEnv["HTTP_PROXY"]; !ok {
			skiffEnv["HTTP_PROXY"] = fmt.Sprintf("http://%s:8443", gateName)
		}
		if _, ok := skiffEnv["HTTPS_PROXY"]; !ok {
			skiffEnv["HTTPS_PROXY"] = fmt.Sprintf("http://%s:8443", gateName)
		}
		// Exempt internal services and Gate from proxy. Gate must be reached directly
		// (not through itself) for ANTHROPIC_BASE_URL to work.
		noProxyBase := fmt.Sprintf("localhost,127.0.0.1,alcove-hail,alcove-bridge,alcove-ledger,host.containers.internal,%s", gateName)
		if spec.DevContainerImage != "" {
			noProxyBase += "," + devName
		}
		if _, ok := skiffEnv["NO_PROXY"]; !ok {
			skiffEnv["NO_PROXY"] = noProxyBase
		}
	}

	for k, v := range skiffEnv {
		skiffArgs = append(skiffArgs, "--env", k+"="+v)
	}
	skiffArgs = append(skiffArgs, spec.Image)

	if _, err := p.run(ctx, skiffArgs...); err != nil {
		// Clean up the gate container (and dev container + volume if present).
		if spec.DevContainerImage != "" {
			_ = p.stopAndRemove(ctx, devName)
			_, _ = p.run(ctx, "volume", "rm", workspaceVol)
		}
		_ = p.stopAndRemove(ctx, gateName)
		return TaskHandle{}, fmt.Errorf("starting skiff container: %w", err)
	}

	return TaskHandle{
		ID:      spec.TaskID,
		PodName: skiffName,
	}, nil
}

// CancelTask stops and removes both the skiff and gate containers for a task.
// It also cleans up any dev container and workspace volume associated with the task.
func (p *PodmanRuntime) CancelTask(ctx context.Context, handle TaskHandle) error {
	skiffName := SkiffContainerName(handle.ID)
	gateName := GateContainerName(handle.ID)
	devName := DevContainerName(handle.ID)
	workspaceVol := WorkspaceVolumeName(handle.ID)

	var firstErr error
	if err := p.stopAndRemove(ctx, skiffName); err != nil {
		firstErr = err
	}
	if err := p.stopAndRemove(ctx, gateName); err != nil && firstErr == nil {
		firstErr = err
	}
	// Always attempt dev container cleanup (no-op if not present).
	_ = p.stopAndRemove(ctx, devName)
	// Always attempt workspace volume cleanup (no-op if not present).
	_, _ = p.run(ctx, "volume", "rm", workspaceVol)
	return firstErr
}

// stopAndRemove stops a container with a 10s timeout then force-removes it.
func (p *PodmanRuntime) stopAndRemove(ctx context.Context, name string) error {
	// Stop with 10 second timeout.
	if _, err := p.run(ctx, "stop", "--time", "10", name); err != nil {
		log.Printf("warning: failed to stop container %s: %v", name, err)
	}
	// Force remove in case stop didn't clean it up (--rm may have handled it).
	if _, err := p.run(ctx, "rm", "-f", name); err != nil {
		log.Printf("warning: failed to remove container %s: %v", name, err)
	}
	return nil
}

// podmanContainerState represents the State block from podman inspect output.
type podmanContainerState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	ExitCode   int    `json:"ExitCode"`
	OciVersion string `json:"OciVersion"`
}

// podmanInspect represents the subset of podman inspect JSON we care about.
type podmanInspect struct {
	State podmanContainerState `json:"State"`
}

// TaskStatus returns the current status of a Skiff task by inspecting its container.
// Returns one of: "running", "exited", "created", "paused", "unknown", or "not_found".
func (p *PodmanRuntime) TaskStatus(ctx context.Context, handle TaskHandle) (string, error) {
	skiffName := SkiffContainerName(handle.ID)
	out, err := p.run(ctx, "inspect", "--format", "json", skiffName)
	if err != nil {
		// If inspect fails, the container likely doesn't exist.
		return "not_found", nil
	}

	var containers []podmanInspect
	if err := json.Unmarshal(out, &containers); err != nil {
		return "unknown", fmt.Errorf("parsing inspect output: %w", err)
	}
	if len(containers) == 0 {
		return "not_found", nil
	}

	status := strings.ToLower(containers[0].State.Status)
	switch status {
	case "running", "exited", "created", "paused", "stopped":
		return status, nil
	default:
		return "unknown", nil
	}
}

// podmanPsEntry represents one entry from podman ps --format json output.
type podmanPsEntry struct {
	Names []string `json:"Names"`
	State string   `json:"State"`
}

// EnsureService starts a long-lived service container if it is not already running.
func (p *PodmanRuntime) EnsureService(ctx context.Context, spec ServiceSpec) error {
	// Check if already running.
	if running, _ := p.isContainerRunning(ctx, spec.Name); running {
		return nil
	}

	args := []string{
		"run", "-d",
		"--name", spec.Name,
	}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	for k, v := range spec.Env {
		args = append(args, "--env", k+"="+v)
	}
	for containerPort, hostPort := range spec.Ports {
		args = append(args, "-p", fmt.Sprintf("%d:%d", hostPort, containerPort))
	}
	for volName, mountPath := range spec.Volumes {
		args = append(args, "-v", fmt.Sprintf("%s:%s", volName, mountPath))
	}
	args = append(args, spec.Image)

	if _, err := p.run(ctx, args...); err != nil {
		return fmt.Errorf("starting service %s: %w", spec.Name, err)
	}
	return nil
}

// isContainerRunning checks if a container with the given name exists and is running.
func (p *PodmanRuntime) isContainerRunning(ctx context.Context, name string) (bool, error) {
	out, err := p.run(ctx, "ps", "--filter", "name=^"+name+"$", "--format", "json")
	if err != nil {
		return false, err
	}

	// podman ps --format json returns "null" or "[]" when no containers match.
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return false, nil
	}

	var entries []podmanPsEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

// StopService stops and removes a long-lived service container.
func (p *PodmanRuntime) StopService(ctx context.Context, name string) error {
	return p.stopAndRemove(ctx, name)
}

// CreateVolume creates a named podman volume.
func (p *PodmanRuntime) CreateVolume(ctx context.Context, name string) (string, error) {
	out, err := p.run(ctx, "volume", "create", name)
	if err != nil {
		return "", fmt.Errorf("creating volume %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// podmanVersionInfo is the structure returned by podman version --format json.
type podmanVersionInfo struct {
	Client struct {
		Version string `json:"Version"`
	} `json:"Client"`
}

// CleanupOrphanedContainers finds containers whose names start with prefix
// (e.g., "gate-") and removes any whose corresponding skiff container is gone.
func (p *PodmanRuntime) CleanupOrphanedContainers(ctx context.Context, prefix string) (int, error) {
	// List all containers (running and stopped) matching the prefix.
	out, err := p.run(ctx, "ps", "-a", "--filter", "name=^"+prefix, "--format", "json")
	if err != nil {
		return 0, fmt.Errorf("listing containers with prefix %s: %w", prefix, err)
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return 0, nil
	}

	var entries []podmanPsEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return 0, fmt.Errorf("parsing container list: %w", err)
	}

	cleaned := 0
	for _, entry := range entries {
		if len(entry.Names) == 0 {
			continue
		}
		name := entry.Names[0]

		// Derive the corresponding skiff container name.
		// "gate-<taskID>" -> "skiff-<taskID>"
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		taskID := strings.TrimPrefix(name, prefix)
		skiffName := SkiffContainerName(taskID)

		// Check if the skiff container still exists.
		skiffHandle := TaskHandle{ID: taskID}
		status, err := p.TaskStatus(ctx, skiffHandle)
		if err == nil && status == "running" {
			// Skiff is still running — do not remove its Gate.
			continue
		}

		// Skiff is gone or not running — clean up the orphaned container.
		log.Printf("cleanup: removing orphaned container %s (skiff %s status: %s)", name, skiffName, status)
		_ = p.stopAndRemove(ctx, name)
		// If cleaning up dev containers, also remove the workspace volume.
		if prefix == "dev-" {
			workspaceVol := WorkspaceVolumeName(taskID)
			_, _ = p.run(ctx, "volume", "rm", workspaceVol)
		}
		cleaned++
	}

	return cleaned, nil
}

// Info returns runtime metadata including the podman version.
func (p *PodmanRuntime) Info(ctx context.Context) (RuntimeInfo, error) {
	out, err := p.run(ctx, "version", "--format", "json")
	if err != nil {
		return RuntimeInfo{Type: "podman"}, fmt.Errorf("getting podman version: %w", err)
	}

	var info podmanVersionInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return RuntimeInfo{Type: "podman"}, fmt.Errorf("parsing podman version: %w", err)
	}

	return RuntimeInfo{
		Type:    "podman",
		Version: info.Client.Version,
	}, nil
}
