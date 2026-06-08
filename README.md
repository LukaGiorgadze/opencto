# OpenCTO - Open Continuous Technical Operator

Self-hosted AI technical operator for your project. Use it from Discord or Telegram.

## Get Started

1. Install Docker Desktop or OrbStack.

2. Install OpenCTO.

```bash
curl -fsSL https://raw.githubusercontent.com/LukaGiorgadze/opencto/main/install.sh | sh
```

3. Configure credentials.

Required:

```bash
OPENAI_API_KEY
```

Discord:

```bash
DISCORD_TOKEN
DISCORD_APPLICATION_ID
```

Telegram:

```bash
TELEGRAM_BOT_TOKEN
TELEGRAM_WEBHOOK_URL
TELEGRAM_WEBHOOK_SECRET
```

4. Start OpenCTO.

```bash
opencto start
opencto doctor
```

Discord: create a bot in the [Discord Developer Portal](https://discord.com/developers/applications), enable `MESSAGE CONTENT INTENT`, and invite it to your server.

Telegram: create a bot with BotFather. For group chats, disable the bot's privacy mode.

## Configuration

Default workspace is `$HOME/.opencto`; set `OPENCTO_WORKSPACE` to use another path.

Credentials live in `$HOME/.opencto/.env`. Runtime config lives in `$HOME/.opencto/config.json`.

`config.json` controls LLM models, Temporal, channels, memory, conversation history, logging, and optional Bifrost.

Uninstall:

```bash
curl -fsSL https://raw.githubusercontent.com/LukaGiorgadze/opencto/main/install.sh | sh -s -- --uninstall
```
