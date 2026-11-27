package rux

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/rex-agents/r-x/internal/selfupdate"
)

// Guests run the rux for their OS and architecture as the runner: Linux for containers and
// Linux VMs, macOS for macOS VMs. The server serves, in order: this binary when the guest
// matches the host, the --runner-bin override (Linux only), rux-OS-ARCH beside this binary
// (development builds), or rux from this version's release, downloaded once, checked
// against the release's checksums and cached in the data directory.

var runnerMu sync.Mutex

// guestArch maps uname -m or GOARCH to the release's architecture names.
func guestArch(s string) string {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "x86_64", "amd64":
		return "amd64"
	default:
		return "arm64"
	}
}

// guestOS maps uname -s or GOOS to the release's OS names.
func guestOS(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "darwin") {
		return "darwin"
	}
	return "linux"
}

func (s *server) runnerCandidates(goos, arch string) []string {
	if goos == runtime.GOOS && arch == runtime.GOARCH {
		return []string{s.selfBin}
	}
	if s.runnerBin != "" && goos == "linux" {
		return []string{s.runnerBin}
	}
	name := "rux-" + goos + "-" + arch
	return []string{
		filepath.Join(filepath.Dir(s.selfBin), name),
		filepath.Join(s.dataDir, "runner", Version, name),
	}
}

// runnerFor is the runner binary for goos and arch, fetching it from the release when needed.
func (s *server) runnerFor(goos, arch string) (string, error) {
	if s.backend == "local" {
		return s.selfBin, nil
	}
	candidates := s.runnerCandidates(goos, arch)
	for _, p := range candidates {
		if isExecutable(p) {
			return p, nil
		}
	}
	if len(candidates) == 1 {
		return "", fmt.Errorf("the runner binary %s doesn't exist", candidates[0])
	}
	runnerMu.Lock()
	defer runnerMu.Unlock()
	cached := candidates[len(candidates)-1]
	if isExecutable(cached) {
		return cached, nil
	}
	if err := selfupdate.FetchBinary(Version, goos, arch, "rux", cached); err != nil {
		return "", fmt.Errorf("no rux for %s/%s guests (%v). For a development build, put one beside rux: "+
			"GOOS=%s GOARCH=%s CGO_ENABLED=0 go build -o %s ./cmd/rux", goos, arch, err, goos, arch, candidates[0])
	}
	return cached, nil
}

var (
	versionMu    sync.Mutex
	versionCache = map[string]string{}
)

// runnerVersions are the hashes of the runner binaries this server serves now. A runner
// reporting another one is upgraded.
func (s *server) runnerVersions() map[string]bool {
	out := map[string]bool{}
	if s.backend == "local" {
		out[fileVersion(s.selfBin)] = true
		return out
	}
	versionMu.Lock()
	defer versionMu.Unlock()
	for _, target := range [][2]string{{"linux", "arm64"}, {"linux", "amd64"}, {"darwin", "arm64"}, {"darwin", "amd64"}} {
		for _, p := range s.runnerCandidates(target[0], target[1]) {
			info, err := os.Stat(p)
			if err != nil {
				continue
			}
			key := fmt.Sprintf("%s@%d@%d", p, info.Size(), info.ModTime().UnixNano())
			if _, ok := versionCache[key]; !ok {
				versionCache[key] = fileVersion(p)
			}
			if v := versionCache[key]; v != "" {
				out[v] = true
			}
			break
		}
	}
	return out
}
