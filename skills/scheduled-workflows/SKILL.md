---
name: scheduled-workflows
description: Use before any OpenCTO scheduled workflow action — WorkflowCreate, WorkflowUpdate, WorkflowDelete, WorkflowOperation, or any direct edit under $OPENCTO_WORKSPACE/workflows/{workflow_id}.
---

# Scheduled Workflows

## Source Of Truth

The only source of truth for a workflow is its source repository:

```
$OPENCTO_WORKSPACE/workflows/{workflow_id}/
```

The scheduled workflow points to a committed hash from this repository. Runtime snapshots, logs, caches, dependency folders, build outputs, and generated files are not source of truth.

Persistent workflow data lives outside Git at:

```
$OPENCTO_WORKSPACE/workflows/{workflow_id}/data/
```

This directory is ignored by Git, survives `WorkflowUpdate`, and is deleted by `WorkflowDelete`.

## Directory Layout

```
$OPENCTO_WORKSPACE/
  workflows/
    {workflow_id}/
      workflow.yml          <- editable manifest
      src/                  <- default location for implementation files
      data/                 <- persistent cross-run data, Git-ignored
      .git/                 <- do not edit directly
      .gitignore            <- managed by OpenCTO; add custom ignores at the bottom, do not remove OpenCTO-managed entries
  workflow-runs/
    {workflow_id}/
      {run_id}/
        workflow.yml
        src/
        artifacts/          <- step outputs shared within this run
```

`workflow.yml` defines the workflow name, description, schedule, notification policy, global env, and ordered steps.

`src/` is the default location for implementation files, but workflow source is not limited to `src/`. Scripts, configs, and packages may live anywhere in the workflow directory unless the path is reserved or ignored.

`workflow-runs/{workflow_id}/{run_id}/` is a per-run snapshot. Treat as runtime evidence only — never edit it.


## Runtime Environment Variables

Set by OpenCTO at step execution time. Do not hardcode their values.

```
OPENCTO_WORKFLOWS_DIR     — root of all workflow source repos under $OPENCTO_WORKSPACE.
                            Each workflow source repo is at OPENCTO_WORKFLOWS_DIR/{workflow_id}/.
                            Read-only at step runtime — do not write here from within a step.

OPENCTO_WORKFLOW_RUN_DIR  — current run snapshot directory and step working directory.
                            Use OPENCTO_WORKFLOW_RUN_DIR/artifacts/ to pass files between
                            steps in the same run. Not available to future runs.

OPENCTO_WORKFLOW_DATA_DIR — persistent data directory at OPENCTO_WORKFLOWS_DIR/{workflow_id}/data/.
                            Git-ignored and writable at step runtime.
                            Use for state or outputs that future scheduled runs must read.
```
Custom environment variables can be defined in `workflow.yml` under `env` as literal `NAME=value` entries and are injected into every step alongside the runtime variables above.

## Before Any Workflow Action

Identify `workflow_id` before acting.

If the target is ambiguous or the user asks for a read or control operation:

```
WorkflowOperation operation=list
WorkflowOperation operation=describe workflow_id=<workflow_id>
```

For `delete`, `pause`, `resume`, or `trigger`, call `WorkflowOperation operation=describe` first unless the current turn already contains a fresh description for that workflow.

## Before Editing or Updating

Before any `Edit`, `Write`, or `WorkflowUpdate`, gather live context:

1. Read `$OPENCTO_WORKSPACE/workflows/{workflow_id}/workflow.yml`
2. List `$OPENCTO_WORKSPACE/workflows/{workflow_id}/` and relevant source directories
3. Read relevant source files
4. Run `git -C $OPENCTO_WORKSPACE/workflows/{workflow_id} status --porcelain`
5. Run `git -C $OPENCTO_WORKSPACE/workflows/{workflow_id} log --oneline -n 20`
6. If schedule state matters: `WorkflowOperation operation=describe`

Skip inspection only when the user supplies the exact file and replacement for a small mechanical change.

## Workflow Design Rules

Each step runs as a Temporal activity. Keep every step single-purpose, bounded, and safe to retry.

- Steps must be idempotent or tolerate repeated execution without duplicating irreversible external effects. Use deterministic keys, upserts, checkpointing, or explicit duplicate guards when writing external state.
- The manifest defines durable step boundaries only. Implementation files are ordinary programs, scripts, or binaries invoked by `command` and `args`. Do not write schedulers, workers, queues, daemons, or orchestration framework code in workflow source.
- `command`: executable only. `args` is optional, but external commands such as
  `python3` usually need args. Never repeat the executable in `args`.
- Optional step fields may be omitted: `args` for workflow-local executables,
  `schedule_to_close_timeout`, and `retry_policy`. Omitted retry settings use
  runtime defaults.
- Global `env` entries apply to every step and must be literal `NAME=value` assignments. Do not use template syntax. `OPENCTO_*` names are reserved.
- `files[].path` is relative to `workflows/{workflow_id}/`. Must not target reserved or ignored paths.

## Step State Passing

OpenCTO does not automatically pass business data between steps via Temporal results or template variables.

**Within a run** — write to `OPENCTO_WORKFLOW_RUN_DIR/artifacts/`:

```sh
mkdir -p "$OPENCTO_WORKFLOW_RUN_DIR/artifacts"
printf '{"ok":true}\n' > "$OPENCTO_WORKFLOW_RUN_DIR/artifacts/payload.json"
```

