package rux

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/rux/quorum"
)

// rux quorum create|invite|join|leave|status|promote|policy|vault-host: pack membership.
// With the authority running these go through its admin socket (~/.rux/quorum.sock); create,
// join and status also work without it, by editing ~/.rux/quorum.json for the authority to
// pick up when it starts.

const quorumUsage = `usage:
  rux quorum create                       make this authority the leader and vault host of a new pack
  rux quorum invite                       print a one-time join code (10 minutes)
  rux quorum join HOST CODE               join the pack whose leader is HOST (name or URL, port 7710)
  rux quorum leave                        leave the pack
  rux quorum status                       members, leader, term, vault host, degraded states
  rux quorum promote HOST                 make HOST the leader (needed with two members)
  rux quorum vault-host HOST              move the vault host
  rux quorum policy [show]                the vault policy
  rux quorum policy allow MEMBER SECRET...  let MEMBER read SECRET... (run on the vault host)
  rux quorum policy deny MEMBER SECRET...

flags: --url URL  this authority's quorum URL (default: its tailnet address, port 7710)
`

func runQuorum(args []string) {
	if err := quorumMain(args); err != nil {
		fmt.Fprintln(os.Stderr, "rux quorum:", err)
		os.Exit(1)
	}
}

func quorumMain(args []string) error {
	url, force, help, rest, err := splitQuorumArgs(args)
	if err != nil {
		return err
	}
	if help {
		fmt.Print(quorumUsage)
		return nil
	}
	if len(rest) == 0 {
		fmt.Fprint(os.Stderr, quorumUsage)
		os.Exit(2)
	}
	dir := authorityHome()
	cmd, rest := rest[0], rest[1:]
	need := func(n int, what string) error {
		if len(rest) < n {
			return fmt.Errorf("%s needs %s", cmd, what)
		}
		return nil
	}
	admin, adminErr := quorum.DialAdmin(dir)
	offline := func() (*quorum.Node, error) {
		return quorum.New(quorum.Config{Dir: dir, Name: authorityName(dir), AdvertiseURL: quorumURL(url)})
	}
	needRunning := func() error {
		if admin == nil {
			return fmt.Errorf("%s needs the authority running (rux authority start): %v", cmd, adminErr)
		}
		return nil
	}
	ctx := context.Background()
	switch cmd {
	case "create":
		if admin != nil {
			return admin.Do("POST", "/admin/create", nil, nil)
		}
		n, err := offline()
		if err != nil {
			return err
		}
		if err := n.Create(); err != nil {
			return err
		}
		fmt.Printf("Created a pack. %s is its leader and vault host. Start the authority to bring it up, then run rux quorum invite.\n", n.Name())
	case "invite":
		if err := needRunning(); err != nil {
			return err
		}
		var info quorum.InviteInfo
		if err := admin.Do("POST", "/admin/invite", nil, &info); err != nil {
			return err
		}
		host := strings.TrimPrefix(strings.TrimPrefix(info.URL, "http://"), "https://")
		fmt.Printf("Join code: %s (valid until %s)\nOn the other authority run:\n  rux quorum join %s %s\n", info.Code, info.Expires.Local().Format("15:04"), host, info.Code)
	case "join":
		if err := need(2, "HOST CODE"); err != nil {
			return err
		}
		if admin != nil {
			return admin.Do("POST", "/admin/join", map[string]string{"host": rest[0], "code": rest[1]}, nil)
		}
		n, err := offline()
		if err != nil {
			return err
		}
		jctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := n.Join(jctx, rest[0], rest[1]); err != nil {
			return err
		}
		fmt.Printf("Joined the pack as %s. Start the authority to connect.\n", n.Name())
	case "leave":
		if admin != nil {
			return admin.Do("POST", "/admin/leave", nil, nil)
		}
		if !force {
			return fmt.Errorf("the authority isn't running, so the pack can't be told. Start it, or run rux quorum leave --force to forget the pack here only")
		}
		n, err := offline()
		if err != nil {
			return err
		}
		return n.Leave(ctx)
	case "status":
		var st quorum.Status
		if admin != nil {
			if err := admin.Do("GET", "/admin/status", nil, &st); err != nil {
				return err
			}
		} else {
			n, err := offline()
			if err != nil {
				return err
			}
			if !n.InPack() {
				fmt.Println("Not in a pack. rux quorum create makes one; rux quorum join HOST CODE joins one.")
				return nil
			}
			st = n.Status()
			st.Degraded = "the authority isn't running"
		}
		printQuorumStatus(st)
	case "promote":
		if err := need(1, "HOST"); err != nil {
			return err
		}
		if err := needRunning(); err != nil {
			return err
		}
		if err := admin.Do("POST", "/admin/promote", map[string]string{"member": rest[0]}, nil); err != nil {
			return err
		}
		fmt.Printf("%s is the pack leader.\n", rest[0])
	case "vault-host":
		if err := need(1, "HOST"); err != nil {
			return err
		}
		if err := needRunning(); err != nil {
			return err
		}
		if err := admin.Do("POST", "/admin/vault-host", map[string]string{"member": rest[0]}, nil); err != nil {
			return err
		}
		fmt.Printf("%s is the vault host now. It serves the pack's keys from its own rix, so make sure it has them (rix auth claude, rix secret set ...). The policy carried over.\n", rest[0])
	case "policy":
		if err := needRunning(); err != nil {
			return err
		}
		sub := "show"
		if len(rest) > 0 {
			sub, rest = rest[0], rest[1:]
		}
		switch sub {
		case "show":
			var p quorum.Policy
			if err := admin.Do("GET", "/admin/policy", nil, &p); err != nil {
				return err
			}
			data, _ := json.MarshalIndent(p, "", "  ")
			fmt.Println(string(data))
		case "allow", "deny":
			if len(rest) < 2 {
				return fmt.Errorf("policy %s needs MEMBER SECRET...", sub)
			}
			return admin.Do("POST", "/admin/policy", map[string]any{"action": sub, "member": rest[0], "secrets": rest[1:]}, nil)
		default:
			return fmt.Errorf("unknown policy command %q", sub)
		}
	default:
		fmt.Fprint(os.Stderr, quorumUsage)
		os.Exit(2)
	}
	return nil
}

