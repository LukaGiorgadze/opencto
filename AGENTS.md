# AGENTS.md

## Purpose

This repository implements OpenCTO: a long-running, self-hosted AI technical operator for founders and indie hackers.

OpenCTO helps users plan, build, test, ship, maintain, and monitor software projects through a conversational interface. It should behave like a practical technical co-founder: autonomous when the path is clear, careful when risk is high, and willing to ask for clarification when needed.

This file defines how coding agents should work in this repository.

The goal is not to produce the most code.
The goal is to build a reliable, safe, understandable, and maintainable system.

---

## Product Summary

OpenCTO is an AI agent runtime that:

- receives user or team requests
- understands project context
- decides what should happen next
- uses tools to inspect, modify, test, and operate software
- asks for approval when actions are risky
- reports progress and results back to the user

OpenCTO should feel like a technical operator that can take ownership of software delivery, not just a code generator.

---

## Core Principles

### 1. Keep the system simple
Prefer simple, explicit designs over clever abstractions.

### 2. Preserve clear boundaries
Separate orchestration, runtime, tools, agent logic, and persistence.

### 3. Keep workflows deterministic
No side effects directly inside workflow logic.

### 4. Treat safety as part of the product
Always consider risk before execution.

### 5. Verify before reporting success
Do not assume a command equals completion.

---

## ReAct Execution Model

NextAction -> ExecuteTool -> observe -> repeat -> final answer

Each step must:
- call exactly one tool OR
- return a final answer

---

## Coding Style

- Simple, explicit Go
- Small functions
- No hidden side effects
- Proper error handling
- Clear naming

---

## Testing

Test meaningful behavior:
- success flows
- failure flows
- safety conditions

Skip trivial tests.

---

## Tool Use

Tools must be:
- explicit
- safe
- auditable

Exec usage must be careful and constrained.

---

## Configuration

- Explicit
- Typed
- No secrets in code

---

## Persistence

- Scoped by project
- Store meaningful state only

---

## Prompts

- Structured outputs preferred
- Validate before execution
- No unsafe direct execution

---

## Documentation

Keep docs updated and practical.

---

## What to Avoid

- Overengineering
- Hidden behavior
- Unsafe execution
- Large rewrites without reason

---

## Definition of Done

- Code compiles
- Behavior verified
- Safe and maintainable

---

## Tech stack

- Go
- temporal
- langchain
