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
	"fmt"
	"log"
	"net"
	"net/url"
	"os"

	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// KubernetesRuntime implements the Runtime interface using the Kubernetes API
// with client-go. Each task runs as a Job with Gate as a native sidecar
// (init container with restartPolicy: Always) and Skiff as the main container.
type KubernetesRuntime struct {
	clientset kubernetes.Interface
	namespace string
}

// jobNamePrefix is the prefix for all Alcove task Job and NetworkPolicy names.
const jobNamePrefix = "alcove-task-"

// managedByLabel marks all resources created by Alcove for easy identification.
const managedByLabel = "alcove"

// NewKubernetesRuntime creates a new KubernetesRuntime. It attempts in-cluster
// configuration first (for when Bridge runs inside the cluster), then falls
// back to kubeconfig for local development.
func NewKubernetesRuntime() (*KubernetesRuntime, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig for local development.
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, _ := os.UserHomeDir()
			kubeconfig = home + "/.kube/config"
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("building kubernetes config: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes clientset: %w", err)
	}

	namespace := detectNamespace()

	return &KubernetesRuntime{
		clientset: clientset,
		namespace: namespace,
	}, nil
}

// detectNamespace determines the Kubernetes namespace to operate in.
// Priority: ALCOVE_NAMESPACE env var > in-cluster service account > default "alcove".
func detectNamespace() string {
	if ns := os.Getenv("ALCOVE_NAMESPACE"); ns != "" {
		return ns
	}
	// When running in-cluster, the namespace is mounted by the kubelet.
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := string(data); ns != "" {
			return ns
		}
	}
	return "alcove"
}

// jobName returns the Kubernetes Job name for a given task ID.
// Kubernetes names must be DNS-compatible (max 63 chars, lowercase alphanumeric and hyphens).
func jobName(taskID string) string {
	name := jobNamePrefix + taskID
	if len(name) > 63 {
		name = name[:63]
	}
	name = strings.TrimRight(name, "-")
	return name
}

// taskLabels returns the standard labels applied to all Alcove task resources.
func taskLabels(taskID string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": managedByLabel,
		"app.kubernetes.io/part-of":    "alcove",
		"alcove.dev/task-id":           taskID,
	}
}

