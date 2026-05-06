package exec

import (
	_ "embed"
	"encoding/json"
)

const (
	ExecToolName        = "Exec"
	ExecToolDescription = `Execute any action necessary to accomplish the current goal.

**IMPORTANT:** Avoid using this tool to run "find", "grep", "cat", "head", "tail", "sed", "awk", "echo", or "printf" commands, unless explicitly instructed or after you have verified that a dedicated tool cannot accomplish your task. Instead, use the appropriate dedicated tool as this will provide a much better experience for the user:

- File search: Use Glob (NOT find or ls)
- Content search: Use Grep (NOT grep or rg)
- Read files: Use Read (NOT cat/head/tail)
- Edit files: Use Edit (NOT sed/awk)
- Write files: Use Write (NOT echo >/cat <<EOF)
- Communication: Output text directly (NOT echo/printf)

While this tool can do similar things, it's better to use the built-in tools as they provide a better user experience and make it easier to review tool calls and give permission.

This tool dispatches to the most appropriate execution backend for exec commands and system-level operations.
Use it to make concrete, direct progress inside the project workspace ($OPENCTO_WORKSPACE) on the current operating system by whatever means are available.

You are not limited to any single tool type. If something is missing, acquire it. If a step fails, diagnose and adapt. Always choose the most direct path to completing the goal.

## Examples of what you can do:
- Run system commands, scripts, builds, tests, and installs
- Install missing runtimes, CLIs, or libraries on the fly
- Chain multiple actions across different tool types to reach the goal

## Instructions
- If your command will create new directories or files, first use this tool to run "ls/dir" to verify the parent directory exists and is the correct location.
- Always quote file paths that contain spaces with double quotes in your command (e.g., cd "path with spaces/file.txt")
- You may specify an optional timeout in milliseconds (up to 600000ms / 10 minutes).
- You may specify cwd to run the command from a specific directory. Use cwd instead of commands like "cd path && command".
- You must classify how the command should run:
  - run_mode=wait_for_exit for commands that should finish and return an exit code.
  - run_mode=start_background for servers, watchers, dev processes, daemons, or commands such as "pnpm run dev" that are expected to keep running.
- You must classify idempotency:
  - read_only for inspection commands.
  - idempotent for commands that are safe to repeat.
  - non_idempotent for one-shot or risky mutations.
  - unknown when you cannot tell.
- For start_background, set process_scope to the intended owner lifetime:
  - stop_on_finish means OpenCTO starts the process in the background, then stops it when the task finishes. Do not use it when the user should be able to access the app, server, or watcher after your response.
  - project means the process belongs to the project and remains running until an explicit stop; use it when the user should be able to access the app, server, or watcher after your response.
- DO NOT use newlines to separate commands (newlines are ok in quoted strings).
- Avoid unnecessary "sleep" commands:
- Do not sleep between commands that can run immediately — just run them.
- Do not retry failing commands in a sleep loop — diagnose the root cause.
- If you must poll an external process, use a check command rather than sleeping first.
- If you must sleep, keep the duration short to avoid blocking the user.

**IMPORTANT:** Do not stop because a specific tool or dependency is absent — find another path using exec commands or acquire what is needed.`
)

//go:embed schema.json
var execToolSchema json.RawMessage

func ExecToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), execToolSchema...)
}
