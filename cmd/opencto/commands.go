package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

type commandHelp struct {
	Summary     string
	Usage       []string
	Description []string
	Options     []string
	Examples    []string
}

var commandHelpByName = map[string]commandHelp{
	"start": {
		Summary: "initialize workspace state and start the runtime",
		Usage:   []string{"opencto start"},
		Description: []string{
			"Creates starter files on first run, initializes local runtime state, runs database migrations, and starts the runtime.",
			"By default, the workspace is $HOME/.opencto. Set OPENCTO_WORKSPACE to override it.",
		},
		Options: []string{
			"-h, --help    show this help",
		},
		Examples: []string{
			"opencto start",
			"OPENCTO_WORKSPACE=$HOME/.opencto opencto start",
		},
	},
	"doctor": {
		Summary: "check local configuration and runtime dependencies",
		Usage:   []string{"opencto doctor"},
		Description: []string{
			"Loads config and .env, then checks local tools, writable runtime directories, SQLite schema state, Discord environment, and Temporal connectivity.",
			"By default, the workspace is $HOME/.opencto. Set OPENCTO_WORKSPACE to override it.",
		},
		Options: []string{
			"-h, --help    show this help",
		},
		Examples: []string{
			"opencto doctor",
			"OPENCTO_WORKSPACE=$HOME/.opencto opencto doctor",
		},
	},
	"configure": {
		Summary: "configure workspace credentials",
		Usage:   []string{"opencto configure"},
		Description: []string{
			"Creates starter files if needed, writes local secrets to .env, and updates the enabled channel in config.json.",
			"By default, the workspace is $HOME/.opencto. Set OPENCTO_WORKSPACE to override it.",
		},
		Options: []string{
			"-h, --help    show this help",
		},
		Examples: []string{
			"opencto configure",
			"OPENCTO_WORKSPACE=$HOME/.opencto opencto configure",
		},
	},
	"config": {
		Summary: "open config.json",
		Usage:   []string{"opencto config"},
		Description: []string{
			"Opens the workspace config.json with the default app.",
			"By default, the workspace is $HOME/.opencto. Set OPENCTO_WORKSPACE to override it.",
		},
		Options: []string{
			"-h, --help    show this help",
		},
		Examples: []string{
			"opencto config",
			"OPENCTO_WORKSPACE=$HOME/.opencto opencto config",
		},
	},
	"inject": {
		Summary: "inject a local event",
		Usage:   []string{`opencto inject -body "message" [-actor "name"]`},
		Description: []string{
			"Injects a local test event into the runtime through Temporal.",
			"Use this when the runtime is already available and you want to simulate an incoming local channel message.",
		},
		Options: []string{
			"-body string     event body to inject; required",
			"-actor string    actor name for the event; defaults to local-user",
			"-h, --help       show this help",
		},
		Examples: []string{
			`opencto inject -body "summarize the workspace"`,
			`opencto inject -actor "luka" -body "run doctor and report back"`,
		},
	},
	"report": {
		Summary: "send a one-shot channel report",
		Usage: []string{
			`opencto report -channel_type discord -channel_id "123" "message"`,
			`opencto report -channel_type telegram -channel_id "-100123" -thread_id "42" "message"`,
			`opencto report -channel_type discord -channel_id "123" -file path/to/file.png`,
		},
		Description: []string{
			"Sends a one-shot report to a configured channel without creating a workflow event.",
			"The report needs a channel type, a channel ID, and either message text or at least one file attachment.",
		},
		Options: []string{
			"-channel_type string    channel type, for example discord or telegram",
			"-channel_id string      target channel ID; required",
			"-thread_id string       optional target thread/topic ID",
			"-file path              attach a file; repeatable",
			"-h, --help              show this help",
		},
		Examples: []string{
			`opencto report -channel_type discord -channel_id "123" "deploy finished"`,
			`opencto report -channel_type telegram -channel_id "-100123" "deploy finished"`,
			`opencto report -channel_type discord -channel_id "123" -file ./report.txt`,
		},
	},
}

func parseNoArgCommand(command string, args []string) error {
	flags := newCommandFlagSet(command)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("%s: unexpected argument %q", command, flags.Arg(0))
	}
	return nil
}

func newCommandFlagSet(command string) *flag.FlagSet {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func requireDevCommand(command string) error {
	if _, ok, err := devRepoRoot(); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s is only available in OpenCTO dev mode; use opencto start", command)
	}
	return nil
}

func commandHelpRequested(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help")
}

func writeCommandHelp(out io.Writer, command string) bool {
	help, ok := commandHelpByName[command]
	if !ok {
		return false
	}
	fmt.Fprintf(out, "Usage: %s\n", strings.Join(help.Usage, "\n       "))
	if help.Summary != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, help.Summary)
	}
	writeHelpSection(out, "Description:", help.Description)
	writeHelpSection(out, "Options:", help.Options)
	writeHelpSection(out, "Examples:", help.Examples)
	return true
}

func writeHelpSection(out io.Writer, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, title)
	for _, line := range lines {
		fmt.Fprintf(out, "  %s\n", line)
	}
}

func commandUsageError(out io.Writer, command, message string) error {
	if out != nil {
		_ = writeCommandHelp(out, command)
	}
	return fmt.Errorf("%s: %s", command, message)
}
