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

// DockerRuntime implements the Runtime interface using the docker CLI.
type DockerRuntime struct {
	// DockerBin is the path to the docker binary. Defaults to "docker".
	DockerBin string

	// execCommand is a hook for testing. If nil, exec.CommandContext is used.
	execCommand func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// NewDockerRuntime creates a new DockerRuntime.
func NewDockerRuntime() *DockerRuntime {
	return &DockerRuntime{
		DockerBin: "docker",
	}
}

// EnsureNetworks creates the internal and external docker networks if they
// do not already exist. Docker does not support --internal networks, so both
// networks are created as normal bridge networks. This means Skiff containers
// have unrestricted network access unless additional firewall rules are applied.
func (d *DockerRuntime) EnsureNetworks(ctx context.Context, internal, external string) error {
	if internal == "" {
		internal = DefaultInternalNetwork
	}
	if external == "" {
		external = DefaultExternalNetwork
	}

	log.Printf("WARNING: Docker does not support --internal networks. Skiff containers have unrestricted network access. Use Podman or Kubernetes for full network isolation.")

	// Create internal network (no --internal flag available in Docker).
	if _, err := d.run(ctx, "network", "create", internal); err != nil {
		// Ignore "already exists" errors.
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("creating internal network %s: %w", internal, err)
		}
	}

	// Create external network (normal bridge with internet access).
	if _, err := d.run(ctx, "network", "create", external); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("creating external network %s: %w", external, err)
		}
	}

	return nil
}

// cmd creates an *exec.Cmd, using the test hook if set.
func (d *DockerRuntime) cmd(ctx context.Context, args ...string) *exec.Cmd {
	if d.execCommand != nil {
		return d.execCommand(ctx, d.dockerBin(), args...)
	}
	return exec.CommandContext(ctx, d.dockerBin(), args...)
}

func (d *DockerRuntime) dockerBin() string {
	if d.DockerBin != "" {
		return d.DockerBin
	}
	return "docker"
}

// run executes a docker command and returns its combined output.
func (d *DockerRuntime) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := d.cmd(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// RunTask starts a skiff container and its gate sidecar using dual-network
// isolation. Gate is attached to both the internal and external networks so
// it can proxy traffic to external services. Skiff is attached ONLY to the
// internal network so it cannot reach the internet directly.
func (d *DockerRuntime) RunTask(ctx context.Context, spec TaskSpec) (TaskHandle, error) {
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
		"--network", internalNet, "--network", externalNet,
	)
	for k, v := range spec.GateEnv {
		gateArgs = append(gateArgs, "--env", k+"="+v)
	}
	gateArgs = append(gateArgs, spec.GateImage)

	if _, err := d.run(ctx, gateArgs...); err != nil {
		return TaskHandle{}, fmt.Errorf("starting gate sidecar: %w", err)
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
		skiffArgs = append(skiffArgs, "--network", internalNet, "--network", externalNet)
	} else {
		skiffArgs = append(skiffArgs, "--network", internalNet)
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
		if _, ok := skiffEnv["NO_PROXY"]; !ok {
			skiffEnv["NO_PROXY"] = fmt.Sprintf("localhost,127.0.0.1,alcove-hail,alcove-bridge,alcove-ledger,host.docker.internal,%s", gateName)
		}
	}

	for k, v := range skiffEnv {
		skiffArgs = append(skiffArgs, "--env", k+"="+v)
	}
	skiffArgs = append(skiffArgs, spec.Image)

	if _, err := d.run(ctx, skiffArgs...); err != nil {
		// Clean up the gate container if skiff fails to start.
		_ = d.stopAndRemove(ctx, gateName)
		return TaskHandle{}, fmt.Errorf("starting skiff container: %w", err)
	}

	return TaskHandle{
		ID:      spec.TaskID,
		PodName: skiffName,
	}, nil
}

