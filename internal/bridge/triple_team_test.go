package bridge

import (
	"strings"
	"testing"
)

func TestTripleTeamPromptContent(t *testing.T) {
	if !strings.HasPrefix(TripleTeamPrompt, "## Triple Team Mode") {
		t.Error("expected prompt to start with triple team header")
	}

	for _, phase := range []string{"PHASE 1", "PHASE 2", "PHASE 3"} {
		if !strings.Contains(TripleTeamPrompt, phase) {
			t.Errorf("expected %s in triple team prompt", phase)
		}
	}

	if !strings.HasSuffix(TripleTeamPrompt, "---\n\n") {
		t.Error("expected prompt to end with separator")
	}
}

func TestTripleTeamPromptPrepend(t *testing.T) {
	original := "Fix the bug in auth.go"
	wrapped := TripleTeamPrompt + original

	if !strings.HasPrefix(wrapped, "## Triple Team Mode") {
		t.Error("expected wrapped prompt to start with triple team header")
	}

	if !strings.HasSuffix(wrapped, original) {
		t.Error("expected original prompt at end")
	}

	if !strings.Contains(wrapped, "---\n\n"+original) {
		t.Error("expected separator before original prompt")
	}
}
