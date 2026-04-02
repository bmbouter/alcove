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
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// newTestKubernetesRuntime creates a KubernetesRuntime with a fake clientset
// for unit testing without a real cluster.
func newTestKubernetesRuntime() (*KubernetesRuntime, *fake.Clientset) {
	clientset := fake.NewSimpleClientset()
	rt := &KubernetesRuntime{
		clientset: clientset,
		namespace: "test-ns",
	}
	return rt, clientset
}

func TestJobName(t *testing.T) {
	tests := []struct {
		taskID string
		want   string
	}{
		{"abc123", "alcove-task-abc123"},
		{"task-1", "alcove-task-task-1"},
		{"", "alcove-task"},
	}
	for _, tt := range tests {
		if got := jobName(tt.taskID); got != tt.want {
			t.Errorf("jobName(%q) = %q, want %q", tt.taskID, got, tt.want)
		}
	}
}

func TestJobName_TruncatesLongNames(t *testing.T) {
	longID := "this-is-a-very-long-task-id-that-exceeds-the-kubernetes-dns-name-limit-of-63-chars"
	name := jobName(longID)
	if len(name) > 63 {
		t.Errorf("jobName produced name of length %d, want <= 63", len(name))
	}
}

func TestTaskLabels(t *testing.T) {
	labels := taskLabels("task-42")
	if labels["app.kubernetes.io/managed-by"] != "alcove" {
		t.Errorf("managed-by label = %q, want %q", labels["app.kubernetes.io/managed-by"], "alcove")
	}
	if labels["alcove.dev/task-id"] != "task-42" {
		t.Errorf("task-id label = %q, want %q", labels["alcove.dev/task-id"], "task-42")
	}
}

func TestRunTask_JobSpec(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-1",
		Image:     "quay.io/alcove/skiff:latest",
		GateImage: "quay.io/alcove/gate:latest",
		Env:       map[string]string{"TASK_ID": "task-1", "REPO_URL": "https://example.com/repo.git"},
		GateEnv:   map[string]string{"GATE_SCOPE": "read"},
		Timeout:   3600,
	}

	handle, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}
	if handle.ID != "task-1" {
		t.Errorf("handle.ID = %q, want %q", handle.ID, "task-1")
	}
	if handle.PodName != "alcove-task-task-1" {
		t.Errorf("handle.PodName = %q, want %q", handle.PodName, "alcove-task-task-1")
	}

	// Fetch the created Job from the fake clientset.
	jobs, err := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs.Items))
	}

	job := jobs.Items[0]

	// Verify Job name.
	if job.Name != "alcove-task-task-1" {
		t.Errorf("job.Name = %q, want %q", job.Name, "alcove-task-task-1")
	}

	// Verify labels on the Job.
	if job.Labels["app.kubernetes.io/managed-by"] != "alcove" {
		t.Errorf("job managed-by label = %q, want %q", job.Labels["app.kubernetes.io/managed-by"], "alcove")
	}
	if job.Labels["alcove.dev/task-id"] != "task-1" {
		t.Errorf("job task-id label = %q, want %q", job.Labels["alcove.dev/task-id"], "task-1")
	}

	// Verify labels on the pod template.
	podLabels := job.Spec.Template.Labels
	if podLabels["app.kubernetes.io/managed-by"] != "alcove" {
		t.Errorf("pod managed-by label = %q, want %q", podLabels["app.kubernetes.io/managed-by"], "alcove")
	}
	if podLabels["alcove.dev/task-id"] != "task-1" {
		t.Errorf("pod task-id label = %q, want %q", podLabels["alcove.dev/task-id"], "task-1")
	}

	// Verify activeDeadlineSeconds matches timeout.
	if job.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("activeDeadlineSeconds is nil, want 3600")
	}
	if *job.Spec.ActiveDeadlineSeconds != 3600 {
		t.Errorf("activeDeadlineSeconds = %d, want %d", *job.Spec.ActiveDeadlineSeconds, 3600)
	}

	// Verify ttlSecondsAfterFinished is set (not debug mode).
	if job.Spec.TTLSecondsAfterFinished == nil {
		t.Fatal("ttlSecondsAfterFinished is nil, expected it to be set")
	}
	if *job.Spec.TTLSecondsAfterFinished != 300 {
		t.Errorf("ttlSecondsAfterFinished = %d, want %d", *job.Spec.TTLSecondsAfterFinished, 300)
	}

	// Verify backoff limit is 0 (no retries).
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Errorf("backoffLimit = %v, want 0", job.Spec.BackoffLimit)
	}

	// Verify pod restart policy.
	if job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q, want %q", job.Spec.Template.Spec.RestartPolicy, corev1.RestartPolicyNever)
	}
}

