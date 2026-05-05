package main

import (
	"encoding/json"
	"fmt"
	"log"
)

type EventTrigger struct {
	GitHub *GitHubTrigger \`json:\"github,omitempty\" yaml:\"github\"\`
	GitLab *GitLabTrigger \`json:\"gitlab,omitempty\" yaml:\"gitlab\"\`
}

type GitLabTrigger struct {
	Events   []string \`json:\"events\" yaml:\"events\"\`
	Actions  []string \`json:\"actions,omitempty\" yaml:\"actions\"\`
	Projects []string \`json:\"projects,omitempty\" yaml:\"projects\"\`
	Labels   []string \`json:\"labels,omitempty\" yaml:\"labels\"\`
}

type GitHubTrigger struct {
	Events []string \`json:\"events\" yaml:\"events\"\`
}

func main() {
	// Test JSON marshaling/unmarshaling
	trigger := &EventTrigger{
		GitLab: &GitLabTrigger{
			Events: []string{\"merge_request\", \"issue\"},
			Actions: []string{\"opened\", \"labeled\"},
			Projects: []string{\"group/project\"},
			Labels: []string{\"ready-for-dev\"},
		},
	}

	data, err := json.Marshal(trigger)
	if err != nil {
		log.Fatalf(\"Failed to marshal trigger: %v\", err)
	}

	var unmarshaled EventTrigger
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		log.Fatalf(\"Failed to unmarshal trigger: %v\", err)
	}

	if unmarshaled.GitLab == nil {
		log.Fatalf(\"GitLab trigger not unmarshaled correctly\")
	}

	fmt.Println(\"✅ GitLab trigger JSON serialization works\")
	fmt.Printf(\"Marshaled: %s\\n\", string(data))
}
