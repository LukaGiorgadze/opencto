# Deployment

`compose.yaml` brings up the local development infrastructure OpenCTO depends on:

- PostgreSQL for Temporal persistence
- Temporal server
- Temporal schema setup and namespace bootstrap jobs
- Temporal UI

## Usage

```bash
docker compose up -d
```

Temporal UI will be available at `http://localhost:8080`, and Temporal gRPC will be available at `localhost:7233`.

If you previously started the stack with an older PostgreSQL volume layout and PostgreSQL fails to become healthy after upgrading images, remove the old volume once and recreate the stack:

```bash
task infra:reset
task infra:up
```

## Task Shortcuts

If you use [`task`](https://taskfile.dev/), the repository now includes a root `Taskfile.yml` with the common development entrypoints:

```bash
task infra:up
task serve
task inject BODY="review the current project state"
task test
```

`task serve` runs `cmd/opencto` in `serve` mode, so it still expects Temporal to be available at the configured host and port.
