# OpenCTO — Architecture & Design Specification

> Version 0.6

---

## 1. What OpenCTO Is

OpenCTO is a long-running AI agent that behaves like a senior technical co-founder, like a human, inspired by OpenClaw. Its defining trait is not capability — it's *judgment*: knowing when to ask, when to act, and when to push back. It lives in Discord, ingests everything you share with it, and executes the full software delivery lifecycle from implementation through production monitoring.

It is a **general AI agent** (similar to OpenHands or Devine, but acting at the CTO level). It is designed to be **self-hosted by anyone** — you run it on your own machine, it owns your system natively, and it talks to your team through Discord. There is no SaaS layer.

---

## 2. Core Design Principles

**Block before guessing.** No action begins when OpenCTO lacks enough context to make the decision a senior engineer would be confident making.

**Agent decides, not config.** OpenCTO is not a configured automation tool. It reasons about what tools to use, how to authenticate, how to test its work, and how to execute code. The system prompt provides absolute freedom. 

**OS-Native Autonomy.** OpenCTO runs natively on the host operating system. It does not force execution into Docker containers by default. If the agent decides a task requires Docker for safety or deployment, it will autonomously install and utilize Docker. It has total freedom to execute commands, install dependencies, and manipulate the file system. 

**Git-Backed Decision History.** All actions, architectural decisions, and the *reasoning* behind them (the "why") must be committed to a project Git repository. This ensures the project history is readable, version-controlled, and transparent to the human team.

**Use existing tools first (MCP → API → Shell etc).** Before writing custom code, OpenCTO looks for an existing SDK. library, MCP whatever. If none exists, it looks for an API or CLI to acomplish the task. The fallback is browser automation. The agent autonomously determines which path provides the highest reliability.

**Risk-tiered autonomy.** Every tool action carries a risk tier. Any risk taken is the ultimate responsibility of the human user (CEO). The agent executes within user-configured bounds. Actions involving financial cost are strictly ring-fenced.

**Major choices via Skills, minor choices via Autonomy.** Major foundational choices (e.g., choosing AWS, Supabase, Go, Rust) are guided by "Skills"—guidance files teaching the agent established best practices. Minor choices (e.g., choosing a specific utility library) are evaluated dynamically by the agent based on project requirements.

**Project-scoped from day one.** Every record, workflow, and query is scoped by `ProjectID`. 

---

## 3. Project and Channel Identity

### 3.1 Project Scope

A **project** is the top-level unit of context. Memory, work items, and execution queues belong to a project.

```go
type Project struct {
    ID          string
    Name        string
    Description string
    Channels    []ChannelBinding
    Config      ProjectConfig
    State       ProjectState
    CreatedAt   time.Time
}

type ChannelBinding struct {
    ChannelID   string
    ChannelType ChannelType
    Hint        string
}
```

---

## 4. High-Level System Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                           CHANNEL LAYER                              │
└──────────────────────────────┬───────────────────────────────────────┘
                               │ normalized Event stream
                               ▼
┌──────────────────────────────────────────────────────────────────────┐
│                         INTERNAL EVENT BUS                           │
└──────────────────────────────┬───────────────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────────────┐
│                      AGENTIC RUNTIME  (Temporal)                     │
│                                                                      │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐   │
│  │  Project     │─▶│ Task Workflow│─▶│  Activities.NextAction   │   │
│  │  Workflow    │  │  ReAct Loop  │  │     (LLM + prompt)       │   │
│  └──────────────┘  └──────────────┘  └────────────┬─────────────┘   │
│         │                                          │                 │
│         ▼ (Layer 2 Async Check)                    │                 │
│  ┌──────────────┐                                  │                 │
│  │ Contradiction│                                  │                 │
│  │ Evaluator    │                                  │                 │
│  └──────────────┘                                  │                 │
│                                                    │                 │
│           ┌──────────────┬─────────────────────────┘                 │
│           ▼              ▼              ▼                            │
│     ┌──────────┐  ┌──────────┐  ┌─────────────────────────────┐     │
│     │ Terminal │  │ExecuteTool│ │     Execution Queue        │     │
│     │ Answer   │  │ Activity │  │   (Yielding Mutex)           │     │
│     └──────────┘  └──────────┘  └─────────────────────────────┘     │
└──────────────────────────────────────────────────────────────────────┘
          │                    │                    │
          ▼                    ▼                    ▼