A later step reads from the same directory:

```sh
cat "$OPENCTO_WORKFLOW_RUN_DIR/artifacts/payload.json"
```

**Across runs** — write to `OPENCTO_WORKFLOW_DATA_DIR/`. Use only for state future scheduled runs must read.

Use clear artifact contracts: stable filenames, documented schemas or simple text formats, and explicit behavior for missing or invalid artifacts.

Do not use template syntax such as `{{steps.check.stdout}}` in `env` or manifest fields.

## Editing and Updating

`Edit` or `Write` for local source files. All source changes must stay under `$OPENCTO_WORKSPACE/workflows/{workflow_id}/`.

Before `WorkflowCreate` or `WorkflowUpdate`, verify the workflow:

1. Re-read changed files if needed
2. Run `git -C $OPENCTO_WORKSPACE/workflows/{workflow_id} diff -- .`
3. Run any available lightweight validation or tests
4. Confirm the manifest still follows the single-responsibility step model
5. When safe, validate with a one-shot run:
   - Existing workflows: call `WorkflowUpdate` first if dirty, then `WorkflowOperation operation=trigger`
   - New workflows: run local validation first, then after `WorkflowCreate` succeeds, trigger one validation run
6. If a validation run could send messages, charge money, or modify external systems — ask first. Document skipped validation if the user declines.

Call `WorkflowUpdate` only after verification succeeds. It validates the bundle, commits dirty changes, and repoints the schedule to the new commit hash.

Do not run `git commit` manually for workflow bundles.

Before triggering, ensure no dirty local changes and that the scheduled commit hash is current. If dirty or HEAD is unpublished, call `WorkflowUpdate` first.

## Examples

Good folder structure:

```text
$OPENCTO_WORKSPACE/workflows/github-stargazers/
  workflow.yml
  src/
    fetch_stargazers.py
    append_visible_emails.py
  data/
    cursor.json
    stargazers.txt
  .git/
  .gitignore

$OPENCTO_WORKSPACE/workflow-runs/github-stargazers/{run_id}/
  workflow.yml
  src/
    fetch_stargazers.py
    append_visible_emails.py
  artifacts/
    fetched_stargazers.json
```

Good `workflow.yml`:

```yaml
name: github stargazers
description: fetch new stargazers and append visible emails
schedule:
  cron: "*/3 * * * *"
  one_shot_at: ""
  time_zone_name: UTC
  overlap_policy: skip
  catchup_window: 10m
  pause_on_failure: false
notification_policy:
  on_failure: true
env:
  - GITHUB_REPO=LukaGiorgadze/wasify-go
  - FOO=bar
steps:
  - id: fetch
    command: python3
    args: ["src/fetch_stargazers.py"]
    start_to_close_timeout: 2m
    retry_policy:
      backoff_coefficient: 0
      maximum_attempts: 3
  - id: append
    command: python3
    args: ["src/append_visible_emails.py"]
    start_to_close_timeout: 2m
    retry_policy:
      backoff_coefficient: 0
      maximum_attempts: 3
```

Why this is good:

- Each step has one responsibility.
- `fetch` writes same-run output to `OPENCTO_WORKFLOW_RUN_DIR/artifacts/fetched_stargazers.json`.
- `append` reads that artifact and writes cross-run output/cursor files under `OPENCTO_WORKFLOW_DATA_DIR`.
- The workflow uses global env only for literal configuration, not runtime state.
- No source code is written under `data/`, `artifacts/`, `workflow-runs/`, dependency folders, or logs.

Bad folder structure:

```text
$OPENCTO_WORKSPACE/workflows/github-stargazers/
  workflow.yml
  src/
    node_modules/
      helper.js
    fetch.log
  data/
    fetch_stargazers.py
  steps/
    fetch/
  artifacts/
    fetch/
```

Why this is bad:

- `src/node_modules/` and `*.log` are ignored/reserved and will not be durable source.
- `data/` is for persistent runtime data, not implementation code.
- `steps/` and top-level `artifacts/` are not source layout directories.
- Runtime artifacts belong under `OPENCTO_WORKFLOW_RUN_DIR/artifacts/` during a run.

Bad `workflow.yml`:

```yaml
name: github stargazers
description: fetch and append stargazers
schedule:
  cron: "*/3 * * * *"
  one_shot_at: ""
  time_zone_name: UTC
  overlap_policy: allow_all
  catchup_window: 10m
  pause_on_failure: false
notification_policy:
  on_failure: true
env:
  - OPENCTO_RUN_DIR=/tmp/run
  - NEXT_PAGE={{steps.fetch.stdout}}
steps:
  - id: fetch-and-append
    command: python3 src/fetch_stargazers.py
    args: ["python3", "src/fetch_stargazers.py", "--append"]
    start_to_close_timeout: 20m
```

Why this is bad:

- `OPENCTO_*` env names are reserved and must not be set in `env`.
- Template syntax such as `{{steps.fetch.stdout}}` is not supported.
- `command` must be only the executable; arguments belong in `args`.
- `args` repeats the executable.
- One step combines fetch and append, making retry behavior harder to reason about.
- `allow_all` can overlap runs; use it only when the workflow is explicitly safe for concurrent execution.
