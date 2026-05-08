# Local Development

## OpenAI

OpenCTO uses OpenAI for next-action planning and semantic memory when `[llm].provider = "openai"`.

Recommended local setup:

```bash
export OPENAI_API_KEY="sk-..."
export LITELLM_PROXY_KEY="sk-admin"
task worker
```

`task worker` now runs through `air`, so code changes rebuild and restart automatically.

When using the local LiteLLM proxy, `OPENAI_API_KEY` is the upstream provider key for LiteLLM and `LITELLM_PROXY_KEY` is the key OpenCTO uses to authenticate to LiteLLM. OpenCTO now reads `LITELLM_PROXY_KEY` directly from the environment. A direct `llm.api_key` value is also supported for local-only testing, but environment variables are safer.

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

Approval commands in Discord are plain messages:

```text
approve <approval-id>
reject <approval-id> optional comment
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
