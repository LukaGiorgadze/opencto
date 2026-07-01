package opencto

import (
	"embed"
	"io/fs"
)

//go:embed skills config.json .env.example assets
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

func DefaultEnvExample() []byte {
	data, err := embeddedFS.ReadFile(".env.example")
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), data...)
}

func EmbeddedAsset(path string) ([]byte, error) {
	data, err := embeddedFS.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), data...), nil
}