// CancelTask stops and removes both the skiff and gate containers for a task.
func (d *DockerRuntime) CancelTask(ctx context.Context, handle TaskHandle) error {
	skiffName := SkiffContainerName(handle.ID)
	gateName := GateContainerName(handle.ID)

	var firstErr error
	if err := d.stopAndRemove(ctx, skiffName); err != nil {
		firstErr = err
	}
	if err := d.stopAndRemove(ctx, gateName); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// stopAndRemove stops a container with a 10s timeout then force-removes it.
func (d *DockerRuntime) stopAndRemove(ctx context.Context, name string) error {
	// Stop with 10 second timeout.
	if _, err := d.run(ctx, "stop", "--time", "10", name); err != nil {
		log.Printf("warning: failed to stop container %s: %v", name, err)
	}
	// Force remove in case stop didn't clean it up (--rm may have handled it).
	if _, err := d.run(ctx, "rm", "-f", name); err != nil {
		log.Printf("warning: failed to remove container %s: %v", name, err)
	}
	return nil
}

// dockerContainerState represents the State block from docker inspect output.
type dockerContainerState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	ExitCode   int    `json:"ExitCode"`
	OciVersion string `json:"OciVersion"`
}

// dockerInspect represents the subset of docker inspect JSON we care about.
type dockerInspect struct {
	State dockerContainerState `json:"State"`
}

// TaskStatus returns the current status of a Skiff task by inspecting its container.
// Returns one of: "running", "exited", "created", "paused", "unknown", or "not_found".
func (d *DockerRuntime) TaskStatus(ctx context.Context, handle TaskHandle) (string, error) {
	skiffName := SkiffContainerName(handle.ID)
	out, err := d.run(ctx, "inspect", "--format", "json", skiffName)
	if err != nil {
		// If inspect fails, the container likely doesn't exist.
		return "not_found", nil
	}

	var containers []dockerInspect
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

// dockerPsEntry represents one entry from docker ps --format json output.
// Docker ps uses "Names" as a string (not an array like podman).
type dockerPsEntry struct {
	Names string `json:"Names"`
	State string `json:"State"`
}

// EnsureService starts a long-lived service container if it is not already running.
func (d *DockerRuntime) EnsureService(ctx context.Context, spec ServiceSpec) error {
	// Check if already running.
	if running, _ := d.isContainerRunning(ctx, spec.Name); running {
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

	if _, err := d.run(ctx, args...); err != nil {
		return fmt.Errorf("starting service %s: %w", spec.Name, err)
	}
	return nil
}

// isContainerRunning checks if a container with the given name exists and is running.
// Docker ps --format json returns one JSON object per line (not an array).
func (d *DockerRuntime) isContainerRunning(ctx context.Context, name string) (bool, error) {
	out, err := d.run(ctx, "ps", "--filter", "name=^"+name+"$", "--format", "json")
	if err != nil {
		return false, err
	}

	// Docker ps --format json returns one JSON object per line (not an array).
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return false, nil
	}
	var entries []dockerPsEntry
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry dockerPsEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return len(entries) > 0, nil
}

// StopService stops and removes a long-lived service container.
func (d *DockerRuntime) StopService(ctx context.Context, name string) error {
	return d.stopAndRemove(ctx, name)
}

// CreateVolume creates a named docker volume.
func (d *DockerRuntime) CreateVolume(ctx context.Context, name string) (string, error) {
	out, err := d.run(ctx, "volume", "create", name)
	if err != nil {
		return "", fmt.Errorf("creating volume %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// dockerVersionInfo is the structure returned by docker version --format json.
type dockerVersionInfo struct {
	Client struct {
		Version string `json:"Version"`
	} `json:"Client"`
}

// Info returns runtime metadata including the docker version.
func (d *DockerRuntime) Info(ctx context.Context) (RuntimeInfo, error) {
	out, err := d.run(ctx, "version", "--format", "json")
	if err != nil {
		return RuntimeInfo{Type: "docker"}, fmt.Errorf("getting docker version: %w", err)
	}

	var info dockerVersionInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return RuntimeInfo{Type: "docker"}, fmt.Errorf("parsing docker version: %w", err)
	}

	return RuntimeInfo{
		Type:    "docker",
		Version: info.Client.Version,
	}, nil
}
