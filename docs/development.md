# Local Development

## OpenAI

OpenCTO uses OpenAI for decisioning and semantic memory when `[llm].provider = "openai"`.

Recommended local setup:

```bash
export OPENAI_API_KEY="sk-..."
export LITELLM_PROXY_KEY="sk-admin"
task worker
```

`task worker` now runs through `air`, so code changes rebuild and restart automatically.

When using the local LiteLLM proxy, `OPENAI_API_KEY` is the upstream provider key for LiteLLM and `LITELLM_PROXY_KEY` is the key OpenCTO uses to authenticate to LiteLLM. The sample config keeps `api_key_env = "LITELLM_PROXY_KEY"`. A direct `llm.api_key` value is also supported for local-only testing, but environment variables or a vault-backed secret are safer.

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

4. Invite the bot to your server with `Send Messages`, `View Channels`, and `Read Message History`.
5. Set `[channels.discord].enabled = true` in `config/config.example.toml` or your local config.
6. Run `task serve`.

`task worker` alone does not start the Discord adapter. Use `task serve` when Discord is enabled.
`task serve` also runs through `air`, so config and Go code changes trigger a rebuild automatically.

Approval commands in Discord are plain messages:

```text
approve <approval-id>
reject <approval-id> optional comment
```

## SQLite And sqlite-vec

SQLite is file-backed and stores data at `data/memory.db`.

`sqlite-vec` is optional today:

- leave `memory.sqlite_vec_path = ""` to run without the extension
- set `memory.sqlite_vec_path` to the installed `vec0`/`sqlite-vec` shared library to load the extension at startup
- set `memory.sqlite_vec_required = true` if startup should fail unless the extension loads

Without `sqlite-vec`, OpenCTO still stores embeddings in SQLite and performs semantic ranking in-process. When the extension is installed and configured, the runtime logs that `sqlite_vec_loaded=true`.

## Vault

The default vault provider now uses [`github.com/zalando/go-keyring`](https://github.com/zalando/go-keyring) behind the existing `vault.Store` interface, so the storage backend can be swapped later without changing callers.
