package rox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/assets"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// imageInfo is an image's id and labels, or nil when the engine doesn't have it.
type imageInfo struct {
	ID     string
	Labels map[string]string
}

func inspectImage(engine, image string) *imageInfo {
	result, err := sys.Try(sys.Cmd{Argv: []string{engine, "image", "inspect", image}, Timeout: 20 * time.Second})
	if err != nil || result.Code != 0 {
		return nil
	}
	var raw any
	if json.Unmarshal([]byte(result.Stdout), &raw) != nil {
		return nil
	}
	if list, ok := raw.([]any); ok && len(list) > 0 {
		raw = list[0]
	}
	data, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	labels := map[string]string{}
	found := findLabels(data)
	if config, ok := data["Config"].(map[string]any); ok {
		if l, ok := config["Labels"].(map[string]any); ok {
			found = l
		}
	}
	for k, v := range found {
		labels[k] = fmt.Sprint(v)
	}
	id, _ := data["Id"].(string)
	if id == "" {
		if index, ok := data["index"].(map[string]any); ok {
			id, _ = index["digest"].(string)
		}
	}
	if id == "" {
		id, _ = data["digest"].(string)
	}
	if id == "" {
		sum := sha256.Sum256([]byte(result.Stdout))
		id = "sha256:" + hex.EncodeToString(sum[:])
	}
	return &imageInfo{ID: id, Labels: labels}
}

// findLabels digs out labels from Apple's container, which nests the OCI config in each platform variant.
func findLabels(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		if l, ok := x["Labels"].(map[string]any); ok {
			return l
		}
		for _, key := range sortedKeys(x) {
			if l := findLabels(x[key]); len(l) > 0 {
				return l
			}
		}
	case []any:
		for _, item := range x {
			if l := findLabels(item); len(l) > 0 {
				return l
			}
		}
	}
	return nil
}

// buildImage runs an engine build of a context folder, capturing its output.
func buildImage(engine, containerfile, tag, context string, labels map[string]string) error {
	argv := []string{engine, "build", "--file", containerfile, "--tag", tag}
	for _, k := range sortedKeysString(labels) {
		argv = append(argv, "--label="+k+"="+labels[k])
	}
	_, err := sys.Run(sys.Cmd{Argv: append(argv, context)})
	return err
}