func TestRunTask_GateInitContainer(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-gate",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		GateEnv:   map[string]string{"GATE_SCOPE": "read", "GATE_TOKEN": "abc"},
	}

	_, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	job := jobs.Items[0]

	// Gate should be an init container.
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}

	gate := job.Spec.Template.Spec.InitContainers[0]

	// Verify name and image.
	if gate.Name != "gate" {
		t.Errorf("gate container name = %q, want %q", gate.Name, "gate")
	}
	if gate.Image != "gate:latest" {
		t.Errorf("gate image = %q, want %q", gate.Image, "gate:latest")
	}

	// Verify restartPolicy is Always (native sidecar pattern).
	if gate.RestartPolicy == nil {
		t.Fatal("gate restartPolicy is nil, want Always")
	}
	if *gate.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Errorf("gate restartPolicy = %q, want %q", *gate.RestartPolicy, corev1.ContainerRestartPolicyAlways)
	}

	// Verify gate env vars.
	gateEnvMap := envVarsToMap(gate.Env)
	if gateEnvMap["GATE_SCOPE"] != "read" {
		t.Errorf("gate GATE_SCOPE = %q, want %q", gateEnvMap["GATE_SCOPE"], "read")
	}
	if gateEnvMap["GATE_TOKEN"] != "abc" {
		t.Errorf("gate GATE_TOKEN = %q, want %q", gateEnvMap["GATE_TOKEN"], "abc")
	}

	// Verify gate exposes port 8443.
	if len(gate.Ports) != 1 || gate.Ports[0].ContainerPort != 8443 {
		t.Errorf("gate ports = %v, want port 8443", gate.Ports)
	}

	// Verify security context.
	assertSecurityContext(t, gate.SecurityContext, "gate")
}

func TestRunTask_SkiffMainContainer(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-skiff",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		Env:       map[string]string{"TASK_ID": "task-skiff", "CUSTOM_VAR": "value"},
	}

	_, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	job := jobs.Items[0]

	// Skiff should be the main container.
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(job.Spec.Template.Spec.Containers))
	}

	skiff := job.Spec.Template.Spec.Containers[0]

	if skiff.Name != "skiff" {
		t.Errorf("skiff container name = %q, want %q", skiff.Name, "skiff")
	}
	if skiff.Image != "skiff:latest" {
		t.Errorf("skiff image = %q, want %q", skiff.Image, "skiff:latest")
	}

	// Verify env vars including proxy configuration.
	skiffEnvMap := envVarsToMap(skiff.Env)
	if skiffEnvMap["TASK_ID"] != "task-skiff" {
		t.Errorf("TASK_ID = %q, want %q", skiffEnvMap["TASK_ID"], "task-skiff")
	}
	if skiffEnvMap["CUSTOM_VAR"] != "value" {
		t.Errorf("CUSTOM_VAR = %q, want %q", skiffEnvMap["CUSTOM_VAR"], "value")
	}

	// Proxy env vars should point to localhost:8443 (Gate is a sidecar).
	if skiffEnvMap["HTTP_PROXY"] != "http://localhost:8443" {
		t.Errorf("HTTP_PROXY = %q, want %q", skiffEnvMap["HTTP_PROXY"], "http://localhost:8443")
	}
	if skiffEnvMap["HTTPS_PROXY"] != "http://localhost:8443" {
		t.Errorf("HTTPS_PROXY = %q, want %q", skiffEnvMap["HTTPS_PROXY"], "http://localhost:8443")
	}
	if skiffEnvMap["NO_PROXY"] != "localhost,127.0.0.1,alcove-hail,alcove-bridge,alcove-ledger,.svc,.svc.cluster.local" {
		t.Errorf("NO_PROXY = %q, want %q", skiffEnvMap["NO_PROXY"], "localhost,127.0.0.1,alcove-hail,alcove-bridge,alcove-ledger,.svc,.svc.cluster.local")
	}
	if skiffEnvMap["ANTHROPIC_BASE_URL"] != "http://localhost:8443" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", skiffEnvMap["ANTHROPIC_BASE_URL"], "http://localhost:8443")
	}

	// Verify security context.
	assertSecurityContext(t, skiff.SecurityContext, "skiff")
}

