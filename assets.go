package opencto

import (
	"embed"
	"io/fs"
)

//go:embed skills
var embeddedFS embed.FS

func SkillsFS() fs.FS {
	skillsFS, err := fs.Sub(embeddedFS, "skills")
	if err != nil {
		panic(err)
	}
	return skillsFS
}
