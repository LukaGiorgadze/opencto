# OpenCTO

OpenCTO is an open-source AI technical operator that helps launch, configure, deploy, and maintain software products.

While AI coding tools have made writing code easier, shipping and operating software still requires many manual steps: selecting the right services and platforms, deploying applications, configuring domains and DNS, setting up analytics and monitoring, publishing mobile apps, managing environments, and connecting dozens of tools.

OpenCTO automates these operational workflows. It can use coding agents when code changes are needed, but its primary goal is to handle the technical work around getting products live and keeping them running.

Built on durable workflows, OpenCTO can plan tasks, execute multi-step operations, interact with developer tools and cloud services, monitor progress, request approvals for sensitive actions, and report results back to channels like Discord or Telegram.

The goal is simple: spend less time operating technology and more time building, marketing, and growing your product.

## Get Started

1. Install Docker Desktop or OrbStack.

2. Install OpenCTO.

```bash
curl -fsSL https://raw.githubusercontent.com/LukaGiorgadze/opencto/main/install.sh | sh
```

3. Configure credentials.

```bash
OPENAI_API_KEY
```

**Discord:**

```bash
DISCORD_TOKEN
DISCORD_APPLICATION_ID
```

Create Discord bot in the [Discord Developer Portal](https://discord.com/developers/applications), enable `MESSAGE CONTENT INTENT`, and invite it to your server.

**Telegram:**

```bash
TELEGRAM_BOT_TOKEN
TELEGRAM_WEBHOOK_URL
TELEGRAM_WEBHOOK_SECRET
```

Create Telegram bot with [BotFather](https://telegram.me/BotFather). For group chats, disable the bot's privacy mode.

4. Start OpenCTO.

```bash
opencto start
opencto doctor # check if all good
opencto config # open configuration file
```

## Configuration

Default workspace is `$HOME/.opencto`; set `OPENCTO_WORKSPACE` to use another path.

Credentials live in `$HOME/.opencto/.env`. Runtime config lives in `$HOME/.opencto/config.json`.

`config.json` controls LLM models, Temporal, channels, memory, conversation history, logging, and optional Bifrost.

Uninstall:

```bash
curl -fsSL https://raw.githubusercontent.com/LukaGiorgadze/opencto/main/install.sh | sh -s -- --uninstall
```