func TestK8sRunTask_ProxyEnvNotOverridden(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-proxy",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		Env: map[string]string{
			"HTTP_PROXY":  "http://custom-proxy:9999",
			"HTTPS_PROXY": "http://custom-proxy:9999",
		},
	}

	_, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	skiff := jobs.Items[0].Spec.Template.Spec.Containers[0]
	skiffEnvMap := envVarsToMap(skiff.Env)

	// Custom proxy values should not be overridden.
	if skiffEnvMap["HTTP_PROXY"] != "http://custom-proxy:9999" {
		t.Errorf("HTTP_PROXY was overridden: got %q, want %q", skiffEnvMap["HTTP_PROXY"], "http://custom-proxy:9999")
	}
	if skiffEnvMap["HTTPS_PROXY"] != "http://custom-proxy:9999" {
		t.Errorf("HTTPS_PROXY was overridden: got %q, want %q", skiffEnvMap["HTTPS_PROXY"], "http://custom-proxy:9999")
	}
}

func TestRunTask_DebugMode(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-debug",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		Debug:     true,
	}

	_, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	job := jobs.Items[0]

	// In debug mode, ttlSecondsAfterFinished should NOT be set.
	if job.Spec.TTLSecondsAfterFinished != nil {
		t.Errorf("ttlSecondsAfterFinished should be nil in debug mode, got %d", *job.Spec.TTLSecondsAfterFinished)
	}
}

func TestRunTask_NoTimeout(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-no-timeout",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		Timeout:   0,
	}

	_, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	job := jobs.Items[0]

	// With zero timeout, activeDeadlineSeconds should not be set.
	if job.Spec.ActiveDeadlineSeconds != nil {
		t.Errorf("activeDeadlineSeconds should be nil with zero timeout, got %d", *job.Spec.ActiveDeadlineSeconds)
	}
}

