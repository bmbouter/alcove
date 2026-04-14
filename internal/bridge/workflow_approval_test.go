package bridge

import (
	"testing"
	"time"
)

func TestValidateApprovalTimeout(t *testing.T) {
	tests := []struct {
		name      string
		timeout   string
		wantError bool
	}{
		{"valid hours", "72h", false},
		{"valid minutes", "30m", false},
		{"valid seconds", "45s", false},
		{"valid combined", "1h30m", false},
		{"invalid format", "invalid", true},
		{"empty", "", true},
		{"negative", "-1h", false}, // time.ParseDuration allows negative
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateApprovalTimeout(tt.timeout)
			if (err != nil) != tt.wantError {
				t.Errorf("validateApprovalTimeout(%q) error = %v, wantError %v", 
					tt.timeout, err, tt.wantError)
			}
		})
	}
}

func TestWorkflowStepApprovalValidation(t *testing.T) {
	tests := []struct {
		name      string
		step      WorkflowStep
		wantError bool
	}{
		{
			name: "valid approval with timeout",
			step: WorkflowStep{
				ID:              "test-step",
				Agent:           "test-agent",
				Approval:        "required",
				ApprovalTimeout: "72h",
			},
			wantError: false,
		},
		{
			name: "approval without timeout (should use default)",
			step: WorkflowStep{
				ID:       "test-step",
				Agent:    "test-agent",
				Approval: "required",
			},
			wantError: false,
		},
		{
			name: "timeout without approval",
			step: WorkflowStep{
				ID:              "test-step",
				Agent:           "test-agent",
				ApprovalTimeout: "72h",
			},
			wantError: true,
		},
		{
			name: "invalid approval value",
			step: WorkflowStep{
				ID:       "test-step",
				Agent:    "test-agent",
				Approval: "invalid",
			},
			wantError: true,
		},
		{
			name: "invalid timeout format",
			step: WorkflowStep{
				ID:              "test-step",
				Agent:           "test-agent",
				Approval:        "required",
				ApprovalTimeout: "invalid",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWorkflowSteps([]WorkflowStep{tt.step})
			if (err != nil) != tt.wantError {
				t.Errorf("validateWorkflowSteps() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}
