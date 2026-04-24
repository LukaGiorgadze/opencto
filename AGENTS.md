# AGENTS.md

## Purpose

This repository implements OpenCTO: a human like, long-running, self-hosted AI technical co-founder that ingests user/team events, loads project context, decides whether to clarify or act, plans work, executes through tools, asks for approval for consequential actions, persists memory, and reports results back to the team. Inspired by OpenClaw.

This file defines how coding agents must work in this repository.

The goal is not to generate large amounts of code quickly.
The goal is to build a reliable, inspectable, testable system that matches the architecture spec and preserves safety boundaries.

---

## Product Summary

OpenCTO is a project-scoped runtime with these major layers:

1. Input and channel normalization
2. Internal event bus
3. Agentic runtime built around Temporal.io
4. Supporting systems:
   - Memory layer
   - Tool layer
   - Skill registry
   - Vault / credentials
   - Git / ADR history
5. Output / feedback / operations

Core flow:

User event
-> normalize into Event
-> classify
-> load context
-> decision engine
-> either contradiction handling, clarification, or planning
-> create work items
-> execution queue / project workflow
-> approval gate when required
-> re-validation before resume
-> tool execution
-> tests / validation / deploy / monitor
-> report back
-> persist facts and history
-> continue loop

---

## Primary Architecture Rules

### 1. Preserve the architecture
Do not collapse the system into one package or one workflow.
Keep the boundaries clear:

- `workflows/` = deterministic Temporal workflow code only
- `activities/` = non-deterministic code only
- `tools/` = external side effects and adapters
- `memory/` = persistence and retrieval
- `channels/` = inbound integrations
- `agent/` = prompts, structured decisioning, skill loading
- `config/` = configuration only

### 2. Determinism is mandatory
Never place non-deterministic behavior in Temporal workflows (or in rare cases use workflow.SideEffect).

Inside workflows, do not:
- call LLMs
- read or write files
- access SQLite directly
- use network calls
- use `time.Now()`
- use random values
- call OS tools

Inside workflows, use:
- workflow inputs
- workflow state
- workflow signals
- workflow queries
- Temporal timers / `workflow.Now()`
- activities for all side effects

### 3. Side effects must be isolated
All side effects must go through explicit interfaces.
No hidden shell calls, hidden DB writes, or hidden HTTP requests in business logic.

### 4. Project scope is mandatory
Every persisted record and runtime operation must be scoped by `ProjectID`.

### 5. Safety before autonomy
OpenCTO is allowed to be powerful, but code in this repository must default to explicit safety checks and policy enforcement before execution.

---

## Non-Goals for Early Phases

Unless the current task explicitly requires it, do not introduce:

- multi-agent swarm orchestration
- Kubernetes deployment complexity
- distributed multi-node runtime
- production cloud provisioning
- autonomous payment flows
- broad GUI/dashboard work
- advanced browser automation flows
- multiple persistence backends
- plugin ecosystems beyond the defined tool interfaces and etc.

Prefer the smallest implementation that preserves the architecture and leaves clean extension points.

---

## Implementation Priorities

When starting work, prefer this order:

1. Domain models and contracts
2. Persistence schema and repositories
3. Tool interfaces and safe adapters
4. Temporal workflow contracts
5. Prompt / decision engine structured IO
6. Channel adapters
7. Approval and policy engine
8. Observability
9. opencto full OS access abilitiy
10. End-to-end flows

If asked to build a large feature, break it into phases and implement the minimal vertical slice first.

---

## Required Domain Model

Do not invent ad hoc structs in random packages.
Create and reuse canonical models for at least:

- `Project`
- `ChannelBinding`
- `Event`
- `WorkItem`
- `Plan`
- `ExecutionAttempt`
- `ApprovalRequest`
- `PendingContradiction`
- `MemoryFact`
- `ToolInvocation`
- `Artifact`
- `ADR`
- `Integration`
- `CredentialRef`

Each relevant model should include:
- stable ID (UUID)
- `ProjectID`
- status enum where applicable
- strict types where applicable
- timestamps
- provenance / metadata
- correlation and causation IDs where useful (UUID)

Keep these models explicit and boring.
Avoid clever abstractions.

---

## Repository Layout Expectations

Use or preserve this layout:

```text
cmd/opencto/
internal/
  channels/
  bus/
  runtime/
    workflows/
    activities/
  agent/
    prompts/
    llm/
    skills/
  tools/
    shell/
    browser/
    mcp/
    git/
  memory/
    sqlite/
    vault/
  config/
pkg/
data/
skills/
config/
```