func DISABLED_TestRunTask_CreatesNetworkPolicy(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-np",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
	}

	_, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	// Verify NetworkPolicy was created.
	nps, err := clientset.NetworkingV1().NetworkPolicies("test-ns").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing network policies: %v", err)
	}
	if len(nps.Items) != 1 {
		t.Fatalf("expected 1 network policy, got %d", len(nps.Items))
	}

	np := nps.Items[0]

	// Verify NetworkPolicy name matches Job name.
	if np.Name != "alcove-task-task-np" {
		t.Errorf("network policy name = %q, want %q", np.Name, "alcove-task-task-np")
	}

	// Verify labels.
	if np.Labels["alcove.dev/task-id"] != "task-np" {
		t.Errorf("network policy task-id label = %q, want %q", np.Labels["alcove.dev/task-id"], "task-np")
	}

	// Verify policy type is Egress only.
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != "Egress" {
		t.Errorf("policy types = %v, want [Egress]", np.Spec.PolicyTypes)
	}

	// Verify egress rules: DNS (UDP 53), HTTPS (TCP 443), and internal Alcove services.
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("expected 3 egress rules, got %d", len(np.Spec.Egress))
	}

	// Rule 0: DNS (UDP and TCP 53).
	dnsRule := np.Spec.Egress[0]
	if len(dnsRule.Ports) != 2 {
		t.Fatalf("DNS rule: expected 2 ports, got %d", len(dnsRule.Ports))
	}
	if *dnsRule.Ports[0].Protocol != corev1.ProtocolUDP {
		t.Errorf("DNS rule port 0 protocol = %q, want UDP", *dnsRule.Ports[0].Protocol)
	}
	if dnsRule.Ports[0].Port.IntValue() != 53 {
		t.Errorf("DNS rule port 0 = %d, want 53", dnsRule.Ports[0].Port.IntValue())
	}
	if *dnsRule.Ports[1].Protocol != corev1.ProtocolTCP {
		t.Errorf("DNS rule port 1 protocol = %q, want TCP", *dnsRule.Ports[1].Protocol)
	}
	if dnsRule.Ports[1].Port.IntValue() != 53 {
		t.Errorf("DNS rule port 1 = %d, want 53", dnsRule.Ports[1].Port.IntValue())
	}

	// Rule 1: HTTPS (TCP 443).
	httpsRule := np.Spec.Egress[1]
	if len(httpsRule.Ports) != 1 {
		t.Fatalf("HTTPS rule: expected 1 port, got %d", len(httpsRule.Ports))
	}
	if *httpsRule.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Errorf("HTTPS rule protocol = %q, want TCP", *httpsRule.Ports[0].Protocol)
	}
	if httpsRule.Ports[0].Port.IntValue() != 443 {
		t.Errorf("HTTPS rule port = %d, want 443", httpsRule.Ports[0].Port.IntValue())
	}

	// Rule 2: Internal Alcove services by label.
	internalRule := np.Spec.Egress[2]
	if len(internalRule.To) != 2 {
		t.Fatalf("internal rule: expected 2 peers (part-of + managed-by), got %d", len(internalRule.To))
	}
	if internalRule.To[0].PodSelector == nil {
		t.Fatal("internal rule: first podSelector is nil")
	}
	if internalRule.To[0].PodSelector.MatchLabels["app.kubernetes.io/part-of"] != "alcove" {
		t.Errorf("internal rule part-of = %q, want %q",
			internalRule.To[0].PodSelector.MatchLabels["app.kubernetes.io/part-of"], "alcove")
	}
	if internalRule.To[1].PodSelector.MatchLabels["app.kubernetes.io/managed-by"] != "alcove" {
		t.Errorf("internal rule managed-by = %q, want %q",
			internalRule.To[1].PodSelector.MatchLabels["app.kubernetes.io/managed-by"], "alcove")
	}

	// Verify the pod selector targets the task's labels.
	if np.Spec.PodSelector.MatchLabels["alcove.dev/task-id"] != "task-np" {
		t.Errorf("pod selector task-id = %q, want %q",
			np.Spec.PodSelector.MatchLabels["alcove.dev/task-id"], "task-np")
	}
}

func DISABLED_TestCancelTask_DeletesJobAndNetworkPolicy(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	// First create a task so there's something to cancel.
	spec := TaskSpec{
		TaskID:    "task-cancel",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
	}

	handle, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	// Verify resources exist before cancel.
	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 job before cancel, got %d", len(jobs.Items))
	}
	nps, _ := clientset.NetworkingV1().NetworkPolicies("test-ns").List(ctx, metav1.ListOptions{})
	if len(nps.Items) != 1 {
		t.Fatalf("expected 1 network policy before cancel, got %d", len(nps.Items))
	}

	// Cancel the task.
	err = rt.CancelTask(ctx, handle)
	if err != nil {
		t.Fatalf("CancelTask() error: %v", err)
	}

	// Verify Job was deleted.
	jobs, _ = clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Errorf("expected 0 jobs after cancel, got %d", len(jobs.Items))
	}

	// Verify NetworkPolicy was deleted.
	nps, _ = clientset.NetworkingV1().NetworkPolicies("test-ns").List(ctx, metav1.ListOptions{})
	if len(nps.Items) != 0 {
		t.Errorf("expected 0 network policies after cancel, got %d", len(nps.Items))
	}
}

