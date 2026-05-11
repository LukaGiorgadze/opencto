# OpenCTO

OpenCTO is a self-hosted AI technical operator for founders and indie hackers.

It helps plan, build, test, ship, maintain, and monitor software projects through a conversational interface. The goal is to feel less like a code generator and more like a practical technical co-founder: autonomous when the path is clear, careful when risk is high, and willing to ask for clarification when needed.

## What It Does

OpenCTO is designed to help users and teams move software projects forward by:

- understanding requests in project context
- deciding what should happen next
- inspecting, modifying, testing, and operating software
- asking for approval before risky actions
- reporting progress and results clearly

The focus is ownership of software delivery, not just producing code.

## Principles

OpenCTO is built around a few product and engineering principles:

- keep the system simple and understandable
- preserve clear boundaries between responsibilities
- make workflows predictable and auditable
- treat safety as part of the product
- verify work before reporting success
- store only meaningful project state
- keep configuration explicit
- avoid hidden behavior and unnecessary complexity

## How It Should Behave

OpenCTO should act with practical judgment.

When the next step is clear, it should move autonomously. When context is missing, assumptions are risky, or an action could have meaningful consequences, it should stop and ask. When it completes work, it should verify the result before claiming success.

## Project Status

OpenCTO is under active development. The product shape, internals, and workflows are expected to evolve as the system becomes more reliable, safer, and more useful in real projects.

## Contributing

Contributions should favor maintainability over cleverness.

Good changes are simple, explicit, tested where behavior matters, and easy for future contributors to understand. Avoid large rewrites unless they clearly reduce complexity or improve reliability.

# Local Development

## OpenAI

OpenCTO uses OpenAI for next-action decisions and semantic memory when `[llm].provider = "openai"`.

Recommended local setup:

```bash
export OPENAI_API_KEY="sk-..."
task worker
```

`task worker` now runs through `air`, so code changes rebuild and restart automatically.

By default, OpenCTO connects directly to the OpenAI API using `llm.base_url` and reads `OPENAI_API_KEY` from the environment. A direct `llm.api_key` value is also supported for local-only testing, but environment variables are safer.

The local Bifrost gateway is optional. To use it, set:

```json
{
  "llm": {
    "base_url": "http://127.0.0.1:4000/openai",
    "bifrost": {
      "enabled": true
    }
  }
}
```

Then start infrastructure with the Bifrost profile and export both keys. The gateway reads its provider and virtual-key settings from `bifrost.json`.

```bash
export OPENAI_API_KEY="sk-..."
export BIFROST_API_KEY="sk-bf-opencto-local"
docker compose --profile bifrost up -d --remove-orphans
task worker
```

In that mode, `OPENAI_API_KEY` is the upstream provider key for Bifrost and `BIFROST_API_KEY` is the virtual key OpenCTO uses to authenticate to Bifrost.

## Discord

To connect the runtime to Discord:

1. Create a Discord application and bot in the [Discord Developer Portal](https://discord.com/developers/applications).
2. Under `Bot`, enable:
   - `MESSAGE CONTENT INTENT`
   - `SERVER MEMBERS INTENT` is not required
   - `PRESENCE INTENT` is not required
3. Copy the bot token and export it locally:

```bash
export DISCORD_TOKEN="..."
export DISCORD_APPLICATION_ID="..."
```

4. Invite the bot to your server with `Send Messages`, `Attach Files`, `View Channels`, and `Read Message History`.
5. Set `"channels.discord.enabled": true` in `config.json` or your local config.
6. Run `task serve`.

`task worker` alone does not start the Discord adapter. Use `task serve` when Discord is enabled.
`task serve` also runs through `air`, so config and Go code changes trigger a rebuild automatically.

Outbound Discord attachment limits are configured under the Discord channel config:

```json
{
  "channels": {
    "discord": {
      "outbound_attachments": {
        "max_files": 10,
        "max_file_bytes": 10485760,
        "max_total_bytes": 26214400
      }
    }
  }
}
```

## Skills

OpenCTO discovers top-level Agent Skills from these roots, in precedence order:

1. `$OPENCTO_WORKSPACE/skills`
2. `$OPENCTO_WORKSPACE/.agents/skills`
3. `$OPENCTO_ROOT/skills`

Each skill is a directory containing `SKILL.md`. Workspace skills are user-defined and may shadow built-in skills with the same ID. Keep supporting files under the skill directory, such as `references/`, `scripts/`, or `assets/`, and refer to them relative to the skill directory.

`LoadSkill` only loads advertised top-level skill IDs. If a loaded skill points to supporting files, use the file tools to read those paths directly.

## Persistence

OpenCTO uses SQLite for local runtime storage.

The default database path is:

```text
$OPENCTO_WORKSPACE/db/opencto.db
```

With the default local config, `$OPENCTO_WORKSPACE` resolves to `$HOME/.opencto`, so the database is created at:

```text
$HOME/.opencto/db/opencto.db
```

`task worker` opens the SQLite store, creates the database directory, runs migrations, and ensures the hardcoded development project exists.

`task serve` verifies that migrations have already been applied and fails fast if the schema is missing or behind. Run `task worker` once after pulling storage changes before running `task serve`.

Storage and memory are configured with small top-level switches:

```json
{
  "storage": {
    "provider": "sqlite"
  },
  "memory": {
    "enabled": true,
    "auto_context_limit": 5
  }
}
```

Memory uses the same SQLite database. The first implementation uses SQLite FTS keyword search and recent/pinned fallback context; embeddings and vector search are intentionally deferred.


## License

License information will be added as the project matures.
