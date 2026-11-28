package rux

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// The mascot is the logo a user's authority draws in place of the rux T-rex. A model makes it
// from a description; it is kept in ~/.rux/mascot.json:
//
//	{"prompt": "...", "mask": ["...#..", ...], "colors": ["#rrggbb", x3], "model": "...", "created": "..."}

const (
	mascotMaxW   = 40
	mascotMaxH   = 16
	mascotMinH   = 5  // a mascot shorter than this is a scribble
	mascotMinW   = 8  // and so is one narrower than this
	mascotMinDot = 40 // ... or with fewer dots than this
)

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func mascotPath(dir string) string { return filepath.Join(dir, "mascot.json") }

// defaultMascotColors are the T-rex's orange, pink and violet.
var defaultMascotColors = []string{"#ff9a3c", "#f472b6", "#a78bfa"}

// validateMascot checks a mascot's shape and colours and tidies it: blank rows above and below
// are dropped, trailing blanks trimmed and colours lowercased. It changes m only when it's valid.
func validateMascot(m *authapi.Mascot) error {
	mask := append([]string(nil), m.Mask...)
	for len(mask) > 0 && strings.TrimSpace(mask[0]) == "" {
		mask = mask[1:]
	}
	for len(mask) > 0 && strings.TrimSpace(mask[len(mask)-1]) == "" {
		mask = mask[:len(mask)-1]
	}
	if len(mask) == 0 {
		return errors.New("the mascot is empty")
	}
	if len(mask) > mascotMaxH {
		return fmt.Errorf("the mascot is %d rows tall; the most is %d", len(mask), mascotMaxH)
	}
	dots, width := 0, 0
	for i, row := range mask {
		for _, c := range row {
			if c != '#' && c != ' ' {
				return fmt.Errorf("row %d has %q; rows use only # and spaces", i+1, c)
			}
		}
		row = strings.TrimRight(row, " ")
		mask[i] = row
		if len(row) > mascotMaxW {
			return fmt.Errorf("row %d is %d wide; the most is %d", i+1, len(row), mascotMaxW)
		}
		width = max(width, len(row))
		dots += strings.Count(row, "#")
	}
	if len(mask) < mascotMinH || width < mascotMinW || dots < mascotMinDot {
		return errors.New("the mascot is too small to see")
	}
	if len(m.Colors) != 3 {
		return fmt.Errorf("the mascot needs 3 colours, not %d", len(m.Colors))
	}
	colors := make([]string, 3)
	for i, c := range m.Colors {
		if !hexColor.MatchString(c) {
			return fmt.Errorf("colour %q isn't #rrggbb", c)
		}
		colors[i] = strings.ToLower(c)
	}
	m.Mask, m.Colors = mask, colors
	return nil
}

// parseMascot reads a model's reply, which should be a JSON object {"mask": [...], "colors":
// [...]}, perhaps in a code fence or after some words, and checks it.
func parseMascot(reply string) (authapi.Mascot, error) {
	i, j := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if i < 0 || j < i {
		return authapi.Mascot{}, errors.New("the reply has no JSON object")
	}
	var m authapi.Mascot
	if err := json.Unmarshal([]byte(reply[i:j+1]), &m); err != nil {
		return authapi.Mascot{}, fmt.Errorf("the reply isn't valid JSON: %v", err)
	}
	m.Prompt, m.Model, m.Created = "", "", ""
	if err := validateMascot(&m); err != nil {
		return authapi.Mascot{}, err
	}
	return m, nil
}

// loadMascot reads ~/.rux/mascot.json; nil when there's none, or it's not a valid mascot.
func loadMascot(dir string) *authapi.Mascot {
	b, err := os.ReadFile(mascotPath(dir))
	if err != nil {
		return nil
	}
	var m authapi.Mascot
	if json.Unmarshal(b, &m) != nil || validateMascot(&m) != nil {
		return nil
	}
	return &m
}

func saveMascot(dir string, m authapi.Mascot) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(mascotPath(dir), append(b, '\n'))
}

// writeFileAtomic writes via a temporary file, so a reader never sees half a file.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mascotSystem tells the model how to draw.
const mascotSystem = `You draw small ASCII mascots for a terminal app. Reply with one JSON object and nothing else:
{"mask": ["row", "row", ...], "colors": ["#rrggbb", "#rrggbb", "#rrggbb"]}

- "mask" is the mascot's silhouette, one string per row, from the top. Use "#" where a dot is drawn
  and a space elsewhere; no other characters. At most 40 characters wide and 16 rows tall; use most of
  that. Make a bold, solid, recognizable shape (a head, a body, ears, a tail), filled in, with a few
  gaps for eyes and details. Terminal cells are about twice as tall as wide, so draw wide.
- "colors" are three hex colours for a gradient from the top left to the bottom right. Pick bright
  colours that suit the character and read on a dark background.

Example: the rux T-rex.
` + "```" + `
%s
` + "```" + `
Colours of the example: ["#ff9a3c", "#f472b6", "#a78bfa"].`

func mascotPrompt(desc string) string {
	return "Draw a mascot for this AI agent: " + strings.TrimSpace(desc)
}