func TestCancelTask_NonexistentTask(t *testing.T) {
	rt, _ := newTestKubernetesRuntime()
	ctx := context.Background()

	// Cancelling a nonexistent task should not return an error
	// (errors are logged as warnings).
	handle := TaskHandle{ID: "nonexistent", PodName: "alcove-task-nonexistent"}
	err := rt.CancelTask(ctx, handle)
	if err != nil {
		t.Errorf("CancelTask() for nonexistent task returned error: %v", err)
	}
}

func TestK8sTaskStatus_Running(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	// Create a Job with active pods (simulating a running task).
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alcove-task-running-1",
			Namespace: "test-ns",
		},
		Status: batchv1.JobStatus{
			Active: 1,
		},
	}
	_, err := clientset.BatchV1().Jobs("test-ns").Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating test job: %v", err)
	}

	status, err := rt.TaskStatus(ctx, TaskHandle{ID: "running-1"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want %q", status, "running")
	}
}

func TestTaskStatus_CompletedJob(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alcove-task-done-1",
			Namespace: "test-ns",
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
	_, err := clientset.BatchV1().Jobs("test-ns").Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating test job: %v", err)
	}

	status, err := rt.TaskStatus(ctx, TaskHandle{ID: "done-1"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "exited" {
		t.Errorf("status = %q, want %q", status, "exited")
	}
}

func TestTaskStatus_FailedJob(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alcove-task-failed-1",
			Namespace: "test-ns",
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobFailed,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
	_, err := clientset.BatchV1().Jobs("test-ns").Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating test job: %v", err)
	}

	status, err := rt.TaskStatus(ctx, TaskHandle{ID: "failed-1"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "exited" {
		t.Errorf("status = %q, want %q", status, "exited")
	}
}

func TestTaskStatus_ConditionNotTrue(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	// A job with a Complete condition that is not True should be treated as running.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alcove-task-pending-1",
			Namespace: "test-ns",
		},
		Status: batchv1.JobStatus{
			Active: 1,
			Conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionFalse,
				},
			},
		},
	}
	_, err := clientset.BatchV1().Jobs("test-ns").Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating test job: %v", err)
	}

	status, err := rt.TaskStatus(ctx, TaskHandle{ID: "pending-1"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want %q", status, "running")
	}
}

func TestK8sTaskStatus_NotFound(t *testing.T) {
	rt, _ := newTestKubernetesRuntime()
	ctx := context.Background()

	status, err := rt.TaskStatus(ctx, TaskHandle{ID: "nonexistent"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	if status != "not_found" {
		t.Errorf("status = %q, want %q", status, "not_found")
	}
}

func TestEnsureService_NoOp(t *testing.T) {
	rt, _ := newTestKubernetesRuntime()
	ctx := context.Background()

	err := rt.EnsureService(ctx, ServiceSpec{Name: "hail", Image: "nats:latest"})
	if err != nil {
		t.Errorf("EnsureService() should be no-op but returned error: %v", err)
	}
}

func TestStopService_NoOp(t *testing.T) {
	rt, _ := newTestKubernetesRuntime()
	ctx := context.Background()

	err := rt.StopService(ctx, "hail")
	if err != nil {
		t.Errorf("StopService() should be no-op but returned error: %v", err)
	}
}

func TestCreateVolume_NoOp(t *testing.T) {
	rt, _ := newTestKubernetesRuntime()
	ctx := context.Background()

	name, err := rt.CreateVolume(ctx, "test-vol")
	if err != nil {
		t.Errorf("CreateVolume() should be no-op but returned error: %v", err)
	}
	if name != "test-vol" {
		t.Errorf("CreateVolume() = %q, want %q", name, "test-vol")
	}
}

func TestEnvMapToVars(t *testing.T) {
	env := map[string]string{"A": "1", "B": "2"}
	vars := envMapToVars(env)
	if len(vars) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(vars))
	}
	m := envVarsToMap(vars)
	if m["A"] != "1" || m["B"] != "2" {
		t.Errorf("envMapToVars roundtrip failed: got %v", m)
	}
}