// RunTask creates a Kubernetes Job with Gate as a native sidecar and Skiff as
// the main container. It also creates a NetworkPolicy to restrict egress.
func (k *KubernetesRuntime) RunTask(ctx context.Context, spec TaskSpec) (TaskHandle, error) {
	name := jobName(spec.TaskID)
	labels := taskLabels(spec.TaskID)

	// Mark pods that should bypass the proxy and get unrestricted egress.
	if spec.DirectOutbound {
		labels["alcove.dev/direct-outbound"] = "true"
	}

	if spec.Debug {
		log.Printf("debug mode: job %s will not have ttlSecondsAfterFinished set", name)
	}

	// Build environment variables for Skiff (main container).
	// Merge spec env with proxy configuration pointing to Gate on localhost
	// (Gate and Skiff share the pod's network namespace).
	skiffEnv := make(map[string]string)
	for k, v := range spec.Env {
		skiffEnv[k] = v
	}
	// Ensure HOME is set — OpenShift assigns random UIDs that don't exist in
	// /etc/passwd, so HOME defaults to "/" which is not writable.
	if _, ok := skiffEnv["HOME"]; !ok {
		skiffEnv["HOME"] = "/home/skiff"
	}
	// Point HTTP(S)_PROXY to the Gate sidecar, unless DirectOutbound is enabled.
	if !spec.DirectOutbound {
		if _, ok := skiffEnv["HTTP_PROXY"]; !ok {
			skiffEnv["HTTP_PROXY"] = "http://localhost:8443"
		}
		if _, ok := skiffEnv["HTTPS_PROXY"]; !ok {
			skiffEnv["HTTPS_PROXY"] = "http://localhost:8443"
		}
		if _, ok := skiffEnv["NO_PROXY"]; !ok {
			skiffEnv["NO_PROXY"] = "localhost,127.0.0.1,alcove-hail,alcove-bridge,alcove-ledger,.svc,.svc.cluster.local"
		}
	}
	// Override Gate-referenced URLs to use localhost since Gate is a sidecar
	// sharing the pod network namespace (not a separate container with its own hostname).
	skiffEnv["ANTHROPIC_BASE_URL"] = "http://localhost:8443"
	for _, key := range []string{
		"GITHUB_API_URL", "GITLAB_API_URL", "JIRA_API_URL",
	} {
		if val, ok := skiffEnv[key]; ok {
			// Replace gate-{taskid}:8443 with localhost:8443
			if strings.Contains(val, ":8443/") {
				parts := strings.SplitN(val, ":8443/", 2)
				skiffEnv[key] = "http://localhost:8443/" + parts[1]
			}
		}
	}
	if _, ok := skiffEnv["GH_HOST"]; ok {
		skiffEnv["GH_HOST"] = "localhost:8443"
	}
	if _, ok := skiffEnv["GLAB_HOST"]; ok {
		skiffEnv["GLAB_HOST"] = "http://localhost:8443/gitlab"
	}

	// Resolve service hostnames to IPs to bypass DNS issues in task pods.
	// OVN-Kubernetes may block UDP DNS from pods with restrictive egress policies.
	if hailURL, ok := skiffEnv["HAIL_URL"]; ok {
		resolved := resolveServiceURL(hailURL)
		if resolved != hailURL {
			log.Printf("resolved HAIL_URL: %s → %s", hailURL, resolved)
			skiffEnv["HAIL_URL"] = resolved
		}
	}
	if ledgerURL, ok := skiffEnv["LEDGER_URL"]; ok {
		resolved := resolveServiceURL(ledgerURL)
		if resolved != ledgerURL {
			log.Printf("resolved LEDGER_URL: %s → %s", ledgerURL, resolved)
			skiffEnv["LEDGER_URL"] = resolved
		}
	}

	// Add resolved IPs to NO_PROXY so direct-IP connections bypass the proxy.
	if noProxy, ok := skiffEnv["NO_PROXY"]; ok {
		var extraHosts []string
		if hailURL, ok := skiffEnv["HAIL_URL"]; ok {
			if u, err := url.Parse(hailURL); err == nil {
				if h := u.Hostname(); h != "" {
					extraHosts = append(extraHosts, h)
				}
			}
		}
		if ledgerURL, ok := skiffEnv["LEDGER_URL"]; ok {
			if u, err := url.Parse(ledgerURL); err == nil {
				if h := u.Hostname(); h != "" {
					extraHosts = append(extraHosts, h)
				}
			}
		}
		for _, h := range extraHosts {
			if !strings.Contains(noProxy, h) {
				noProxy += "," + h
			}
		}
		skiffEnv["NO_PROXY"] = noProxy
	}

	// Also resolve Gate env vars that reference internal services.
	gateEnv := spec.GateEnv
	if gateEnv == nil {
		gateEnv = make(map[string]string)
	}
	for _, key := range []string{"GATE_LEDGER_URL", "GATE_TOKEN_REFRESH_URL"} {
		if val, ok := gateEnv[key]; ok {
			resolved := resolveServiceURL(val)
			if resolved != val {
				log.Printf("resolved %s: %s → %s", key, val, resolved)
				gateEnv[key] = resolved
			}
		}
	}
	spec.GateEnv = gateEnv

	// Build final env var slices AFTER all resolution is done.
	gateEnvVars := envMapToVars(spec.GateEnv)
	skiffEnvVars := envMapToVars(skiffEnv)

	// Security context: run as non-root with minimal capabilities.
	securityContext := &corev1.SecurityContext{
		RunAsNonRoot:             boolPtr(true),
		AllowPrivilegeEscalation: boolPtr(false),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}

	// Gate runs as a native sidecar: an init container with restartPolicy Always.
	// This ensures Gate starts before Skiff and stays running for the pod's lifetime.
	sidecarRestart := corev1.ContainerRestartPolicyAlways
	gateContainer := corev1.Container{
		Name:            "gate",
		Image:           spec.GateImage,
		Env:             gateEnvVars,
		RestartPolicy:   &sidecarRestart,
		SecurityContext: securityContext,
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: 8443,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
	}

	// Skiff is the main container that runs the actual task.
	skiffContainer := corev1.Container{
		Name:            "skiff",
		Image:           spec.Image,
		Env:             skiffEnvVars,
		SecurityContext: securityContext,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
		},
	}

	// Build the Job spec.
	backoffLimit := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: k.namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{gateContainer},
					Containers:     []corev1.Container{skiffContainer},
					RestartPolicy:  corev1.RestartPolicyNever,
				},
			},
		},
	}

	// Set activeDeadlineSeconds from the task timeout.
	if spec.Timeout > 0 {
		job.Spec.ActiveDeadlineSeconds = &spec.Timeout
	}

	// Set TTL cleanup: 5 minutes after completion, unless in debug mode.
	if !spec.Debug {
		ttl := int32(300)
		job.Spec.TTLSecondsAfterFinished = &ttl
	}

	// NOTE: Per-task NetworkPolicy creation is disabled for now.
	// The static alcove-allow-internal policy provides sufficient restriction.
	// Per-task policies caused DNS resolution failures on OVN-Kubernetes.
	// TODO: Re-enable once the root cause of DNS blocking is identified.

	// Create the Job.
	created, err := k.clientset.BatchV1().Jobs(k.namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return TaskHandle{}, fmt.Errorf("creating job %s: %w", name, err)
	}

	return TaskHandle{
		ID:      spec.TaskID,
		PodName: created.Name,
	}, nil
}