Guidelines:
- `internal/` holds application code
- `pkg/` is only for truly reusable public packages
- do not move runtime logic into `cmd/`
- do not put workflow code into adapter packages
- do not put database schema inside unrelated packages
- do not write duplicate code, re-use first

---

## Coding Rules

### General
- Prefer simple, explicit Go.
- Favor small interfaces with one responsibility.
- Prefer composition over inheritance-like patterns.
- Avoid magic behavior.
- Avoid global mutable state.
- Avoid duplicate code.
- Keep function names literal and unsurprising.
- Avoid useless comments.
- Use structured errors.
- Return typed errors for important failure classes.
- Code Formatting.
- Naming Conventions.
- Package Design Principles.
- Error Handling Patterns.
- Function and Method Design.
- Concurrency Guidelines.
- Comment Conventions, no useless comments.
- Testing Conventions.
- Performance Optimization Guidelines.
- Follow `uber-go` style guide: https://github.com/uber-go/guide/blob/master/style.md

### Concurrency
- Be explicit about ownership of shared state.
- If a component is single-threaded by design, state that in comments and preserve it.
- Avoid hidden goroutines unless they are part of a clearly owned runtime loop.
- All goroutines must have shutdown behavior.

### Context usage
- Thread `context.Context` through activities, tool execution, repositories, and IO boundaries.
- Do not store context in structs.
- Respect cancellation and deadlines.

### Configuration
- All config must be explicit and typed.
- Support sane defaults, but do not hide critical behavior in defaults.
- Never hardcode secrets.
- Never assume a production environment exists.

---

## Temporal Rules

### Workflow design
Use `ProjectID` as prefix for temporal workflows.
Use a singleton workflow per `ProjectID` for project-level coordination.
Use child workflows where isolation is helpful.

Expected top-level workflow concepts:
- `ProjectWorkflow`
- `TaskWorkflow`
- `ApprovalWorkflow`
- `ContradictionWorkflow`

### Workflow behavior
- Workflows should manage state transitions and orchestration only.
- Activities perform all IO and side effects.
- Use signals for inbound events and human approvals.
- Use queries for read-only inspection where helpful.
- Use `ContinueAsNew` to control workflow history growth.

### Continue-as-new
Do not wait until history is huge.
Design state snapshots early and intentionally.

Persist or carry forward only what is required:
- queue state
- active approvals
- pending contradictions
- current execution metadata
- compact workflow state only

Do not carry bulky transient data.

### Retry policy
Use different retry policies for:
- LLM calls
- shell commands
- network calls
- database operations
- browser automation

Do not use one retry policy for everything.

---

## Activity Rules

Activities must be small and single-purpose.
Examples of valid activity responsibilities:
- load memory context
- persist contradiction
- call reasoning model
- call fast model
- run shell command
- read file
- write file
- commit ADR
- load skill files
- fetch integration metadata

Do not build giant “god activities”.

Activities must emit structured results, not free-form text blobs, whenever possible.

---

## Prompting and LLM Rules

- Store prompts as files under `internal/agent/prompts/`.
- Do not hardcode large prompts in Go source.
- Use `embed` for prompt loading where appropriate.
- Prefer structured outputs with schemas.
- Parse model outputs into typed Go structs.
- Fail loudly on schema mismatch.
- Never let raw LLM text directly drive dangerous execution without policy validation.
- Use langchaingo

The LLM should decide intent and structured plans.
The policy layer decides whether execution is allowed.

---

## Tool Layer Rules

OpenCTO prefers:
1. MCP
2. Shell / OS
3. API / HTTP
4. Browser automation
5. Anything else

Preserve this fallback order unless a task explicitly requires something else.

All tool execution must produce a structured record that includes:
- requested intent
- chosen tool
- fallback candidates
- risk classification
- timeout
- working directory
- input summary
- output summary
- exit status or equivalent
- error details
- rollback or compensation notes when relevant

### Shell safety
The shell executor is high risk.
Treat it as a guarded capability, not a convenience helper.

Rules:
- enforce working directory boundaries
- prevent execution outside allowed project workspace unless explicitly approved
- support timeouts
- capture stdout/stderr separately
- log executed command metadata
- avoid shell string concatenation when safer argument-based execution is possible
- redact secrets from logs
- never assume `sudo`
- never write secrets into `.env` files by default

---

## Policy Engine Rules

Before dangerous execution, policy must evaluate at least:
- risk tier
- path safety
- command safety
- network egress safety
- secret exposure risk
- destructive action risk
- financial / production impact