func TestEnvMapToVars_Empty(t *testing.T) {
	vars := envMapToVars(nil)
	if len(vars) != 0 {
		t.Errorf("expected 0 env vars for nil map, got %d", len(vars))
	}
}

func TestRunTask_NilEnvMaps(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-nil-env",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		Env:       nil,
		GateEnv:   nil,
	}

	_, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	job := jobs.Items[0]

	// Gate should have no env vars when GateEnv is nil.
	gate := job.Spec.Template.Spec.InitContainers[0]
	if len(gate.Env) != 0 {
		t.Errorf("gate env vars = %d, want 0 for nil GateEnv", len(gate.Env))
	}

	// Skiff should still have proxy env vars even when Env is nil.
	skiff := job.Spec.Template.Spec.Containers[0]
	skiffEnvMap := envVarsToMap(skiff.Env)
	if skiffEnvMap["HTTP_PROXY"] != "http://localhost:8443" {
		t.Errorf("HTTP_PROXY = %q, want default proxy when Env is nil", skiffEnvMap["HTTP_PROXY"])
	}
	if skiffEnvMap["HTTPS_PROXY"] != "http://localhost:8443" {
		t.Errorf("HTTPS_PROXY = %q, want default proxy when Env is nil", skiffEnvMap["HTTPS_PROXY"])
	}
	if skiffEnvMap["NO_PROXY"] != "localhost,127.0.0.1,alcove-hail,alcove-bridge,alcove-ledger,.svc,.svc.cluster.local" {
		t.Errorf("NO_PROXY = %q, want default NO_PROXY when Env is nil", skiffEnvMap["NO_PROXY"])
	}
	if skiffEnvMap["ANTHROPIC_BASE_URL"] != "http://localhost:8443" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want default when Env is nil", skiffEnvMap["ANTHROPIC_BASE_URL"])
	}
}

func TestK8sRunTask_ProxyEnvNO_PROXYNotOverridden(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-noproxy",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		Env: map[string]string{
			"NO_PROXY": "my-service.local,10.0.0.0/8",
		},
	}

	_, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	skiff := jobs.Items[0].Spec.Template.Spec.Containers[0]
	skiffEnvMap := envVarsToMap(skiff.Env)

	if skiffEnvMap["NO_PROXY"] != "my-service.local,10.0.0.0/8" {
		t.Errorf("NO_PROXY was overridden: got %q, want %q", skiffEnvMap["NO_PROXY"], "my-service.local,10.0.0.0/8")
	}
}

func TestRunTask_AnthropicBaseURLAlwaysOverridden(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-anthropic",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		Env: map[string]string{
			"ANTHROPIC_BASE_URL": "https://custom-llm-endpoint.example.com",
		},
	}

	_, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	skiff := jobs.Items[0].Spec.Template.Spec.Containers[0]
	skiffEnvMap := envVarsToMap(skiff.Env)

	// ANTHROPIC_BASE_URL must always be forced to localhost:8443 (Gate sidecar),
	// regardless of what the user provides. This is a security requirement:
	// LLM keys never enter Skiff.
	if skiffEnvMap["ANTHROPIC_BASE_URL"] != "http://localhost:8443" {
		t.Errorf("ANTHROPIC_BASE_URL should be forced to Gate sidecar, got %q", skiffEnvMap["ANTHROPIC_BASE_URL"])
	}
}

func TestTaskStatus_NoConditionsNoActivePods(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	// A job with no conditions and no active pods -- happens briefly during
	// pod scheduling or image pull.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alcove-task-scheduling-1",
			Namespace: "test-ns",
		},
		Status: batchv1.JobStatus{
			Active: 0,
			// No conditions set.
		},
	}
	_, err := clientset.BatchV1().Jobs("test-ns").Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating test job: %v", err)
	}

	status, err := rt.TaskStatus(ctx, TaskHandle{ID: "scheduling-1"})
	if err != nil {
		t.Fatalf("TaskStatus() error: %v", err)
	}
	// Should be "running" since the job exists but hasn't finished.
	if status != "running" {
		t.Errorf("status = %q, want %q", status, "running")
	}
}

