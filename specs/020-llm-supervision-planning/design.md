# Design

## Start-of-task flow

`POST /api/v1/supervision/plan` calls the configured semantic reviewer only when it also implements `SupervisionPlanner`. The model returns strict JSON. The supervisor normalizes durations and budgets, bounds text, and rejects shell-based verification commands. The browser copies the proposal into ordinary editable task fields; only the existing task submission endpoint can start execution.

## Per-task semantic policy

`supervision.semantic_review` contains `final_review` and up to eight acceptance criteria. These values are captured in the task runtime and supplied to the existing read-only semantic toolbox. The deterministic verification report remains authoritative.

## End-of-task flow

After deterministic verification succeeds, the runtime stores a bounded review context: trusted operator messages, current instruction, complete response, verification report, and supervisor snapshot. `POST /api/v1/tasks/{id}/review` can replay that context only while the task is waiting for operator input. It publishes the standard semantic review events with `trigger=manual` and returns the structured verdict without applying it.

## Safety boundaries

- Planner output is a proposal, never an action.
- Commands are direct argv and still pass normal request authorization at dispatch.
- Review tools are read-only and outputs are bounded/redacted by existing event handling.
- A manual verdict cannot complete, fail, redirect, or stop a task.