Tier model:
- Tier 0: read / observe
- Tier 1: safe local change
- Tier 2: consequential but reversible, approval required
- Tier 3: irreversible, production-facing, or financial, owner approval required

No command should execute only because the LLM suggested it.
Policy validation is mandatory.

---

## Memory Layer Rules

Implement memory as explicit categories, not a single blob:

- conversation memory
- project facts
- pending contradictions
- execution history
- decisions / ADR references

Memory retrieval must be scoped by project.
Memory writes must preserve provenance.

Contradictions are first-class records.
If facts conflict, do not silently overwrite.
Create or update contradiction records and require explicit resolution where the design requires it.

---

## Git / ADR Rules

OpenCTO tracks decisions in Git, but do not dump raw chain-of-thought into the repository.

Use Git / ADR history for:
- architectural decisions
- meaningful execution summaries
- rationale that a teammate can audit
- project evolution

Do not commit:
- noisy internal scratch reasoning
- secrets
- giant generated logs unless explicitly requested

Prefer concise ADRs and structured summaries.

---

## Testing Rules

Every non-trivial change should come with tests unless the task is explicitly documentation-only.

Minimum test expectations:
- unit tests for pure logic
- repository tests for persistence
- activity tests for adapters
- Temporal workflow tests for orchestration
- end-to-end tests for important flows when practical

Test the contracts, not just happy paths.

Always cover:
- approval pause / resume
- contradiction blocking
- workflow retry behavior
- policy rejection
- invalid structured LLM output
- shell timeout and cancellation
- project scoping

---

## Observability Rules

Build inspectability in from the start.

Prefer:
- structured logs
- clear event IDs
- correlation IDs
- metrics around retries, queue size, failures, approvals, and tool latency

When adding logs:
- log actions and state transitions
- avoid log spam
- redact secrets
- keep logs useful for operators

---

## Documentation Rules

When changing architecture or behavior:
- update the relevant docs
- update ADRs when decisions change
- keep comments accurate
- document assumptions in code near the contract they affect

Do not add decorative comments.
Add comments only where they reduce ambiguity.

---

## How to Work on Tasks

For any task larger than a tiny edit, first create a short implementation plan in the working thread or in a planning document if requested.

Use this format:

1. Goal
2. Constraints
3. Files to touch
4. Contracts to preserve
5. Steps
6. Tests
7. Risks / unknowns

For larger or multi-step tasks, use a dedicated planning document and keep it updated as work progresses.

Do not implement everything at once if the task is architecture-heavy.
Prefer incremental phases with compiling checkpoints.

---

## Expected Development Style

When writing code:
- make the smallest clean change that matches the architecture
- keep interfaces stable
- preserve extension points
- avoid speculative abstractions
- avoid introducing frameworks unless necessary
- prefer explicit constructors
- prefer dependency injection through structs

When editing existing code:
- align with surrounding style if it is already good
- improve local quality without broad unnecessary rewrites
- do not rename large surfaces unless required

---

## What to Avoid

Do not:
- collapse all logic into a single service object
- bypass interfaces for “just one quick call”
- put DB access inside workflows
- let prompts drift into code strings everywhere
- make unsafe shell execution easy
- add hidden environment assumptions
- add generic abstractions with no current use
- overengineer early phases
- silently ignore errors
- silently downgrade safety checks

---

## Preferred Early Deliverables

If asked to bootstrap the project, prefer this order:

Phase 1:
- module setup
- folder layout
- config loading
- canonical domain types
- repository interfaces
- shell interface and safe mock
- SQLite repository skeleton
- test scaffolding

Phase 2:
- Temporal workflow contracts
- activity contracts
- project workflow skeleton
- signal and query definitions
- continue-as-new mechanics
- approval / contradiction state model

Phase 3:
- LLM clients
- structured decision engine
- prompt loading
- skill loading
- policy engine

Phase 4:
- Discord adapter
- end-to-end event flow
- Git / ADR integration
- observability
- richer tool adapters

---

## If the Task Is Ambiguous

If requirements are missing:
- preserve architecture
- choose the smallest reversible design
- leave TODOs only when they are concrete and intentional
- document the assumption in the changed code or ADR if important

Do not invent product behavior that changes the system model unless necessary.

---

## Definition of Done

A task is done when:
- code compiles
- tests relevant to the change pass
- architecture boundaries are respected
- safety and policy checks are preserved
- docs are updated if behavior changed
- the change is small enough to review confidently

---

## Local Override Section

The human owner may append project-specific instructions below this line.
Do not remove them.

<!-- OWNER_APPEND_ONLY -->