func TestK8sInfo(t *testing.T) {
	rt, _ := newTestKubernetesRuntime()
	ctx := context.Background()

	info, err := rt.Info(ctx)
	if err != nil {
		t.Fatalf("Info() error: %v", err)
	}
	if info.Type != "kubernetes" {
		t.Errorf("info.Type = %q, want %q", info.Type, "kubernetes")
	}
	// The fake clientset returns a version; just verify it's non-empty.
	// (fake.NewSimpleClientset returns "0.0" as GitVersion by default.)
	if info.Version == "" {
		t.Error("info.Version is empty, expected a value from fake discovery")
	}
}

func TestRunTask_DebugModeStillSetsActiveDeadline(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	spec := TaskSpec{
		TaskID:    "task-debug-timeout",
		Image:     "skiff:latest",
		GateImage: "gate:latest",
		Debug:     true,
		Timeout:   7200,
	}

	_, err := rt.RunTask(ctx, spec)
	if err != nil {
		t.Fatalf("RunTask() error: %v", err)
	}

	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	job := jobs.Items[0]

	// Debug mode disables TTL but should not affect activeDeadlineSeconds.
	if job.Spec.TTLSecondsAfterFinished != nil {
		t.Errorf("ttlSecondsAfterFinished should be nil in debug mode, got %d", *job.Spec.TTLSecondsAfterFinished)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 7200 {
		t.Errorf("activeDeadlineSeconds = %v, want 7200 (debug mode should not affect timeout)", job.Spec.ActiveDeadlineSeconds)
	}
}

func TestRunTask_MultipleTasksCreateSeparateResources(t *testing.T) {
	rt, clientset := newTestKubernetesRuntime()
	ctx := context.Background()

	for _, taskID := range []string{"task-a", "task-b"} {
		spec := TaskSpec{
			TaskID:    taskID,
			Image:     "skiff:latest",
			GateImage: "gate:latest",
		}
		_, err := rt.RunTask(ctx, spec)
		if err != nil {
			t.Fatalf("RunTask(%s) error: %v", taskID, err)
		}
	}

	// Verify two separate Jobs were created.
	jobs, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 2 {
		t.Errorf("expected 2 jobs, got %d", len(jobs.Items))
	}

	// Per-task NetworkPolicies are currently disabled.
	nps, _ := clientset.NetworkingV1().NetworkPolicies("test-ns").List(ctx, metav1.ListOptions{})
	if len(nps.Items) != 0 {
		t.Errorf("expected 0 network policies (disabled), got %d", len(nps.Items))
	}
}

func TestJobName_ExactlyAt63Chars(t *testing.T) {
	// The prefix "alcove-task-" is 12 chars, so a 51-char ID produces exactly 63.
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 51 chars
	name := jobName(id)
	if len(name) != 63 {
		t.Errorf("jobName produced name of length %d, want exactly 63", len(name))
	}
	if name != jobNamePrefix+id {
		t.Errorf("jobName = %q, want %q", name, jobNamePrefix+id)
	}
}

func TestJobName_OneOverLimit(t *testing.T) {
	// 52-char ID + 12-char prefix = 64, should be truncated to 63.
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 52 chars
	name := jobName(id)
	if len(name) != 63 {
		t.Errorf("jobName produced name of length %d, want 63", len(name))
	}
}

// envVarsToMap converts a slice of Kubernetes EnvVar to a map for easy assertion.
func envVarsToMap(vars []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		m[v.Name] = v.Value
	}
	return m
}

// assertSecurityContext verifies the standard security context settings
// applied to Alcove containers (non-root, drop all capabilities, no privilege escalation).
func assertSecurityContext(t *testing.T, sc *corev1.SecurityContext, containerName string) {
	t.Helper()
	if sc == nil {
		t.Fatalf("%s: security context is nil", containerName)
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Errorf("%s: runAsNonRoot should be true", containerName)
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("%s: allowPrivilegeEscalation should be false", containerName)
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("%s: capabilities.drop should be [ALL], got %v", containerName, sc.Capabilities)
	}
}