func printQuorumStatus(st quorum.Status) {
	if !st.InPack {
		fmt.Println("Not in a pack.")
		return
	}
	role := "member"
	if st.IsLeader {
		role = "leader"
	}
	fmt.Printf("%s (%s)\n", st.Name, role)
	leader := st.Leader
	if leader == "" {
		leader = "none"
	}
	fmt.Printf("  leader      %s, term %d\n", leader, st.Term)
	fmt.Printf("  vault host  %s (%s)\n", st.VaultHost, map[bool]string{true: "ok", false: "vault unreachable"}[st.VaultOK])
	fmt.Printf("  git keys    %s\n", st.SSHAgent)
	fail := "automatic with three or more members"
	if !st.AutoFailover {
		fail = "manual: two members elect no one, run rux quorum promote HOST on the one to promote"
	}
	fmt.Printf("  failover    %s\n", fail)
	fmt.Println("  members")
	for _, m := range st.Members {
		mark := ""
		for _, v := range st.Voters {
			if v == m.Name {
				mark = " voter"
			}
		}
		fmt.Printf("    %-20s %s%s\n", m.Name, m.URL, mark)
	}
	if st.Degraded != "" {
		fmt.Printf("  degraded    %s\n", st.Degraded)
	}
}

// authorityName is the name from ~/.rux/authority.json, else the hostname.
func authorityName(dir string) string {
	if data, err := os.ReadFile(filepath.Join(dir, "authority.json")); err == nil {
		var c struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &c) == nil && c.Name != "" {
			return c.Name
		}
	}
	h, _ := os.Hostname()
	return strings.SplitN(h, ".", 2)[0]
}

// quorumURL is where other authorities reach this one: --url, RUX_QUORUM_URL, the tailnet
// address, or the hostname, on port 7710.
func quorumURL(flag string) string {
	if flag == "" {
		flag = os.Getenv("RUX_QUORUM_URL")
	}
	if flag != "" {
		if !strings.Contains(flag, "://") {
			flag = "http://" + flag
		}
		return flag
	}
	host := ""
	if out, err := exec.Command("tailscale", "ip", "-4").Output(); err == nil {
		host = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	}
	if host == "" {
		host, _ = os.Hostname()
	}
	return fmt.Sprintf("http://%s:%d", host, quorum.DefaultPort)
}

// splitQuorumArgs separates the flags from the positional arguments wherever they are:
// `rux quorum join HOST CODE --url X` and `rux quorum --url X join HOST CODE` mean the same.
// One or two dashes both work, and `--` ends the flags.
func splitQuorumArgs(args []string) (url string, force, help bool, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			rest = append(rest, a)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch name {
		case "url":
			if !hasValue {
				if i+1 >= len(args) {
					return "", false, false, nil, fmt.Errorf("--url needs a value")
				}
				i++
				value = args[i]
			}
			url = value
		case "force":
			force = true
		case "h", "help":
			help = true
		default:
			return "", false, false, nil, fmt.Errorf("unknown flag %s", a)
		}
	}
	return
}
