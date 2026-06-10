package main

import (
	"bufio"
	"encoding/json"
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
	configPath := filepath.Join(workspaceRoot, "config.json")
	if err := ensureDefaultDotEnvValues(envPath); err != nil {
		return err
	}
	envValues, err := readDotEnvValues(envPath)
	if err != nil {
		return err
	}
	interfaceChoice, err := readConfiguredInterface(configPath)
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

	channel, err := promptChoice(reader, stdout, "Interface", []string{"discord", "telegram", "none"}, interfaceChoice)
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
	if err := writeConfiguredInterface(configPath, channel); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "\nConfigured %s\n", workspaceRoot)
	return nil
}

func readConfiguredInterface(path string) (string, error) {
	doc, err := readConfigDocument(path)
	if err != nil {
		return "", err
	}
	channels, _ := doc["channels"].(map[string]any)
	if channelEnabled(channels, "discord") {
		return "discord", nil
	}
	if channelEnabled(channels, "telegram") {
		return "telegram", nil
	}
	return "none", nil
}

func writeConfiguredInterface(path, choice string) error {
	doc, err := readConfigDocument(path)
	if err != nil {
		return err
	}
	channels := ensureJSONObject(doc, "channels")
	discord := ensureJSONObject(channels, "discord")
	telegram := ensureJSONObject(channels, "telegram")

	discordEnabled := choice == "discord"
	telegramEnabled := choice == "telegram"
	if jsonBool(discord["enabled"]) == discordEnabled && jsonBool(telegram["enabled"]) == telegramEnabled {
		return nil
	}
	discord["enabled"] = discordEnabled
	telegram["enabled"] = telegramEnabled

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	return os.WriteFile(path, append(data, '\n'), mode)
}

func readConfigDocument(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func channelEnabled(channels map[string]any, name string) bool {
	if channels == nil {
		return false
	}
	channel, _ := channels[name].(map[string]any)
	return jsonBool(channel["enabled"])
}

func ensureJSONObject(parent map[string]any, key string) map[string]any {
	if value, ok := parent[key].(map[string]any); ok {
		return value
	}
	value := map[string]any{}
	parent[key] = value
	return value
}

func jsonBool(value any) bool {
	enabled, _ := value.(bool)
	return enabled
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

func ensureDefaultDotEnvValues(path string) error {
	current, err := readDotEnvValues(path)
	if err != nil {
		return err
	}
	defaults, err := dotEnvDefaults()
	if err != nil {
		return err
	}

	var missing []string
	for _, item := range defaults {
		if _, ok := current[item.name]; !ok {
			missing = append(missing, item.name+"="+dotEnvValue(item.value))
		}
	}
	if len(missing) == 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	text := strings.TrimRight(string(data), "\n")
	if text != "" {
		text += "\n"
	}
	text += strings.Join(missing, "\n") + "\n"
	return os.WriteFile(path, []byte(text), 0o600)
}

type dotEnvDefault struct {
	name  string
	value string
}

func dotEnvDefaults() ([]dotEnvDefault, error) {
	var defaults []dotEnvDefault
	for _, line := range strings.Split(defaultEnvFile(), "\n") {
		name, value, ok := parseDotEnvLine(line)
		if ok {
			defaults = append(defaults, dotEnvDefault{name: name, value: value})
		}
	}
	return defaults, nil
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
