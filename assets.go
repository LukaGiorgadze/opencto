package opencto

import (
	"embed"
	"io/fs"
)

//go:embed skills config.json
var embeddedFS embed.FS

func SkillsFS() fs.FS {
	skillsFS, err := fs.Sub(embeddedFS, "skills")
	if err != nil {
		panic(err)
	}
	return skillsFS
}

func DefaultConfigJSON() []byte {
	data, err := embeddedFS.ReadFile("config.json")
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), data...)
}