// createNetworkPolicy creates a NetworkPolicy that restricts the task pod's
// egress to only DNS (UDP 53), HTTPS (TCP 443) for LLM/SCM APIs, and
// internal Alcove services identified by label.
func (k *KubernetesRuntime) createNetworkPolicy(ctx context.Context, taskID string, podLabels map[string]string) error {
	name := jobName(taskID)

	dnsPort := intstr.FromInt32(53)
	httpsPort := intstr.FromInt32(443)
	udp := corev1.ProtocolUDP
	tcp := corev1.ProtocolTCP

	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: k.namespace,
			Labels:    taskLabels(taskID),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				// Allow DNS resolution (UDP and TCP 53 to any).
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: &udp,
							Port:     &dnsPort,
						},
						{
							Protocol: &tcp,
							Port:     &dnsPort,
						},
					},
				},
				// Allow HTTPS egress (TCP 443 to any) for LLM and SCM APIs.
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: &tcp,
							Port:     &httpsPort,
						},
					},
				},
				// Allow egress to Alcove infrastructure (Bridge, NATS)
				// and other task pods.
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app.kubernetes.io/part-of": "alcove",
								},
							},
						},
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app.kubernetes.io/managed-by": managedByLabel,
								},
							},
						},
					},
				},
			},
		},
	}

	_, err := k.clientset.NetworkingV1().NetworkPolicies(k.namespace).Create(ctx, np, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating network policy %s: %w", name, err)
	}
	return nil
}

