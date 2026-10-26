# SPEC-020: LLM-assisted supervision planning and result review

## Goal

Let an operator describe what needs supervision before dispatch, receive an editable supervision proposal, and optionally request an evidence-backed semantic review after an Agent turn.

## Requirements

1. The planning model proposes deterministic timeouts, bounded recovery budgets, verification settings, and semantic acceptance criteria.
2. A proposal never creates a task or executes a command. The operator can inspect and edit every field before dispatch.
3. Suggested verification commands use direct argv only and reject shells. Existing deployment authorization and workspace verification remain authoritative.
4. Task-specific semantic criteria are passed to mid-turn and final semantic reviews as trusted operator-approved priorities.
5. Automatic final semantic review can be disabled per task without disabling deterministic verification.
6. While an interactive task is waiting for input, the operator can manually request a fresh final semantic review.
7. Manual review is advisory: it records evidence but does not change task status, execute a follow-up, or consume a corrective-action budget.
8. Planning and review require the configured semantic model and return an explicit unavailable error otherwise.

## Non-goals

- Executing model-generated shell commands.
- Letting an LLM override deterministic verification, authorization, or lifecycle rules.
- Persisting or displaying model chain-of-thought.

## Acceptance criteria

- The Web form can generate a proposal from the task and optional supervision focus, then edit and dispatch it.
- The REST API exposes planning and manual result-review endpoints.
- Semantic review events identify whether the trigger was automatic or manual.
- A manual review leaves a waiting task in `waiting_input`.
- Unit tests reject unsafe planner output and invalid semantic policy input.
