# Workflow YAML Schema

This document describes the workflow YAML schema used in Alcove for orchestrating multi-step workflows.

## Overview

Workflow files define sequences of agent executions with dependencies, conditions, and data flow between steps. They sit alongside agent definitions in agent repositories and are parsed by the Bridge component.

## Schema Structure

### WorkflowDefinition

The top-level workflow definition:

```yaml
name: string              # Required: Human-readable workflow name
workflow: []WorkflowStep  # Required: List of workflow steps
```

### WorkflowStep

Each step in the workflow:

```yaml
id: string                    # Required: Unique step identifier
agent: string                 # Required: Agent name to execute
repo: string                  # Optional: Repository for the step
trigger:                      # Optional: Trigger configuration
  github:                     # GitHub webhook trigger
    events: [string]          # List of GitHub events (issues, pull_request, etc.)
    actions: [string]         # List of actions (opened, synchronize, etc.)
    labels: [string]          # Required labels for trigger
needs: [string]               # Optional: List of step IDs this step depends on
condition: string             # Optional: Runtime condition expression
approval: string              # Optional: "required" for manual approval
outputs: [string]             # Optional: List of output names this step produces
inputs: map[string]interface{} # Optional: Input values (supports templates)
```

## Validation Rules

The parser validates the following:

1. **Unique Step IDs**: All step IDs must be unique within the workflow
2. **Required Fields**: Each step must have `id` and `agent` fields
3. **Valid Dependencies**: All steps referenced in `needs` must exist
4. **No Circular Dependencies**: The dependency graph must be acyclic
5. **Valid Approval Values**: Approval field can only be empty or "required"

## Template Expressions

Steps can reference outputs from previous steps using template expressions:

```yaml
inputs:
  context: "{{steps.previous_step.outputs.summary}}"
  result: "{{steps.another_step.outputs.result}}"
```

Conditions can also use template expressions:

```yaml
condition: "steps.previous_step.outcome == 'completed'"
```

## Example Workflow

See `docs/examples/feature-delivery-workflow.yaml` for a complete example of a feature delivery pipeline.

## DAG (Directed Acyclic Graph)

Workflows form a DAG through the `needs` relationships:
- Steps with no `needs` are root steps (entry points)
- Steps with `needs` wait for their dependencies to complete
- Circular dependencies are detected and rejected during parsing

## Usage

```go
import "github.com/bmbouter/alcove/internal/bridge"

// Parse workflow from YAML bytes
data, err := ioutil.ReadFile("workflow.yaml")
if err != nil {
    log.Fatal(err)
}

workflow, err := bridge.ParseWorkflowDefinition(data)
if err != nil {
    log.Fatal(err)
}

// Access workflow properties
fmt.Printf("Workflow: %s\n", workflow.Name)
fmt.Printf("Steps: %d\n", len(workflow.Workflow))

// Get dependency graph
dag := workflow.GetWorkflowDAG()

// Get root steps (no dependencies)
roots := workflow.GetRootSteps()

// Check for template expressions
hasTemplates := workflow.HasTemplateExpressions()
```