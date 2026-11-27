package rix

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// Credentials that tools on this machine, such as rux, ask rix for: a GitHub token, and
// named secrets that rux actions list. rix keeps them in its vault (mode 0600) and hands
// them out on stdout, so no other tool stores them.

const githubTokenFile = "github-token"

// GitHubCredential is the token Git and the GitHub API use, and where it came from.
type GitHubCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Source   string `json:"source"` // "vault" or "gh"
}

// GitHub returns the GitHub token: the one stored with rix auth github, or else, unless
// that's turned off, the gh CLI's login on this machine.
func (c *Config) GitHub() (*GitHubCredential, error) {
	settings, _ := c.Data["github"].(map[string]any)
	if off, _ := settings["off"].(bool); off {
		return nil, errors.New("GitHub access is off. Turn it on with rix auth github")
	}
	if file, _ := settings["token_file"].(string); file != "" {
		token, err := readSecretFile(sys.Expand(file))
		if err != nil {
			return nil, err
		}
		return &GitHubCredential{Username: "x-access-token", Password: token, Source: "vault"}, nil
	}
	if _, err := sys.R.LookPath("gh"); err != nil {
		return nil, errors.New("No GitHub token. Store one with rix auth github, or sign in with gh auth login")
	}
	out, err := sys.Output("gh", "auth", "token", "--hostname", "github.com")
	token := strings.TrimSpace(out)
	if err != nil || token == "" {
		return nil, errors.New("gh isn't signed in to github.com. Run gh auth login, or store a token with rix auth github")
	}
	return &GitHubCredential{Username: "x-access-token", Password: token, Source: "gh"}, nil
}

func readSecretFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("Could not read %s: %v", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s holds a secret, so only you may read it. Run chmod 600 %s", path, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// AuthGitHub stores a GitHub token, uses gh's login, or turns GitHub access off.
func AuthGitHub(useGh, off bool) error {
	ui.Header("auth github", "")
	path := ConfigLocation("")
	settings := map[string]any{}
	switch {
	case off:
		settings["off"] = true
	case useGh:
	default:
		token, err := ui.Secret("GitHub token:", "Paste a fine-grained or classic personal access token with access to the repositories agents\nwork on. Leave it blank to use gh's login on this machine instead.")
		if err != nil {
			return err
		}
		if strings.ContainsAny(token, " \t") {
			return errors.New("Expected a single token with no spaces")
		}
		if token != "" {
			file := filepath.Join(filepath.Dir(path), githubTokenFile)
			if err := sys.WritePrivate(file, token+"\n"); err != nil {
				return err
			}
			ui.Done("Saved the GitHub token to %s %s", ui.Code(file), ui.Muted("(0600)"))
			settings["token_file"] = file
		}
	}
	err := UpdateThen(path, func(data map[string]any) error {
		if len(settings) == 0 {
			delete(data, "github")
		} else {
			data["github"] = settings
		}
		return nil
	}, func() {
		// The old token goes only after the config stopped naming it, still under its lock.
		if settings["token_file"] == nil {
			os.Remove(filepath.Join(filepath.Dir(path), githubTokenFile))
		}
	})
	if err != nil {
		return err
	}
	switch {
	case off:
		ui.Done("Tools get no GitHub access from rix")
	case settings["token_file"] != nil:
		ui.Done("Tools use this token for GitHub")
	default:
		ui.Done("Tools use gh's login for GitHub %s", ui.Muted("· gh auth token"))
	}
	return nil
}

func credentialCommand() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "credential github",
		Short: "print the GitHub token for a tool on this machine, such as rux",
		Long: "Print the GitHub token rix keeps (rix auth github), or gh's login when none is stored.\n" +
			"rux asks for it to list repositories and to answer the git credential helper in its guests,\n" +
			"so the token is never written to a guest's disk.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"github"},
		RunE: func(_ *cobra.Command, args []string) error {
			if args[0] != "github" {
				return errors.New("rix credential knows github")
			}
			c, err := LoadConfig("")
			if err != nil {
				return err
			}
			cred, err := c.GitHub()
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(ui.Stdout()).Encode(cred)
			}
			fmt.Fprintln(ui.Stdout(), cred.Password)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, `print {"username", "password", "source"}`)
	return c
}

// Secrets are named values rux actions ask for, in secrets.json beside the config.

var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func secretsPath() string { return filepath.Join(filepath.Dir(ConfigLocation("")), "secrets.json") }

// LoadSecrets reads every stored secret.
func LoadSecrets() (map[string]string, error) {
	path := secretsPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s holds secrets, so only you may read it. Run chmod 600 %s", path, path)
	}
	secrets := map[string]string{}
	if err := json.Unmarshal(data, &secrets); err != nil {
		return nil, fmt.Errorf("Could not read %s: %v", path, err)
	}
	return secrets, nil
}

// UpdateSecrets locks the secrets, lets fn change them and saves them with mode 0600.
func UpdateSecrets(fn func(map[string]string) error) error {
	path := secretsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := sys.WaitLock(path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Unlock()
	secrets, err := LoadSecrets()
	if err != nil {
		return err
	}
	if err := fn(secrets); err != nil {
		return err
	}
	data, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return err
	}
	return sys.WritePrivate(path, string(data)+"\n")
}

