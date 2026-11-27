// Package assets holds the files rox ships inside its binary: the guest scripts, the
// rox:linux Containerfile and the rox skill.
package assets

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

//go:embed Containerfile guest/bootstrap.sh guest/container-entrypoint.sh guest/rix-guest.sh
var files embed.FS

//go:embed all:skills
var skills embed.FS

// Read returns an embedded file, such as "guest/bootstrap.sh".
func Read(name string) []byte {
	data, err := files.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return data
}

// Bootstrap is the guest-side stage script rox pipes over SSH.
func Bootstrap() string { return string(Read("guest/bootstrap.sh")) }

// RixGuest is the guest-side script rix sends through rox exec.
func RixGuest() string { return string(Read("guest/rix-guest.sh")) }

// Toolkit is the io.rox.toolkit label of the rox:linux Containerfile.
func Toolkit() string {
	m := regexp.MustCompile(`(?m)^LABEL io\.rox\.toolkit="([^"]+)"`).FindSubmatch(Read("Containerfile"))
	if m == nil {
		return ""
	}
	return string(m[1])
}

// BuildContext writes the Containerfile and guest scripts to dir, as a container build context.
func BuildContext(dir string) error {
	for _, name := range []string{"Containerfile", "guest/bootstrap.sh", "guest/container-entrypoint.sh"} {
		target := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, Read(name), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Skill is the rox skill's folder.
func Skill() fs.FS {
	sub, err := fs.Sub(skills, "skills/rox")
	if err != nil {
		panic(err)
	}
	return sub
}