┌──────────────────┐  ┌──────────────────┐  ┌─────────────────────┐
│   MEMORY LAYER   │  │    TOOL LAYER    │  │   SKILL REGISTRY    │
│                  │  │                  │  │                     │
│ SQLite + vec     │  │ OS / Shell       │  │ NextJS              │
│ Pending Checks   │  │ Browser          │  │ Supabase            │
│                  │  │ HTTP             │  │ Hetzner             │
│                  │  │ MCP client etc..      │  │ AWS / GCP etc, for exmaple│
└──────────────────┘  └──────────────────┘  └─────────────────────┘
```

---

## 5. Component Breakdown

### 5.1 Channel Layer
Normalizes inbound signals into a single `Event` type.

### 5.2 Integration Layer — Agent-Driven Setup
OpenCTO does not have hardcoded paths for how to authenticate with third-party tools. 

When given a new integration goal, the agent:
1. Searches the local MCP registry file.
2. Explores the tool's authentication requirements via API docs or MCP capabilities.
3. Determines the best path (e.g., asking the user for a token, generating a URL).
4. Passes credentials through explicit runtime configuration and environment variables.

**Git Authentication:**
When OpenCTO is asked to interact with a repository ("Here is the repo for the project"), it must request missing credentials explicitly. The core OpenCTO Git configuration (if it manages its own state) is fed via standard environment variables.

### 5.3 Internal Event Bus
Single-process Go channels + goroutines.

### 5.4 Tool Layer — OS-Native
The tool layer provides primitives. The agent chooses when and how to use them. **The shell executes natively on the host OS.** 

```go
type ToolKit interface {
    // PRIMARY: full host OS access
    Shell(ctx context.Context, cmd string, opts ShellOpts) (ShellResult, error)
    
    BrowserOpen(ctx context.Context, url string) (Page, error)
    BrowserAct(ctx context.Context, page Page, action BrowserAction) (BrowserResult, error)
    FileRead(ctx context.Context, path string) ([]byte, error)
    FileWrite(ctx context.Context, path string, content []byte) error
    HTTPRequest(ctx context.Context, req HTTPReq) (HTTPResp, error)
    MCPConnect(ctx context.Context, endpoint string) (MCPSession, error)
    MCPCall(ctx context.Context, session MCPSession, tool string, params map[string]any) (any, error)
}
```

### 5.5 Agentic Runtime (Temporal)

Temporal ensures long-running workflows survive restarts. 
To prevent Temporal History Size crashes (which occur when workflow history grows too large over months of operation), the main project loop heavily utilizes **`ContinueAsNew`** and spawns discrete **Child Workflows** for isolated tasks.

#### 5.5.1 NextAction Decision Activity

Tool selection, tool-result observation, event persistence, memory updates, and final reporting are owned by one non-deterministic Temporal activity: `Activities.NextAction`. The Task Workflow does not schedule separate triage, question, observer, persistence, or reporting activities.

`Activities.NextAction` receives the original event, current decision state, prior observations, and the last `Activities.ExecuteTool` result. It returns exactly one of:

- one `ToolChoice` for `Activities.ExecuteTool`
- one terminal result: `completed`, `blocked`, `failed`, `awaiting_approval`, or `ignored`

When the agent needs more information or approval, it returns a blocked terminal answer with the exact question or blocker.
* **Dependency Auditing:** Before the agent selects a third-party dependency or package, it must perform an audit step using available ecosystem tools (e.g., checking registry stats) to mitigate hallucinated packages.

#### 5.5.2 Execution Workflow & Concurrency

Multiple engineers might request conflicting tasks across different Discord channels.
To prevent race conditions, OpenCTO enforces a **Project-Level Execution Mutex**.

1. **Locking:** While a task is *actively executing*, it holds the Mutex. Any new `ACTION_REQUEST` enters a queue.
2. **Iterative ReAct Loop:** A task alternates as `Activities.NextAction -> Activities.ExecuteTool -> Activities.NextAction`. A successful command never completes the workflow by itself. Only `Activities.NextAction` observes the previous tool result and chooses the next single tool call or terminal answer.
3. **Circuit Breaker:** The execution loop is bounded. If the agent cannot converge within the allowed execution cycles, it halts and reports the blocker instead of spinning indefinitely.

---

## 6. Autonomy Model

### 6.1 Tier System

- **Tier 0:** Read / Observe (Autonomous)
- **Tier 1:** Safe local change
- **Tier 2:** Consequential but reversible
- **Tier 3:** Irreversible, production-facing, or **financially consequential**. *Note: Any action that spins up paid cloud resources, charges a card, or scales infrastructure defaults to Tier 3.*

---

## 7. Blocked Answer Rules

1. **Memory before questions.**
2. **Ask only when blocked.**
3. **State what you know first.**
4. **Explain each question briefly.**
5. **Keep it short.**
6. **Hard-gate on contradictions.** Resolve `PendingContradiction` before continuing.

---

## 8. Configuration

```toml
[project]
id   = "default"