// ParseEnvFile reads KEY=VALUE lines, skipping blanks and comments, and unquoting values.
func ParseEnvFile(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !secretName.MatchString(k) {
			return nil, fmt.Errorf("line %d isn't NAME=VALUE", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, sc.Err()
}

func secretCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "secret",
		Short: "keep the named secrets rux actions use",
		Long: "Keep named secrets, such as API keys that rux actions list in their tool.json. They're\n" +
			"stored in secrets.json beside the config (mode 0600). An action gets only the ones it lists,\n" +
			"and only on this machine; guests never see them.",
	}
	var fromStdin bool
	set := &cobra.Command{
		Use:   "set NAME",
		Short: "store a secret, typed without echo or read from --stdin",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if !secretName.MatchString(name) {
				return fmt.Errorf("%s isn't a valid name. Use letters, digits and underscores, such as ITCH_API_KEY", name)
			}
			var value string
			if fromStdin {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				value = strings.TrimRight(string(data), "\r\n")
			} else {
				ui.Header("secret set", name)
				v, err := ui.Secret(name+":", "")
				if err != nil {
					return err
				}
				value = v
			}
			if value == "" {
				return errors.New("Expected a value. To delete a secret, use rix secret rm " + name)
			}
			if err := UpdateSecrets(func(s map[string]string) error { s[name] = value; return nil }); err != nil {
				return err
			}
			ui.Done("Saved %s %s", ui.Bold(name), ui.Muted("· "+secretsPath()))
			return nil
		},
	}
	set.Flags().BoolVar(&fromStdin, "stdin", false, "read the value from stdin")

	var lsJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "list the secrets' names",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			secrets, err := LoadSecrets()
			if err != nil {
				return err
			}
			if lsJSON {
				return json.NewEncoder(ui.Stdout()).Encode(map[string]any{"names": sortedNames(secrets)})
			}
			if len(secrets) == 0 {
				ui.Info("No secrets. Add one with %s", ui.Code("rix secret set NAME"))
				return nil
			}
			for _, k := range sortedNames(secrets) {
				fmt.Fprintln(ui.Stdout(), k)
			}
			return nil
		},
	}

	ls.Flags().BoolVar(&lsJSON, "json", false, `print {"names": [...]}`)

	rm := &cobra.Command{
		Use:   "rm NAME...",
		Short: "delete secrets",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var gone []string
			err := UpdateSecrets(func(s map[string]string) error {
				for _, k := range args {
					if _, ok := s[k]; ok {
						delete(s, k)
						gone = append(gone, k)
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			if len(gone) == 0 {
				ui.Skip("No secret named %s", strings.Join(args, ", "))
				return nil
			}
			ui.Done("Deleted %s", strings.Join(gone, ", "))
			return nil
		},
	}

	var keepFile bool
	imp := &cobra.Command{
		Use:   "import FILE",
		Short: "store each NAME=VALUE line of an env file, such as rux's old ~/.rux/secrets.env",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			file := sys.Expand(args[0])
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			values, err := ParseEnvFile(f)
			f.Close()
			if err != nil {
				return fmt.Errorf("%s: %v", file, err)
			}
			if len(values) == 0 {
				ui.Skip("%s has no secrets", file)
				return nil
			}
			err = UpdateSecrets(func(s map[string]string) error {
				for k, v := range values {
					s[k] = v
				}
				return nil
			})
			if err != nil {
				return err
			}
			ui.Done("Stored %s %s", strings.Join(sortedNames(values), ", "), ui.Muted("· "+secretsPath()))
			if !keepFile {
				if err := os.Remove(file); err == nil {
					ui.Done("Removed %s, so the secrets live only in rix", ui.Code(file))
				}
			}
			return nil
		},
	}
	imp.Flags().BoolVar(&keepFile, "keep", false, "leave the file in place")

	var asJSON bool
	get := &cobra.Command{
		Use:   "get NAME...",
		Short: "print secrets for a tool on this machine, such as rux",
		Long: "Print the named secrets. With --json, print {\"values\": {NAME: VALUE}, \"missing\": [NAME]}\n" +
			"and succeed even when some are missing, so a tool can say which to add. Without it, print\n" +
			"one value per line and fail if any is missing.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			secrets, err := LoadSecrets()
			if err != nil {
				return err
			}
			values, missing := map[string]string{}, []string{}
			for _, k := range args {
				if v, ok := secrets[k]; ok {
					values[k] = v
				} else {
					missing = append(missing, k)
				}
			}
			if asJSON {
				return json.NewEncoder(ui.Stdout()).Encode(map[string]any{"values": values, "missing": missing})
			}
			if len(missing) > 0 {
				return fmt.Errorf("No secret named %s. Add it with rix secret set %s", strings.Join(missing, ", "), missing[0])
			}
			for _, k := range args {
				fmt.Fprintln(ui.Stdout(), values[k])
			}
			return nil
		},
	}
	get.Flags().BoolVar(&asJSON, "json", false, "print values and missing names as JSON")

	c.AddCommand(set, ls, rm, imp, get)
	return c
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
