package schedule

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func ResolveHostTimeZone() (*time.Location, string, error) {
	if name := strings.TrimSpace(os.Getenv("TZ")); name != "" && name != "Local" {
		location, err := time.LoadLocation(name)
		if err != nil {
			return nil, "", fmt.Errorf("load TZ %q: %w", name, err)
		}
		return location, name, nil
	}

	if location := time.Now().Location(); location != nil {
		name := strings.TrimSpace(location.String())
		if name != "" && name != "Local" {
			if loaded, err := time.LoadLocation(name); err == nil {
				return loaded, name, nil
			}
		}
	}

	for _, path := range []string{"/etc/localtime", "/var/db/timezone/localtime"} {
		name := zoneNameFromSymlink(path)
		if name == "" {
			continue
		}
		location, err := time.LoadLocation(name)
		if err != nil {
			return nil, "", fmt.Errorf("load host timezone %q: %w", name, err)
		}
		return location, name, nil
	}

	return nil, "", fmt.Errorf("host IANA timezone could not be resolved")
}

func zoneNameFromSymlink(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	target = filepath.ToSlash(target)
	for _, marker := range []string{"/zoneinfo/", "/zoneinfo.default/"} {
		index := strings.Index(target, marker)
		if index < 0 {
			continue
		}
		name := strings.Trim(target[index+len(marker):], "/")
		if validZoneName(name) {
			return name
		}
	}
	return ""
}

func validZoneName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && !strings.Contains(name, "..") && !strings.HasPrefix(name, "/")
}
