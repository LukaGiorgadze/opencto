package shell

import (
	_ "embed"
	"encoding/json"
)

const (
	ShellToolName        = "Command"
	ShellToolDescription = `Execute any action necessary to accomplish the current goal.

    **IMPORTANT:** Avoid using this tool to run "find", "grep", "cat", "head", "tail", "sed", "awk", "echo", or "printf" commands, unless explicitly instructed or after you have verified that a dedicated tool cannot accomplish your task. Instead, use the appropriate dedicated tool as this will provide a much better experience for the user:

    - File search: Use Glob (NOT find or ls)
    - Content search: Use Grep (NOT grep or rg)
    - Read files: Use Read (NOT cat/head/tail)
    - Edit files: Use Edit (NOT sed/awk)
    - Write files: Use Write (NOT echo >/cat <<EOF)
    - Communication: Output text directly (NOT echo/printf)

    While this tool can do similar things, it's better to use the built-in tools as they provide a better user experience and make it easier to review tool calls and give permission.

    This tool dispatches to the most appropriate execution backend for shell commands and system-level operations.
    Use it to make concrete, direct progress inside the project workspace on the current operating system by whatever means are available.

    You are not limited to any single tool type. If something is missing, acquire it. If a step fails, diagnose and adapt. Always choose the most direct path to completing the goal.

    ## Examples of what you can do:
    - Run system commands, scripts, builds, tests, and installs
    - Install missing runtimes, CLIs, or libraries on the fly
    - Chain multiple actions across different tool types to reach the goal

    ## Instructions
    - If your command will create new directories or files, first use this tool to run "ls/dir" to verify the parent directory exists and is the correct location.
    - Always quote file paths that contain spaces with double quotes in your command (e.g., cd "path with spaces/file.txt")
    - You may specify an optional timeout in milliseconds (up to 600000ms / 10 minutes).
    - DO NOT use newlines to separate commands (newlines are ok in quoted strings).
    - Avoid unnecessary "sleep" commands:
    - Do not sleep between commands that can run immediately — just run them.
    - Do not retry failing commands in a sleep loop — diagnose the root cause.
    - If you must poll an external process, use a check command rather than sleeping first.
    - If you must sleep, keep the duration short to avoid blocking the user.

    **IMPORTANT:** Do not stop because a specific tool or dependency is absent — find another path using shell commands or acquire what is needed.`
)

//go:embed schema.json
var shellToolSchema json.RawMessage

func ShellToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), shellToolSchema...)
}
