package openshift_test

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestTemplateValidYAML verifies the OpenShift template is valid YAML and
// contains the expected deployment hardening fields (preStop hook,
// terminationGracePeriodSeconds).
func TestTemplateValidYAML(t *testing.T) {
	data, err := os.ReadFile("template.yaml")
	if err != nil {
		t.Fatalf("failed to read template.yaml: %v", err)
	}

	var tmpl map[string]interface{}
	if err := yaml.Unmarshal(data, &tmpl); err != nil {
		t.Fatalf("template.yaml is not valid YAML: %v", err)
	}

	// Verify top-level structure
	if tmpl["kind"] != "Template" {
		t.Errorf("expected kind=Template, got %v", tmpl["kind"])
	}

	objects, ok := tmpl["objects"].([]interface{})
	if !ok {
		t.Fatal("expected objects to be a list")
	}

	// Find the Bridge Deployment
	var bridgeDep map[string]interface{}
	for _, obj := range objects {
		o, ok := obj.(map[string]interface{})
		if !ok {
			continue
		}
		if o["kind"] != "Deployment" {
			continue
		}
		meta, ok := o["metadata"].(map[string]interface{})
		if !ok {
			continue
		}
		if meta["name"] == "alcove-bridge" {
			bridgeDep = o
			break
		}
	}
	if bridgeDep == nil {
		t.Fatal("alcove-bridge Deployment not found in template")
	}

	spec := bridgeDep["spec"].(map[string]interface{})
	podTemplate := spec["template"].(map[string]interface{})
	podSpec := podTemplate["spec"].(map[string]interface{})

	// Check terminationGracePeriodSeconds at pod spec level
	grace, ok := podSpec["terminationGracePeriodSeconds"]
	if !ok {
		t.Error("terminationGracePeriodSeconds not found in pod spec")
	} else if grace != 60 {
		t.Errorf("expected terminationGracePeriodSeconds=60, got %v", grace)
	}

	// Find the bridge container
	containers := podSpec["containers"].([]interface{})
	var bridgeContainer map[string]interface{}
	for _, c := range containers {
		container := c.(map[string]interface{})
		if container["name"] == "bridge" {
			bridgeContainer = container
			break
		}
	}
	if bridgeContainer == nil {
		t.Fatal("bridge container not found in pod spec")
	}

	// Check lifecycle.preStop hook
	lifecycle, ok := bridgeContainer["lifecycle"]
	if !ok {
		t.Error("lifecycle not found in bridge container spec")
	} else {
		lc := lifecycle.(map[string]interface{})
		preStop, ok := lc["preStop"]
		if !ok {
			t.Error("preStop not found in lifecycle")
		} else {
			ps := preStop.(map[string]interface{})
			exec, ok := ps["exec"]
			if !ok {
				t.Error("exec not found in preStop")
			} else {
				e := exec.(map[string]interface{})
				cmd, ok := e["command"]
				if !ok {
					t.Error("command not found in preStop exec")
				} else {
					cmdList := cmd.([]interface{})
					if len(cmdList) != 3 || cmdList[0] != "/bin/sh" || cmdList[1] != "-c" || cmdList[2] != "sleep 5" {
						t.Errorf("unexpected preStop command: %v", cmdList)
					}
				}
			}
		}
	}
}