[llm]
provider         = "openai"
base_url         = "http://127.0.0.1:4000"
model_reasoning  = "gpt-5.4"
model_fast       = "gpt-5.4-mini"
transcription_model = "gpt-4o-mini-transcribe"
embedding_model  = "text-embedding-3-small"

[mcp]
registry_file = "./config/mcp-registry.json"

[autonomy]
threshold                  = 2

[temporal]
..temporal specific config...
```

---

## 9. Tech Stack

| Layer | Technology |
|-------|------------|
| Language | Go |
| Durable execution | Temporal (embedded via temporal, `ContinueAsNew` enabled) |
| MCP client | Connects to pre-defined registry of MCP servers |
| Version Control | Git (agent tracks decisions, ADRs, and context and explanation of each decision) |
| Execution | Native OS / Shell (Agent decides everything, openclaw like agent) |
| LLM | Fast API model for triage, Reasoning model for logic |

--

## Folder Structure, Design Patterns, and Best Practices.

---

### 1. Folder Structure (Idiomatic Go)

We will use the standard Go project layout, strictly separating Temporal Workflows (deterministic) from Activities (non-deterministic LLM/OS calls).

```text
opencto/
├── cmd/
│   └── opencto/                # Main entry point (wire up dependencies, start workers/server)
├── internal/
│   ├── channels/               # Channel Layer (Discord, CLI, Webhooks)
│   │   ├── discord/
│   │   └── types.go            # Normalized Event types
│   ├── bus/                    # Internal Event Bus (Pub/Sub via channels/goroutines)
│   ├── runtime/                # Agentic Runtime (temporal)
│   │   ├── workflows/          # Deterministic orchestration (Project/Task loop)
│   │   └── activities/         # Non-deterministic logic (LLM calls, Shell execution, DB reads)
│   ├── agent/                  # Core Intelligence
│   │   ├── prompts/            # Embedded markdown/text templates for system prompts
│   │   ├── llm/                # LLM client (OpenAI interface, parsing, retry logic)
│   ├── tools/                  # Tool Layer (OS-Native executions)
│   │   ├── shell/              # OS Shell executor (os/exec wrapper)
│   │   ├── browser/            # Browser automation (e.g., Playwright/Rod)
│   │   ├── mcp/                # MCP Client implementation
│   └── config/                 # TOML parsing and Config Structs
├── pkg/                        # Exportable/Reusable packages (if any)
├── data/                       # Local runtime artifacts and logs (gitignored)
├── skills/                     # Skill definition files (e.g., supabase.md, nextjs.md)
├── config/                     # Default config (e.g., config.toml, mcp-registry.json)
├── go.mod
└── go.sum
```

---

### 2. Core Design Patterns to Implement

#### A. The "Actor Pattern" (Temporal implementation of the Yielding Mutex)
**The Pattern:** Use a **Temporal Singleton Workflow** per `ProjectID`. 
* The workflow loops indefinitely.
* It listens to Temporal **Signals** (Inbound Events and contradiction resolutions).
* It maintains an internal queue `[]WorkItem`.

#### B. Strategy Pattern (Tools & Channels)
Tool layer needs to be extremely modular. The Agent will decide *how* to execute a task.
```go
// internal/tools/toolkit.go
type ToolKit interface {
    Execute(ctx context.Context, cmd Command) (Result, error)
}

