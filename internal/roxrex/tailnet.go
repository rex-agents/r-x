package roxrex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/rox"
	"github.com/rex-agents/r-x/internal/sys"
)

// tailscaleOn turns on the guest server's tailnet node. The auth key goes over rox exec's stdin.
func tailscaleOn(g *contract.Guest, t *rox.Tailnet, key *string) error {
	argv := guestRex(g, "server", "tailscale", "on", "--hostname", g.Name, "--json")
	var tags []string
	controlURL := ""
	if t != nil {
		tags, controlURL = t.Tags, t.ControlURL
	}
	for _, tag := range tags {
		argv = append(argv, "--advertise-tag", tag)
	}
	if len(tags) > 0 {
		// A tagged node belongs to its tags, so the tailnet ACLs decide who may connect.
		argv = append(argv, "--allow-tailnet-members")
	}
	if controlURL != "" {
		argv = append(argv, "--control-url", controlURL)
	}
	input := ""
	if key != nil {
		argv = append(argv, "--auth-key-file", "-")
		input = *key
	}
	_, err := guestRun(g, input, 30*time.Second, argv...)
	return err
}

type tailnetState struct {
	State   string `json:"state"`
	DNSName string `json:"dns_name"`
	HTTPS   struct {
		State string `json:"state"`
	} `json:"https"`
}

func tailnetStatus(g *contract.Guest) (tailnetState, error) {
	var status struct {
		Tailscale tailnetState `json:"tailscale"`
	}
	out, err := sys.OutputWith(sys.Cmd{Argv: rexOn(g, "server", "status", "--json"), Timeout: 8 * time.Second})
	if err != nil {
		return status.Tailscale, err
	}
	err = json.Unmarshal([]byte(out), &status)
	return status.Tailscale, err
}

// enableTailnet adds the guest's Rex server to the tailnet, when the rox config asks for it,
// and returns its tailnet name.
func enableTailnet(g *contract.Guest, launch *contract.Launch, out *Outcome) string {
	c, err := rox.LoadConfig(launch.Config)
	if err != nil {
		return ""
	}
	t, err := c.TailnetSettings(launch.NoTailscale)
	if err != nil {
		out.warn("%v", err)
		return ""
	}
	if t == nil {
		return ""
	}
	name, err := func() (string, error) {
		key, err := t.Key()
		if err == nil {
			err = tailscaleOn(g, t, &key)
		}
		if err != nil {
			return "", err
		}
		// The node reports needs_login for a moment while Tailscale takes the key, so only a node
		// still waiting when time runs out means the key was rejected.
		var last tailnetState
		name, err := sys.WaitFor(60*time.Second, "the guest to join the tailnet", func() (string, bool, error) {
			state, err := tailnetStatus(g)
			last = state
			return strings.TrimRight(state.DNSName, "."), err == nil && state.State == "running" && state.DNSName != "", err
		})
		if err != nil && last.State == "needs_login" {
			err = errors.New("Tailscale still wants a browser sign-in, so the auth key was rejected. Check that it's valid, reusable, and allowed to use the configured tags")
		}
		return name, err
	}()
	if err != nil {
		out.warn("Could not add %s to the tailnet: %s", g.Name, firstLine(err.Error()))
		return ""
	}
	// Rex serves the tailnet over HTTPS, and refuses other devices until Tailscale issues a certificate.
	if _, err := sys.WaitFor(90*time.Second, "the guest's tailnet HTTPS certificate", func() (bool, bool, error) {
		state, err := tailnetStatus(g)
		return true, err == nil && state.HTTPS.State == "available", err
	}); err != nil {
		out.warn("On the tailnet at %s, but its HTTPS certificate isn't ready yet", name)
	}
	out.done("On the tailnet at %s", name)
	return name
}

// resumeTailnet serves over the tailnet again after a server restart. Tailscale deletes an
// ephemeral node that stays offline, so the configured key goes along when there is one.
func resumeTailnet(g *contract.Guest, out *Outcome) string {
	if g.Tailnet == "" {
		return ""
	}
	err := func() error {
		state, err := tailnetStatus(g)
		if err != nil || state.State == "running" {
			return err
		}
		var key *string
		var t *rox.Tailnet
		if c, err := rox.LoadConfig(""); err == nil {
			if t, _ = c.TailnetSettings(false); t != nil {
				k, err := t.Key()
				if err != nil {
					return err
				}
				key = &k
			}
		}
		if err := tailscaleOn(g, t, key); err != nil {
			return err
		}
		_, err = sys.WaitFor(30*time.Second, "the guest to rejoin the tailnet", func() (bool, bool, error) {
			state, err := tailnetStatus(g)
			return true, err == nil && state.State == "running", err
		})
		return err
	}()
	if err != nil {
		out.warn("%s could not rejoin the tailnet, so only this machine can reach it (%s)", g.Name, firstLine(err.Error()))
	}
	return ""
}

// leaveTailnet signs the guest's node out so it doesn't linger after the guest is gone.
func leaveTailnet(g *contract.Guest, out *Outcome) {
	if _, err := guestRun(g, "", 20*time.Second, guestRex(g, "server", "tailscale", "logout")...); err != nil {
		out.warn("Could not sign %s out of the tailnet. Remove it in the Tailscale admin console if it's still listed", g.Name)
	}
}

var _ = fmt.Sprint