// deleteNetworkPolicy removes the NetworkPolicy for a task.
// Returns the raw API error (not wrapped) so callers can use apierrors.IsNotFound().
func (k *KubernetesRuntime) deleteNetworkPolicy(ctx context.Context, taskID string) error {
	name := jobName(taskID)
	return k.clientset.NetworkingV1().NetworkPolicies(k.namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// CancelTask deletes the Job (cascading to its pod) and the associated
// NetworkPolicy. Errors are logged as warnings but do not cause failure,
// since the resources may already be cleaned up.
func (k *KubernetesRuntime) CancelTask(ctx context.Context, handle TaskHandle) error {
	name := jobName(handle.ID)
	propagation := metav1.DeletePropagationBackground

	// Delete the Job with background propagation to cascade to pods.
	if err := k.clientset.BatchV1().Jobs(k.namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	}); err != nil && !apierrors.IsNotFound(err) {
		log.Printf("warning: failed to delete job %s: %v", name, err)
	}

	// Delete the associated NetworkPolicy.
	if err := k.deleteNetworkPolicy(ctx, handle.ID); err != nil && !apierrors.IsNotFound(err) {
		log.Printf("warning: failed to delete network policy for task %s: %v", handle.ID, err)
	}

	return nil
}

// TaskStatus returns the current status of a Skiff task by inspecting its Job.
// Returns one of: "running", "exited", or "not_found".
func (k *KubernetesRuntime) TaskStatus(ctx context.Context, handle TaskHandle) (string, error) {
	name := jobName(handle.ID)
	job, err := k.clientset.BatchV1().Jobs(k.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "not_found", nil
		}
		return "", fmt.Errorf("getting job %s: %w", name, err)
	}

	// Check Job conditions for completion or failure.
	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		if cond.Type == batchv1.JobComplete || cond.Type == batchv1.JobFailed {
			return "exited", nil
		}
	}

	// If the job has active pods, it's running.
	if job.Status.Active > 0 {
		return "running", nil
	}

	// Job exists but no active pods and no terminal condition yet.
	// This can happen briefly during pod scheduling.
	return "running", nil
}

// EnsureService is a no-op on Kubernetes. Infrastructure services (Hail, Ledger)
// are managed by Helm charts or Kubernetes manifests, not by Bridge directly.
func (k *KubernetesRuntime) EnsureService(ctx context.Context, spec ServiceSpec) error {
	log.Printf("kubernetes runtime: EnsureService is a no-op; service %q should be managed by Helm/manifests", spec.Name)
	return nil
}

// StopService is a no-op on Kubernetes. Infrastructure services are managed
// externally via Helm charts or Kubernetes manifests.
func (k *KubernetesRuntime) StopService(ctx context.Context, name string) error {
	log.Printf("kubernetes runtime: StopService is a no-op; service %q should be managed by Helm/manifests", name)
	return nil
}

// CreateVolume is a no-op on Kubernetes. PersistentVolumeClaims are managed
// externally via Helm charts or Kubernetes manifests.
func (k *KubernetesRuntime) CreateVolume(ctx context.Context, name string) (string, error) {
	log.Printf("kubernetes runtime: CreateVolume is a no-op; PVC %q should be managed by Helm/manifests", name)
	return name, nil
}

// Info returns runtime metadata for the Kubernetes runtime, including the
// server version reported by the API server.
func (k *KubernetesRuntime) Info(ctx context.Context) (RuntimeInfo, error) {
	serverVersion, err := k.clientset.Discovery().ServerVersion()
	if err != nil {
		return RuntimeInfo{Type: "kubernetes"}, fmt.Errorf("getting kubernetes server version: %w", err)
	}

	return RuntimeInfo{
		Type:    "kubernetes",
		Version: serverVersion.GitVersion,
	}, nil
}

// envMapToVars converts a map of environment variable key-value pairs to a
// slice of Kubernetes EnvVar objects.
func envMapToVars(env map[string]string) []corev1.EnvVar {
	vars := make([]corev1.EnvVar, 0, len(env))
	for k, v := range env {
		vars = append(vars, corev1.EnvVar{Name: k, Value: v})
	}
	return vars
}

// resolveServiceURL resolves the hostname in a service URL to an IP address.
// For example, nats://alcove-hail:4222 becomes nats://10.x.x.x:4222.
// Returns the original URL unchanged if resolution fails.
func resolveServiceURL(serviceURL string) string {
	u, err := url.Parse(serviceURL)
	if err != nil {
		return serviceURL
	}
	host := u.Hostname()
	if host == "" {
		return serviceURL
	}
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return serviceURL
	}
	u.Host = net.JoinHostPort(addrs[0], u.Port())
	return u.String()
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool {
	return &b
}