// Strategy implementations
type ShellTool struct { ... }
type MCPTool struct { ... }
type BrowserTool struct { ... }
```
When Temporal asks the LLM what to do, the LLM returns an enum (`TOOL_SHELL`, `TOOL_MCP`), and a factory routes it to the correct strategy.

#### C. Saga Pattern (Circuit Breaking & Rollbacks)
Because OpenCTO operates on the local OS, an execution might partially succeed (e.g., installed dependency, but failed to write the config file).
* Temporal native compensation (Sagas) should be used. If an Activity fails X times (this should be configurable) (the Circuit Breaker mentioned), Temporal should automatically execute a "Compensating Activity" (e.g., `git reset --hard` or uninstalling the package).

#### D. Repository Pattern (Memory Layer)
Abstract persistence entirely. The LLM agent should only know about "Context".
```go
// internal/memory/repository.go
type MemoryStore interface {
    SaveContradiction(ctx context.Context, projID string, c Contradiction) error
    CheckContradictions(ctx context.Context, projID string) ([]Contradiction, error)
    RetrieveContext(ctx context.Context, projID string, query VectorQuery) ([]Fact, error)
}
```

---

### 3. Best Practices to Dictate to the AI Codex

When instructing your AI to write the code, enforce these strict rules:

#### 1. Temporal Strict Determinism
* **Rule:** NEVER use `time.Now()`, `math/rand`, or make API/DB calls inside `internal/runtime/workflows/`. 
* **Rule:** All LLM calls, Discord reads, file writing, and persistence interactions MUST happen inside `internal/runtime/activities/`. 
* **Rule:** Workflows must use `workflow.Now()`.

#### 2. Managing the LLM Context & Prompts
* **Rule:** Do not hardcode prompts in Go strings. Place them in `internal/agent/prompts/` as `.tmpl` files and use Go's `embed` package. This allows you to easily tweak prompts without hunting through logic.
* **Rule:** Enforce **Structured Output** via tool-calling. OpenAI Responses API and compatible SDK layers support structured JSON outputs. The `Decision Engine` activity must unmarshal LLM outputs directly into Go structs such as `ToolChoice` and terminal `NextAction` results.

#### 3. OS-Native Security (Critical)
* Because OpenCTO runs OS-natively, it has the power to run `rm -rf /`.
* **Rule:** The `internal/tools/shell` executor must enforce an absolute working directory boundary. Even if it's "OS Native", restrict the execution context to the specific `ProjectID` workspace folder.
* **Rule:** Credentials must come from explicit runtime configuration or environment variables. Do not allow the agent to write `.env` files with production secrets; inject them as environment variables during the `os/exec` command generation.

#### 4. The "ContinueAsNew" Loop
* **Rule:** The main Project Workflow will eventually hit Temporal's 50k event history limit.
* Structure the main loop to track iteration count. After ~1,000 events, it must pack the current state (queue, active tier status) into a struct and call `return workflow.NewContinueAsNewError(ctx, MainProjectWorkflow, currentState)`.

#### 5. Graceful Degradation of Tools
* Ensure the system explicitly attempts the fallback logic:
  `MCP, API, Shell, etc.`. 
* Code this fallback choice inside `Activities.NextAction`. The Task Workflow still schedules only `Activities.NextAction` and `Activities.ExecuteTool`; it must not catch tool-specific fallback branches directly.

### 4. Next Step: How to prompt the AI

> **Prompt to start:**
> *"Here is the OpenCTO Architecture Spec (v0.6). We are going to build this in Go using Temporal. Do not build the whole thing at once. 
> **Phase 1:** Set up the Go project structure as defined. Then, implement the `internal/tools/shell` interfaces and the runtime configuration boundaries around credentials. Provide mock implementations so we can test the boundaries. Do not write Temporal workflows yet."*

Build the side-effect layers first via interfaces, the Temporal Workflows and LLM routing will be much cleaner to build in Phase 2.
