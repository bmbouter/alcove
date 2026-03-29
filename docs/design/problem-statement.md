# Problem Statement

## Why Ephemeral Agents?

Most AI coding agent platforms assume long-running agents: a persistent process
that stays alive across multiple tasks, accumulating context and maintaining
state. This feels natural — it mirrors how humans work — but it creates
fundamental problems in practice.

Alcove takes the opposite approach: each task gets a fresh agent in a fresh
container, with a clean context window, clean filesystem, and task-specific
network authorization. This document explains why.


## Problem 1: Context Window Contamination

When a long-running agent handles multiple tasks sequentially, prior context
remains in its window. The agent cannot fully distinguish which context is
relevant to the current task and which is residue from previous work.

**What happens in practice:**

- Agent fixes a bug in authentication code (Task A). Next, it's asked to update
  documentation (Task B). The agent applies security-oriented reasoning from
  Task A to a documentation task where it doesn't apply, producing overly
  cautious or irrelevant suggestions.

- Agent refactors a database model (Task A). Next, it's asked to fix a CI
  pipeline (Task B). The agent references table names, migration patterns, and
  ORM concepts from Task A when reasoning about YAML pipeline definitions.

- Agent works on repo X (Task A), then repo Y (Task B). File paths, function
  names, and architectural patterns from repo X leak into reasoning about repo Y.

**The core issue**: LLMs do not have a reliable mechanism to "forget" or
partition prior context. Prompt engineering can mitigate this ("ignore all
previous context") but cannot guarantee it — especially under adversarial
conditions (prompt injection from Task A influencing behavior in Task B).

**Alcove's approach**: each task starts a new Skiff pod with a completely fresh
Claude Code session. There is zero context carryover between tasks. The context
window contains only what is relevant to the current task.


## Problem 2: Network Authorization Drift

Long-running agents that interact with external services (GitHub, GitLab, Jira,
CI systems) accumulate network access over time. The credentials and
authorization scopes configured at startup persist across all subsequent tasks,
even when later tasks require different — often narrower — access.

**What happens in practice:**

- An agent is given GitHub write access for Task A (opening a PR). Task B only
  needs read access (reviewing code). But the agent still holds write
  credentials, and a prompt injection in Task B could exploit them.

- An agent is authorized to access repo X for Task A. Task B is about repo Y.
  The agent still has credentials for repo X — an unnecessary attack surface.

- Authorization changes mid-session (a token is rotated, a scope is narrowed)
  require reconfiguring a running agent, which is error-prone and often skipped.

**The core issue**: in a long-running agent, the authorization envelope is the
union of everything it has ever been granted. There is no clean way to revoke
access to a running process that has already received credentials in its
environment or memory.

**Alcove's approach**: Gate provisions per-task authorization scopes. Each Skiff
pod receives only an opaque session token that Gate maps to the specific
operations authorized for that task. When the task ends, the token expires. The
next task gets a new token with its own scope. There is no authorization
accumulation.


## Problem 3: Filesystem State Accumulation

Long-running containers accumulate filesystem state: downloaded dependencies,
cached builds, modified configs, temporary files, and — critically — artifacts
from previous agent sessions. This state is invisible to the agent's context
window but influences its behavior through the tools it runs.

**What happens in practice:**

- A previous task installed a compromised dependency. The next task inherits it.

- A previous task modified `.git/config` or `.bashrc`. The next task executes
  in a subtly altered environment.

- Build caches from Task A cause Task B to produce different results than a
  clean build would.

- A prompt injection from Task A writes a malicious script to disk. Task B
  unknowingly executes it.

**The core issue**: filesystem state is a persistence mechanism for
cross-task attacks. Even without malicious intent, accumulated state makes
agent behavior non-reproducible — the same prompt can produce different results
depending on what tasks ran before it.

**Alcove's approach**: each Skiff pod starts from a known-clean container image
and is destroyed after the task completes. Kubernetes reprovisions a fresh pod
for the next task. The container image is the only input; the session transcript
is the only durable output. There is no filesystem state that survives between
tasks.


## Problem 4: Credential Exposure in Long-Lived Processes

Long-running agents need credentials for the services they interact with. These
credentials typically live in environment variables, config files, or in-memory
state for the entire lifetime of the process — across all tasks, regardless of
whether each task needs them.

**What happens in practice:**

- An agent's environment contains `GH_TOKEN`, `JIRA_API_KEY`, and
  `ANTHROPIC_API_KEY` simultaneously. A prompt injection that exfiltrates
  environment variables gets all of them.

- Credentials are logged accidentally in debug output, error messages, or
  session transcripts that are stored long-term.

- Token rotation requires restarting the agent, which disrupts in-progress work.

**The core issue**: credential lifetime equals process lifetime. There is no
mechanism to scope credentials to individual tasks within a single process.

**Alcove's approach**: real credentials never enter Skiff pods. Skiff pods hold
only an opaque session token and an LLM provider key. All external service
access goes through Gate, which performs token replacement at request time. Even
a complete compromise of a Skiff pod yields only a short-lived, scope-limited
session token and an LLM key — not GitHub PATs, GitLab tokens, or Jira
credentials.


## Summary

| Problem | Long-running agents | Alcove (ephemeral) |
|---------|--------------------|--------------------|
| Context contamination | Prior tasks pollute current reasoning | Fresh context window per task |
| Authorization drift | Credentials accumulate, scopes widen | Per-task scopes via Gate, token expires at task end |
| Filesystem poisoning | State persists across tasks | Container destroyed after each task |
| Credential exposure | All creds available for entire lifetime | Only opaque session token in Skiff pod (no LLM key) |
| Reproducibility | Same prompt, different results depending on history | Same prompt, same clean starting point every time |
| Auditability | Unclear which task caused which side effect | Each task is an isolated, fully recorded session |
