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

## Persistence

Persistence is not implemented right now. The runtime starts without a database backend until the next storage layer is introduced.
