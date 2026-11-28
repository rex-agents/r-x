package rux

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/rex-agents/r-x/internal/rux/authapi"
	"github.com/rex-agents/r-x/internal/rux/transcript"
)

// callAuthority sends one API request to the local authority and decodes the answer. A failure
// comes back as the authority's own explanation.
func callAuthority(method, path string, in, out any) error {
	base, err := ensureAuthority()
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// authorityRename is `rux authority rename NAME`.
func authorityRename(args []string) {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, authorityUsage)
		os.Exit(2)
	}
	var p authapi.Persona
	if err := callAuthority("PUT", "/api/persona/name", authapi.PersonaName{Name: args[0]}, &p); err != nil {
		fmt.Fprintln(os.Stderr, "rux:", err)
		os.Exit(1)
	}
	fmt.Printf("%s Your authority is now %s. New agent sessions use the name.\n",
		transcript.StAccent.Render("◆"), transcript.StBold.Render(p.Name))
}

// authorityMascot is `rux authority mascot "PROMPT"` and `rux authority mascot --reset`.
func authorityMascot(args []string) {
	if len(args) == 1 && args[0] == "--reset" {
		if err := callAuthority("DELETE", "/api/persona/mascot", nil, nil); err != nil {
			fmt.Fprintln(os.Stderr, "rux:", err)
			os.Exit(1)
		}
		fmt.Printf("%s Back to the T-rex.\n", transcript.StAccent.Render("◆"))
		return
	}
	prompt := strings.TrimSpace(strings.Join(args, " "))
	if prompt == "" || strings.HasPrefix(prompt, "-") {
		fmt.Fprint(os.Stderr, authorityUsage)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "%s Drawing your mascot…\n", transcript.StAccent.Render("◆"))
	var m authapi.Mascot
	if err := callAuthority("POST", "/api/persona/mascot", authapi.MascotRequest{Prompt: prompt, Save: true}, &m); err != nil {
		fmt.Fprintln(os.Stderr, "rux:", err)
		os.Exit(1)
	}
	fmt.Println()
	fmt.Println(lipgloss.NewStyle().PaddingLeft(2).Render(mascotLogo(&m, 0)))
	fmt.Println()
	fmt.Printf("%s Saved. Every rux screen shows it now.\n  %s\n", transcript.StAccent.Render("◆"),
		transcript.StDim.Render(fmt.Sprintf("%d×%d · %s · %s", mascotWidth(m.Mask), len(m.Mask), strings.Join(m.Colors, " "), m.Model)))
}

func mascotWidth(mask []string) int {
	w := 0
	for _, r := range mask {
		w = max(w, len(r))
	}
	return w
}
