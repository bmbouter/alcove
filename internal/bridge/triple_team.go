package bridge

const TripleTeamPrompt = `## Triple Team Mode

You MUST follow these instructions EXACTLY. Complete this task using a three-phase approach with diverse specialist agents. Each phase uses the Agent tool to spawn parallel sub-agents.

### PHASE 1: Workers (3 agents in parallel)

Spawn exactly 3 agents using the Agent tool. Send ALL 3 tool calls in a SINGLE message so they run in parallel. Each agent must have a distinct specialist perspective:

- **Agent 1 (Pragmatic Engineer)**: Focus on the simplest, most maintainable solution. Prefer established patterns and minimal changes. Prioritize code clarity and long-term maintainability.
- **Agent 2 (Thorough Architect)**: Focus on comprehensive correctness. Consider edge cases, error handling, validation, and architectural consistency. Ensure the solution handles all scenarios.
- **Agent 3 (Creative Problem Solver)**: Explore alternative approaches. Consider whether a different design pattern, algorithm, or structural approach would yield a better result. Challenge assumptions.

Each agent's prompt must include the full original task (provided below the separator) and their specialist perspective instructions. Each agent should produce a complete, working solution.

### PHASE 2: Evaluators (3 agents in parallel)

After ALL Phase 1 agents complete, spawn exactly 3 NEW agents in a SINGLE message. Each evaluator receives a summary of ALL Phase 1 solutions. Each evaluator must:

1. **Independently evaluate** each Phase 1 solution for correctness, completeness, code quality, and edge case handling
2. **Identify strengths and weaknesses** of each approach
3. **Develop their own solution** that may draw on the best ideas from Phase 1 or take an entirely new approach

Evaluator perspectives:
- **Evaluator 1 (Code Reviewer)**: Focus on code quality, test coverage, error handling, and adherence to project conventions
- **Evaluator 2 (Adversarial Tester)**: Actively try to find bugs, edge cases, race conditions, and failure modes in the Phase 1 solutions
- **Evaluator 3 (Integration Specialist)**: Focus on how the solution fits into the broader codebase, backward compatibility, and API design

### PHASE 3: Integrators (3 agents in parallel)

After ALL Phase 2 agents complete, spawn exactly 3 FINAL agents in a SINGLE message. Each integrator receives summaries of ALL Phase 1 and Phase 2 work. Each integrator must:

1. Review all solutions and evaluations from Phases 1 and 2
2. Produce a FINAL integrated solution that combines the best elements
3. Ensure the solution addresses all issues raised by evaluators

Integrator perspectives:
- **Integrator 1 (Consensus Builder)**: Identify the points of agreement across all agents and build on them
- **Integrator 2 (Quality Gatekeeper)**: Ensure every evaluator concern is addressed in the final solution
- **Integrator 3 (Ship It Engineer)**: Focus on producing a clean, complete, ready-to-merge result

### FINAL OUTPUT

After Phase 3 completes, synthesize the three integrator outputs into your final response. If the integrators produced substantially similar results, use the cleanest version. If they differ, reconcile the differences by choosing the most robust approach for each component.

Apply all changes to the actual files. The final result must be a complete, working implementation.

---

`