func sortedKeysString(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

// PrepareBaseImage builds rox:linux when it's missing or its Containerfile changed.
func PrepareBaseImage(engine, image string) error {
	if image != DefaultContainerImage {
		return nil
	}
	// Recheck after taking the lock in case another launch built the image.
	lock, err := sys.WaitLock(filepath.Join(StateRoot(), "container-image.lock"))
	if err != nil {
		return err
	}
	defer lock.Unlock()
	info := inspectImage(engine, image)
	wanted := assets.Toolkit()
	if info != nil && info.Labels["io.rox.toolkit"] == wanted {
		ui.Skip("%s is up to date", image)
		return nil
	}
	reason := "the Containerfile changed"
	if info == nil {
		reason = "first build"
	}
	s := ui.Start("Building %s with %s", image, engine)
	s.Note(reason)
	scratch, err := os.MkdirTemp("", "rox-build-")
	if err != nil {
		s.Fail(err)
		return err
	}
	defer os.RemoveAll(scratch)
	if err := assets.BuildContext(scratch); err != nil {
		s.Fail(err)
		return err
	}
	if err := buildImage(engine, filepath.Join(scratch, "Containerfile"), image, scratch, nil); err != nil {
		s.Fail(err)
		return fmt.Errorf("Building %s with %s failed: %w", image, engine, err)
	}
	s.Done("Built %s", image)
	return nil
}

// ProjectID is the id, and checkout folder name, of a guest's first project called name.
func ProjectID(name string) string {
	if id := Slugify(name, 40); id != "" {
		return id
	}
	return "project"
}

func imageCheckoutPath(home, name string) string { return path.Join(home, ProjectID(name)) }

var (
	containerSlug = regexp.MustCompile(`[^a-z0-9_.-]+`)
	vmSlug        = regexp.MustCompile(`[^a-z0-9-]+`)
)

// ProjectImageName is the image a project's .rox.sh is baked into.
func ProjectImageName(backend, project, base string) string {
	var name string
	if backend == "container" {
		slug := strings.Trim(containerSlug.ReplaceAllString(strings.ToLower(project), "-"), ".-")
		if len(slug) > 100 {
			slug = slug[:100]
		}
		name = "rox:" + firstNonEmpty(slug, "project")
	} else {
		slug := strings.Trim(vmSlug.ReplaceAllString(strings.ToLower(project), "-"), "-")
		if len(slug) > 60 {
			slug = slug[:60]
		}
		name = "rox-" + firstNonEmpty(slug, "project") + "-" + backend
	}
	if name == base {
		return name + "-project"
	}
	return name
}

const projectContainerfile = `FROM {base}
USER root
RUN install -d -m 700 -o workspace -g workspace /home/workspace/.rox
COPY bootstrap.sh /usr/local/share/rox/bootstrap.sh
COPY --chown=workspace:workspace project {project}
USER workspace
RUN {bake}
USER root
`

const agentContainerfile = `FROM {base}
USER root
COPY bootstrap.sh /usr/local/share/rox/bootstrap.sh
USER workspace
RUN {install}
USER root
`

func render(template string, values map[string]string) string {
	for k, v := range values {
		template = strings.ReplaceAll(template, "{"+k+"}", v)
	}
	return template
}

func jsonArgv(argv ...string) string {
	data, _ := json.Marshal(argv)
	return string(data)
}

// PrebuildRequest is what a backend needs to bake a project image.
type PrebuildRequest struct {
	Backend, Base, Project, URL, Branch string
	Branch_                             *Branch
	Rebuild                             bool
	// For Tart builds, which boot a throwaway guest.
	User, Identity, HostKey string
	Boot                    time.Duration
	CPUs                    int
	Memory                  string
	Versions                map[string]string
	Lease                   *Lease
}

func prebuildContainer(engine string, p *PrebuildRequest) (string, error) {
	if p.Branch_ == nil || p.Branch_.Hook == "" {
		return "", nil
	}
	target := ProjectImageName(p.Backend, p.Project, p.Base)
	lock, err := sys.WaitLock(filepath.Join(StateRoot(), "image-"+strings.ReplaceAll(target, ":", "-")+".lock"))
	if err != nil {
		return "", err
	}
	defer lock.Unlock()
	base := inspectImage(engine, p.Base)
	if base == nil {
		err := ui.Run("Pulling "+p.Base, "Pulled "+p.Base, func(*ui.Step) error {
			_, err := sys.Run(sys.Cmd{Argv: []string{engine, "image", "pull", p.Base}})
			return err
		})
		if err != nil {
			return "", err
		}
		if base = inspectImage(engine, p.Base); base == nil {
			return "", fmt.Errorf("Base image %s is not available to %s", p.Base, engine)
		}
	}
	checkout := imageCheckoutPath(ContainerHome, p.Project)
	wanted := map[string]string{"io.rox.hook": p.Branch_.Hook, "io.rox.base": base.ID, "io.rox.repo": p.URL, "io.rox.checkout": checkout}
	current := inspectImage(engine, target)
	if !p.Rebuild && current != nil && labelsMatch(current.Labels, wanted) {
		ui.Done("Project image %s is current %s", target, ui.Muted("(built from "+short(current.Labels["io.rox.commit"])+")"))
		return target, nil
	}
	reason := ".rox.sh or base image changed"
	switch {
	case p.Rebuild:
		reason = "--rebuild"
	case current == nil:
		reason = "first build"
	}
	s := ui.Start("Building project image %s", target)
	s.Note(reason + " · running .rox.sh")
	context, err := os.MkdirTemp("", "rox-image-")
	if err != nil {
		s.Fail(err)
		return "", err
	}
	defer os.RemoveAll(context)
	commit, err := exportCheckout(p.Branch_.Mirror, p.URL, p.Branch, filepath.Join(context, "project"))
	if err == nil {
		err = os.WriteFile(filepath.Join(context, "bootstrap.sh"), []byte(assets.Bootstrap()), 0o644)
	}
	if err == nil {
		bake := jsonArgv("bash", "/usr/local/share/rox/bootstrap.sh", "bake", checkout, "container", target)
		err = os.WriteFile(filepath.Join(context, "Containerfile"),
			[]byte(render(projectContainerfile, map[string]string{"base": p.Base, "project": checkout, "bake": bake})), 0o644)
	}
	if err == nil {
		labels := map[string]string{"io.rox.commit": commit, "io.rox.branch": p.Branch}
		for k, v := range wanted {
			labels[k] = v
		}
		if err = buildImage(engine, filepath.Join(context, "Containerfile"), target, context, labels); err != nil {
			err = fmt.Errorf("Building %s failed while running .rox.sh. Launch with --no-prebuild to run it at boot: %w", target, err)
		}
	}
	if err != nil {
		s.Fail(err)
		return "", err
	}
	s.Done("Built project image %s", target)
	if built := inspectImage(engine, target); engine != "container" && current != nil && built != nil && built.ID != current.ID {
		// Drop the superseded image unless a workspace still uses it.
		sys.Status(engine, "rmi", current.ID)
	}
	return target, nil
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func short(commit string) string {
	if commit == "" {
		return "?"
	}
	if len(commit) > 12 {
		return commit[:7]
	}
	return commit
}

// exportCheckout writes a checkout of the mirrored branch whose origin is the real remote.
func exportCheckout(mirror, url, branch, destination string) (string, error) {
	git := []string{"git", "-C", destination}
	steps := [][]string{
		{"git", "init", "--quiet", destination},
		append(append([]string{}, git...), "remote", "add", "origin", url),
		append(append([]string{}, git...), "fetch", "--quiet", "--no-tags", mirror, "+"+branchCacheRef(branch)+":refs/remotes/origin/"+branch),
		append(append([]string{}, git...), "checkout", "--quiet", "-B", branch, "refs/remotes/origin/"+branch),
		append(append([]string{}, git...), "branch", "--quiet", "--set-upstream-to=origin/"+branch),
	}
	for _, step := range steps {
		if _, err := sys.Output(step...); err != nil {
			return "", err
		}
	}
	return sys.Output(append(git, "rev-parse", "HEAD")...)
}

// AgentImageName is the image of base with agents installed in one last layer.
func AgentImageName(base string) string {
	if strings.HasPrefix(base, "rox:") {
		return base + "-agents"
	}
	slug := strings.Trim(containerSlug.ReplaceAllString(strings.ToLower(base), "-"), ".-")
	if len(slug) > 100 {
		slug = slug[:100]
	}
	return "rox:" + firstNonEmpty(slug, "image") + "-agents"
}

// PrepareAgentImage returns an image of base with the agents installed in one last layer.
// Only that layer rebuilds for a new agent release, so .rox.sh doesn't run again. When the
// build fails it returns base, and the guest installs the agents at boot.
func PrepareAgentImage(engine, base string, versions map[string]string) (string, error) {
	target := AgentImageName(base)
	spec := strings.Join(AgentSpecs(versions), " ")
	lock, err := sys.WaitLock(filepath.Join(StateRoot(), "image-"+strings.ReplaceAll(target, ":", "-")+".lock"))
	if err != nil {
		return base, err
	}
	defer lock.Unlock()
	baseInfo := inspectImage(engine, base)
	if baseInfo == nil {
		if _, err := sys.Run(sys.Cmd{Argv: []string{engine, "image", "pull", base}}); err != nil {
			return base, err
		}
		if baseInfo = inspectImage(engine, base); baseInfo == nil {
			return base, fmt.Errorf("Base image %s is not available to %s", base, engine)
		}
	}
	wanted := map[string]string{"io.rox.agents": spec, "io.rox.agents-base": baseInfo.ID}
	current := inspectImage(engine, target)
	if current != nil && labelsMatch(current.Labels, wanted) {
		ui.Done("%s %s", DescribeAgents(versions), ui.Muted("baked into "+target))
		return target, nil
	}
	reason := "base image changed"
	switch {
	case current == nil:
		reason = "first build"
	case current.Labels["io.rox.agents"] != spec:
		reason = "new agent versions"
	}
	s := ui.Start("Baking %s into %s", DescribeAgents(versions), target)
	s.Note(reason)
	context, err := os.MkdirTemp("", "rox-agents-")
	if err != nil {
		s.Fail(err)
		return base, err
	}
	defer os.RemoveAll(context)
	install := jsonArgv(append([]string{"env", "HOME=/home/workspace", "bash", "/usr/local/share/rox/bootstrap.sh", "agents"}, AgentSpecs(versions)...)...)
	_ = os.WriteFile(filepath.Join(context, "bootstrap.sh"), []byte(assets.Bootstrap()), 0o644)
	_ = os.WriteFile(filepath.Join(context, "Containerfile"), []byte(render(agentContainerfile, map[string]string{"base": base, "install": install})), 0o644)
	if err := buildImage(engine, filepath.Join(context, "Containerfile"), target, context, wanted); err != nil {
		s.Warn("Could not bake the agents into %s; the guest installs them at boot instead", target)
		return base, nil
	}
	s.Done("Baked %s into %s", DescribeAgents(versions), target)
	if built := inspectImage(engine, target); engine != "container" && current != nil && built != nil && built.ID != current.ID {
		sys.Status(engine, "rmi", current.ID)
	}
	return target, nil
}

// BuildGuest is a throwaway Tart guest that bakes a project image.
func buildGuest(name, backend, base, user, identity string, cpus int, memory string) *Guest {
	g := NewGuest(name, backend, base, user, "")
	g.Identity, g.CPUs, g.Memory = identity, cpus, memory
	g.dir = filepath.Join(StateRoot(), "builds", name)
	return g
}

func prebuildVM(p *PrebuildRequest) (string, error) {
	if p.Branch_ == nil || p.Branch_.Hook == "" {
		return "", nil
	}
	target := ProjectImageName(p.Backend, p.Project, p.Base)
	images := filepath.Join(StateRoot(), "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		return "", err
	}
	record := filepath.Join(images, target+".json")
	lock, err := sys.WaitLock(filepath.Join(images, target+".lock"))
	if err != nil {
		return "", err
	}
	defer lock.Unlock()
	wanted := map[string]string{"hook": p.Branch_.Hook, "base": p.Base, "repo": p.URL, "checkout": ProjectID(p.Project)}
	current := map[string]string{}
	if data, err := os.ReadFile(record); err == nil {
		_ = json.Unmarshal(data, &current)
	}
	existing, err := tartImages()
	if err != nil {
		return "", err
	}
	exists := existing[target]
	if !p.Rebuild && exists && labelsMatch(current, wanted) {
		ui.Done("Project image %s is current %s", target, ui.Muted("(built from "+short(current["commit"])+")"))
		return target, nil
	}
	reason := ".rox.sh or base image changed"
	switch {
	case p.Rebuild:
		reason = "--rebuild"
	case !exists:
		reason = "first build"
	}
	s := ui.Start("Building project image %s", target)
	s.Note(reason)
	name := "rv-build-" + randomHex(6)
	if err := bakeVM(p, name, target, s); err != nil {
		s.Fail(err)
		return "", err
	}
	if exists {
		if _, err := sys.Run(sys.Cmd{Argv: []string{"tart", "delete", target}}); err != nil {
			s.Fail(err)
			return "", err
		}
	}
	if _, err := sys.Run(sys.Cmd{Argv: []string{"tart", "rename", name, target}}); err != nil {
		s.Fail(err)
		return "", err
	}
	wanted["commit"], wanted["branch"] = p.Branch_.Commit, p.Branch
	_ = sys.WriteJSON(record, wanted)
	s.Done("Built project image %s", target)
	return target, nil
}

func bakeVM(p *PrebuildRequest, name, target string, s *ui.Step) (err error) {
	g := buildGuest(name, p.Backend, p.Base, p.User, p.Identity, p.CPUs, p.Memory)
	if err := os.MkdirAll(g.Directory(), 0o700); err != nil {
		return err
	}
	if err := ensureRuntimeDirs(g); err != nil {
		return err
	}
	stopped := false
	defer func() {
		if !stopped {
			sys.Status("tart", "stop", g.Name)
			sys.Status("tart", "delete", g.Name)
		}
		os.RemoveAll(g.Runtime())
		if stopped {
			os.RemoveAll(g.Directory())
		}
		if err != nil {
			err = fmt.Errorf("%w\nThe build log is in %s. Launch with --no-prebuild to run .rox.sh at boot instead", err, g.Directory())
		}
	}()
	hostKey, err := os.ReadFile(p.HostKey)
	if err != nil {
		return err
	}
	if err := pinHost(g, string(hostKey)); err != nil {
		return err
	}
	if g.Address, g.Port, err = (tartBackend{}).Launch(g, p.Boot, s); err != nil {
		return err
	}
	if err := checkGuestOS(g, p.Boot); err != nil {
		return err
	}
	out, err := guestStage(g, "bake-prepare", stageOpts{}, ProjectID(p.Project))
	if err != nil {
		return err
	}
	paths := sys.Lines(out)
	if len(paths) != 2 || !strings.HasPrefix(paths[0], "/") || !strings.HasPrefix(paths[1], "/") {
		return errors.New("Guest image preparation did not return a valid checkout path")
	}
	workspace, pins := paths[0], paths[1]
	if p.Lease != nil && p.Lease.KnownHosts != "" {
		if _, err := guestRun(g, []string{"bash", "-c", `umask 077; cat > "$1"`, "rox", pins}, p.Lease.KnownHosts, 30*time.Second); err != nil {
			return err
		}
	}
	agent := ""
	if p.Lease != nil {
		agent = p.Lease.Socket
	}
	s.Note("cloning")
	if _, err := guestStage(g, "clone", stageOpts{Agent: agent}, workspace, p.URL, p.Branch, yesNo(agent != ""), pins); err != nil {
		return err
	}
	s.Note("running .rox.sh")
	if _, err := guestStage(g, "bake", stageOpts{}, workspace, p.Backend, target); err != nil {
		return err
	}
	if len(p.Versions) > 0 {
		// Guests check these versions again at boot, so a newer release costs only an update.
		s.Note("installing " + DescribeAgents(p.Versions))
		if _, err := guestStage(g, "agents", stageOpts{}, AgentSpecs(p.Versions)...); err != nil {
			return err
		}
	}
	if _, err := guestRun(g, []string{"sync"}, "", 60*time.Second); err != nil {
		return err
	}
	if _, err := sys.Run(sys.Cmd{Argv: []string{"tart", "stop", g.Name}}); err != nil {
		return err
	}
	stopped = true
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
