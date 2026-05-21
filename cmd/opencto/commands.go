package main

import (
	"flag"
	"fmt"
	"io"
)

func parseConfigOnlyCommand(command string, args []string) (string, error) {
	flags := newCommandFlagSet(command)
	configPath := flags.String("config", "", "path to config file")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() > 0 {
		return "", fmt.Errorf("%s: unexpected argument %q", command, flags.Arg(0))
	}
	return *configPath, nil
}

func newCommandFlagSet(command string) *flag.FlagSet {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}
