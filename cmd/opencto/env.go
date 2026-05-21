package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/observability"
)

type commandEnvironment struct {
	OpenCTORoot string
	ConfigPath  string
	Config      config.Config
	Logger      *slog.Logger
}

func loadCommandEnvironment(configPath string, logOut io.Writer) (commandEnvironment, error) {
	openCTORoot, err := resolveOpenCTORoot()
	if err != nil {
		return commandEnvironment{}, err
	}
	if err := loadOpenCTORootDotEnv(openCTORoot); err != nil {
		return commandEnvironment{}, err
	}
	return loadCommandEnvironmentWithRoot(openCTORoot, configPath, logOut)
}

func loadCommandEnvironmentWithRoot(openCTORoot, configPath string, logOut io.Writer) (commandEnvironment, error) {
	resolvedConfigPath, cfg, err := loadConfig(configPath, openCTORoot)
	if err != nil {
		return commandEnvironment{}, err
	}
	return commandEnvironment{
		OpenCTORoot: openCTORoot,
		ConfigPath:  resolvedConfigPath,
		Config:      cfg,
		Logger:      observability.NewLogger(cfg.Observability.LogLevel, cfg.Observability.JSONLogs, logOut),
	}, nil
}

func loadConfig(configPath, openCTORoot string) (string, config.Config, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		var err error
		configPath, err = resolveDefaultConfigPath(openCTORoot)
		if err != nil {
			return "", config.Config{}, err
		}
	}

	resolvedConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return "", config.Config{}, fmt.Errorf("resolve config path: %w", err)
	}
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		return "", config.Config{}, fmt.Errorf("load config %s: %w", resolvedConfigPath, err)
	}
	return resolvedConfigPath, cfg, nil
}

func resolveDefaultConfigPath(openCTORoot string) (string, error) {
	if path, ok, err := readInstalledOpenCTOConfigPath(); err != nil {
		return "", err
	} else if ok {
		return path, nil
	}
	openCTORoot = strings.TrimSpace(openCTORoot)
	if openCTORoot == "" {
		return "", fmt.Errorf("OpenCTO root is required")
	}
	return filepath.Join(openCTORoot, "config.json"), nil
}

func resolveOpenCTORoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv("OPENCTO_ROOT")); root != "" {
		return filepath.Clean(root), nil
	}
	if root, ok, err := readInstalledOpenCTORootPath(); err != nil {
		return "", err
	} else if ok {
		return root, nil
	}

	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), nil
}

func readInstalledOpenCTOConfigPath() (string, bool, error) {
	return readInstalledPathMarker(installedOpenCTOConfigFilename, "OpenCTO config")
}

func readInstalledOpenCTORootPath() (string, bool, error) {
	return readInstalledPathMarker(installedOpenCTORootFilename, "OpenCTO root")
}

func readInstalledPathMarker(filename, label string) (string, bool, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", false, nil
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(executable), filename))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s marker: %w", label, err)
	}
	path := strings.TrimSpace(string(data))
	if path == "" {
		return "", false, fmt.Errorf("%s marker is empty", label)
	}
	return filepath.Clean(path), true, nil
}

func loadOpenCTORootDotEnv(openCTORoot string) error {
	openCTORoot = strings.TrimSpace(openCTORoot)
	if openCTORoot == "" {
		return nil
	}
	file, err := os.Open(filepath.Join(openCTORoot, ".env"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open OpenCTO .env: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, value, ok := parseDotEnvLine(scanner.Text())
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set env %s from OpenCTO .env: %w", name, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read OpenCTO .env: %w", err)
	}
	return nil
}

func parseDotEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	name, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		quote := value[0]
		if (quote == '"' || quote == '\'') && value[len(value)-1] == quote {
			value = value[1 : len(value)-1]
		}
	}
	return name, value, true
}
