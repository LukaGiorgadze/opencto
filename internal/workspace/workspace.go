package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, "opencto"), nil
}

func ResolveRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return DefaultRoot()
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	if root == "~" {
		root = home
	} else if strings.HasPrefix(root, "~/") || strings.HasPrefix(root, `~\`) {
		root = filepath.Join(home, root[2:])
	} else {
		root = os.Expand(root, func(key string) string {
			switch key {
			case "HOME", "USERPROFILE":
				return home
			default:
				return os.Getenv(key)
			}
		})
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	return absRoot, nil
}
