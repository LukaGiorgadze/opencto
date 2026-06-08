package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func runConfigureCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if commandHelpRequested(args) {
		writeCommandHelp(stdout, "configure")
		return nil
	}
	if err := parseNoArgCommand("configure", args); err != nil {
		return commandUsageError(stderr, "configure", err.Error())
	}
	workspaceRoot, err := defaultWorkspaceRoot()
	if err != nil {
		return err
	}
	return runConfigure(workspaceRoot, stdin, stdout)
}

func runConfigure(workspaceRoot string, stdin io.Reader, stdout io.Writer) error {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if _, err := ensureStarterFiles(workspaceRoot); err != nil {
		return err
	}

	envPath := filepath.Join(workspaceRoot, ".env")
	envValues, err := readDotEnvValues(envPath)
	if err != nil {
		return err
	}

	reader := bufio.NewReader(stdin)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Configure OpenCTO")

	openAIKey, err := promptValue(reader, stdout, "OpenAI API key", envValues["OPENAI_API_KEY"], true)
	if err != nil {
		return err
	}
	envUpdates := map[string]string{}
	if openAIKey != "" {
		envUpdates["OPENAI_API_KEY"] = openAIKey
	}

	channel, err := promptChoice(reader, stdout, "Interface", []string{"discord", "telegram", "none"}, "discord")
	if err != nil {
		return err
	}

	switch channel {
	case "discord":
		token, err := promptValue(reader, stdout, "Discord bot token", envValues["DISCORD_TOKEN"], true)
		if err != nil {
			return err
		}
		applicationID, err := promptValue(reader, stdout, "Discord application ID (optional)", envValues["DISCORD_APPLICATION_ID"], false)
		if err != nil {
			return err
		}
		if token != "" {
			envUpdates["DISCORD_TOKEN"] = token
		}
		if applicationID != "" {
			envUpdates["DISCORD_APPLICATION_ID"] = applicationID
		}
	case "telegram":
		token, err := promptValue(reader, stdout, "Telegram bot token", envValues["TELEGRAM_BOT_TOKEN"], true)
		if err != nil {
			return err
		}
		webhookURL, err := promptValue(reader, stdout, "Telegram webhook URL", envValues["TELEGRAM_WEBHOOK_URL"], false)
		if err != nil {
			return err
		}
		secret, err := promptValue(reader, stdout, "Telegram webhook secret", envValues["TELEGRAM_WEBHOOK_SECRET"], true)
		if err != nil {
			return err
		}
		if token != "" {
			envUpdates["TELEGRAM_BOT_TOKEN"] = token
		}
		if webhookURL != "" {
			envUpdates["TELEGRAM_WEBHOOK_URL"] = webhookURL
		}
		if secret != "" {
			envUpdates["TELEGRAM_WEBHOOK_SECRET"] = secret
		}
	}

	if err := writeDotEnvValues(envPath, envUpdates); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "\nConfigured %s\n", workspaceRoot)
	return nil
}

func promptValue(reader *bufio.Reader, out io.Writer, label, current string, secret bool) (string, error) {
	current = strings.TrimSpace(current)
	if secret && current != "" {
		fmt.Fprintf(out, "%s [set]: ", label)
	} else if current != "" {
		fmt.Fprintf(out, "%s [%s]: ", label, current)
	} else {
		fmt.Fprintf(out, "%s: ", label)
	}
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	if err == io.EOF && strings.TrimSpace(line) == "" {
		return current, nil
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return current, nil
	}
	return value, nil
}

func promptChoice(reader *bufio.Reader, out io.Writer, label string, choices []string, current string) (string, error) {
	current = strings.ToLower(strings.TrimSpace(current))
	if current == "" {
		current = choices[0]
	}
	for {
		fmt.Fprintf(out, "%s (%s) [%s]: ", label, strings.Join(choices, "/"), current)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		value := strings.ToLower(strings.TrimSpace(line))
		if value == "" {
			value = current
		}
		for _, choice := range choices {
			if value == choice {
				return value, nil
			}
		}
		fmt.Fprintf(out, "Choose one of: %s\n", strings.Join(choices, ", "))
		if err == io.EOF {
			return "", fmt.Errorf("invalid %s: %s", strings.ToLower(label), value)
		}
	}
}

func readDotEnvValues(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := parseDotEnvLine(line)
		if ok {
			values[name] = value
		}
	}
	return values, nil
}

func writeDotEnvValues(path string, updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		name, _, ok := parseDotEnvLine(line)
		if !ok {
			continue
		}
		value, exists := updates[name]
		if !exists {
			continue
		}
		lines[i] = name + "=" + dotEnvValue(value)
		seen[name] = true
	}
	for name, value := range updates {
		if !seen[name] {
			lines = append(lines, name+"="+dotEnvValue(value))
		}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func dotEnvValue(value string) string {
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " \t#'\"\\") {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
	}
	return value
}
