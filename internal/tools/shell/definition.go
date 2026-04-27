package shell

import (
	_ "embed"
	"encoding/json"
)

const (
	SelectorToolName        = "Command"
	SelectorToolDescription = `Execute any action necessary to accomplish the current goal.

    This tool dispatches to the most appropriate execution backend — shell commands, HTTP requests, browser automation, file operations, or any other available capability.
    Use it to make concrete, direct progress by whatever means are available.
    
    You are not limited to any single tool type. If something is missing, acquire it. If a step fails, diagnose and adapt. Always choose the most direct path to completing the goal.
    
    Examples of what you can do:
    - Run system commands, scripts, builds, tests, and installs
    - Make HTTP requests to APIs or download remote resources
    - Interact with a browser to scrape, test, or automate web flows
    - Read, write, move, or delete files anywhere in the workspace
    - Install missing runtimes, CLIs, or libraries on the fly
    - Chain multiple actions across different tool types to reach the goal
    
    Do not stop because a specific tool or dependency is absent — find another path or acquire what is needed.`
)

//go:embed selector_parameters.json
var selectorParameters json.RawMessage

func SelectorToolParameters() json.RawMessage {
	return append(json.RawMessage(nil), selectorParameters...)
}
