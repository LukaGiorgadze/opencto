---
name: codex-noninteractive
description: Use this skill when a user asks for non-trivial code changes — such as implementing a new feature, fixing a bug, refactoring, or any task that requires meaningful edits across one or more files. Skip it for minor changes that can be done in a few lines directly.
allowed-tools: Exec Glob Grep Read
compatibility: Requires Codex CLI
---

# Codex Non-Interactive Mode

Delegate repository code work to `codex exec`. It runs Codex headlessly against
the target project; progress streams to `stderr`, and the final agent message
goes to `stdout`.

## Basic Invocation

```bash
# Read-only analysis
codex exec --cd "$PROJECT_ROOT" "explain what the AuthService class does and identify any issues"

# Write or edit code
codex exec --cd "$PROJECT_ROOT" --sandbox workspace-write "add a rate-limiting middleware to the Express app"

# Skip persisting session files
codex exec --cd "$PROJECT_ROOT" --ephemeral --sandbox workspace-write "rename the user_id field to userId across the codebase"
```

Be specific in the prompt: include file names, function names, failing command
output, or expected behavior when available.

## Prompt Safety

OpenCTO should invoke `codex` with argv, not by concatenating user text into a
shell command string. If the prompt includes user-controlled content and a shell
is unavoidable, pass the prompt through stdin:

```bash
codex exec --cd "$PROJECT_ROOT" --sandbox workspace-write -
```

Do not interpolate raw user text into quoted shell commands.

## Sandbox Choice

Default sandbox is read-only. For any task that writes files, use
`--sandbox workspace-write`.

| Task type | Flag |
|---|---|
| Read, analyze, explain | no sandbox flag |
| Write, edit, create, fix files | `--sandbox workspace-write` |
| Write outside the project | prefer `--add-dir <dir>` with `workspace-write` |
| Needs network or full system access | `--sandbox danger-full-access` only after explicit approval in an isolated environment |

Prefer explicit sandbox flags over `--full-auto` so the permission level is
auditable.

## Machine-Readable Output

Use JSONL when OpenCTO needs to inspect or forward the result programmatically:

```bash
codex exec --cd "$PROJECT_ROOT" --json --sandbox workspace-write "add input validation to the signup endpoint"
```

Useful event types:

- `item.completed` where `item.type == "agent_message"`: final text response
- `item.completed` where `item.type == "file_change"`: file Codex edited
- `turn.completed`: completed turn and token usage
- `turn.failed` or `error`: failed run

Always check the process exit code before treating the run as successful.

## Piping Context

Pipe relevant logs or command output into Codex as context, with the instruction
as the prompt argument:

```bash
npm test 2>&1 \
  | codex exec --cd "$PROJECT_ROOT" --sandbox workspace-write "fix the failing tests"

npx eslint src/ 2>&1 \
  | codex exec --cd "$PROJECT_ROOT" --sandbox workspace-write "fix all lint errors"

generate_task_prompt.sh \
  | codex exec --cd "$PROJECT_ROOT" --sandbox workspace-write -
```

## Code Reviews

Use the dedicated review subcommand for review-only tasks:

```bash
# Review all local changes
codex exec --cd "$PROJECT_ROOT" review --uncommitted

# Review changes against a base branch
codex exec --cd "$PROJECT_ROOT" review --base main

# Review one commit
codex exec --cd "$PROJECT_ROOT" review --commit <SHA>
```

Use normal `codex exec --sandbox workspace-write` only when the user wants fixes
implemented after the review.

## Multi-Turn Tasks

For larger tasks, investigate first in read-only mode, then resume with write
access:

```bash
codex exec --cd "$PROJECT_ROOT" "identify all places where async errors are not handled"

codex exec --cd "$PROJECT_ROOT" --sandbox workspace-write resume --last "add proper error handling to every location you found"

codex exec --cd "$PROJECT_ROOT" --sandbox workspace-write resume <SESSION_ID> "implement the plan"
```

`resume --last` is scoped to the current working directory. Add `--all` only when
resuming a session started from a different project root.

## Key Flags

For the fuller `codex exec`, `resume`, and `review` option reference, read
`$OPENCTO_ROOT/skills/codex-noninteractive/references/command-line-options.md`
when adding or debugging CLI flags.

| Flag | Effect |
|---|---|
| `--cd <dir>` | Set the project root Codex should operate on |
| `--sandbox workspace-write` | Allow file edits in the project workspace |
| `--add-dir <dir>` | Add another writable directory with `workspace-write` |
| `--sandbox danger-full-access` | Full system and network access |
| `--ephemeral` | Do not persist session files to disk |
| `--json` | Emit JSONL events on stdout |
| `-o <path>` | Write final message to a file and stdout |
| `--output-schema <path>` | Constrain the final response to a JSON schema |
| `--ignore-user-config` | Do not load `$CODEX_HOME/config.toml` |
| `--ignore-rules` | Skip `.rules` execpolicy files |
| `--skip-git-repo-check` | Allow running outside a git repo |

## Gotchas

- Always use `--sandbox workspace-write` for code-changing tasks.
- Use `--cd "$PROJECT_ROOT"` so Codex does not operate on OpenCTO's process cwd.
- Use `OPENCTO_ROOT` only for this skill's references, helper files, or when the
  user explicitly asks to work on OpenCTO's own code.
- Use `OPENCTO_WORKSPACE` only for OpenCTO data/artifacts. Do not pass it to
  `--cd` for repository code work.
- Pass user-controlled prompts through argv or stdin, not shell interpolation.
- `danger-full-access` removes the important guardrails; require explicit
  approval and isolation before using it.
- Progress is on `stderr`; the final message is on `stdout`.
- `--json` replaces normal stdout with JSONL; filter for the agent message when
  only the final text is needed.


## Command line options

See `$OPENCTO_ROOT/skills/codex-noninteractive/references/command-line-options.md`
for options and flags for the Codex terminal client.
