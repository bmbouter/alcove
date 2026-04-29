# Alcove Terminology Glossary

This document defines the canonical terminology used throughout Alcove's codebase, documentation, CLI, and APIs. Use these terms consistently to avoid confusion when discussing features, debugging issues, or reading code.

## Core Concepts

### Workflow
A reusable pipeline template defined in YAML (`.alcove/workflows/*.yml`). Contains a name, trigger configuration, and a DAG of steps. Synced from git repos by the agent-repo-syncer.

- **Lifetime**: Persistent — lives as long as the source repo contains the YAML
- **Storage**: `workflows` database table
- **Example**: A code review workflow with steps for analysis, testing, and PR creation

### Workflow Step (definition)
A node in a workflow's DAG. Defines what to run (agent name or bridge action), dependencies (`depends`), conditions, inputs/outputs, and iteration limits. Two types: `agent` (dispatches a Skiff pod) and `bridge` (executes inline in Bridge, e.g., create-pr, await-ci, merge).

- **Lifetime**: Part of the workflow definition — not independently stored
- **Storage**: Embedded in workflow YAML
- **Example**: A step that runs the "code-reviewer" agent on changed files

### Workflow Run
A single execution of a workflow, triggered by an event (GitHub label, Jira label, cron) or manually. Tracks overall status, trigger reference, and accumulated step outputs.

- **Lifetime**: Created at trigger time, terminal when all steps complete/fail/skip
- **Storage**: `workflow_runs` database table
- **Example**: One execution of the code review workflow triggered by a "needs-review" label

### Workflow Run Step
A single execution of a step within a run. For agent steps, links to a Session via `session_id`. For bridge steps, executes inline with no session. Tracks status, outputs, and iteration count.

- **Lifetime**: Created when the step is dispatched, terminal when the step completes
- **Storage**: `workflow_run_steps` database table
- **Example**: One execution of the "code-reviewer" step within a specific workflow run

### Task
The unit of work passed to a Skiff pod: prompt, repos, scope, timeout, budget, model. Ephemeral — exists only as environment variables inside the container. Not stored in the database as a separate entity.

- **Lifetime**: Exists from dispatch to pod termination
- **Storage**: Not persisted (env vars in pod)
- **Example**: Instructions to "review this PR for security issues" with repo context

### Session
The execution record for a Task. Created when Bridge dispatches a pod. Stores status, transcript, proxy log, artifacts, exit code, and runtime config. A session may be standalone (ad-hoc `alcove run`) or part of a workflow run step.

- **Lifetime**: Created at dispatch, updated throughout execution, immutable after completion
- **Storage**: `sessions` database table
- **Example**: The complete record of an agent analyzing code, including all interactions and outputs

## Hierarchical Relationships

```
Workflow (YAML definition)
  └── Workflow Run (one execution)
        └── Workflow Run Step (one step execution)
              └── Session (if agent step) ──── contains ──── Transcript
              └── (inline execution, if bridge step)
```

## Key Distinctions

### Task vs Session
- **Task**: The ephemeral input sent to a Skiff pod (what to do)
- **Session**: The persistent execution record (what happened)
- **Relationship**: They share an ID (`task_id` on Session = `id` on Task)

### Workflow Step vs Workflow Run Step
- **Workflow Step**: The static definition in YAML (what should run)
- **Workflow Run Step**: One execution of that definition (what actually ran)
- **Relationship**: A Workflow Step can have many Workflow Run Steps across different runs

### Standalone vs Workflow Sessions
- **Standalone Session**: Created by `alcove run "do X"` — no workflow context
- **Workflow Session**: Created by a Workflow Run Step — has workflow/step/run context

## Usage Guidelines

### In Code Comments
- Use "workflow run step" not "step instance" or "step execution"
- Use "session" for execution records, "task" for input data
- Use "workflow" for the YAML definition, "workflow run" for one execution

### In CLI Output
- Show sessions with clear context: `session-123 (workflow: code-review, run: 456, step: analyzer)`
- Use consistent terminology in help text and error messages

### In API Documentation
- Endpoint paths should use these terms: `/workflows`, `/workflow-runs`, `/sessions`
- Response field names should match: `workflow_id`, `session_id`, `task_id`

### In Error Messages
- "Session abc123 failed" not "Task abc123 failed" (sessions fail, tasks are input)
- "Workflow run step xyz789 exceeded max iterations" not "Step xyz789 failed"

## Edge Cases

### Re-execution
Workflow Run Steps can re-execute (bounded by `max_iterations`), creating new sessions each iteration. Each iteration gets a fresh Task and Session.

### Bridge Steps
Bridge steps execute inline in Bridge without creating a Session. They still create a Workflow Run Step record for tracking.

### Session Artifacts
Sessions store all outputs: transcript, proxy logs, artifacts, exit codes. Tasks contain only the input specification.

## Evolution Guidelines

This glossary is a living document. When adding new concepts:

1. Define clear boundaries and relationships to existing terms
2. Update this glossary before writing code or documentation
3. Use the established naming patterns (e.g., compound terms with consistent separators)
4. Consider the CLI user experience — terms should be intuitive in `alcove list` output

For questions about terminology usage, refer to this document first, then discuss additions or changes in GitHub issues.
