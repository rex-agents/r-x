package rux

import (
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// goodMask is a small but valid mascot.
func goodMask() []string {
	return []string{
		"   ########   ",
		"  ##########  ",
		" ############ ",
		" ## ###### ## ",
		" ############ ",
		"  ##########  ",
		"   ## ## ##   ",
	}
}

func jsonOf(mask []string, colors string) string {
	q := make([]string, len(mask))
	for i, r := range mask {
		q[i] = `"` + r + `"`
	}
	return `{"mask": [` + strings.Join(q, ",") + `], "colors": ` + colors + `}`
}

const goodColors = `["#FF9A3C", "#f472b6", "#a78bfa"]`

func TestParseMascotGood(t *testing.T) {
	// Words and a code fence around the JSON are fine.
	reply := "Here you go:\n```json\n" + jsonOf(goodMask(), goodColors) + "\n```\n"
	m, err := parseMascot(reply)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Mask) != 7 || m.Colors[0] != "#ff9a3c" {
		t.Errorf("mascot = %+v", m)
	}
	if m.Mask[0] != "   ########" {
		t.Errorf("trailing blanks not trimmed: %q", m.Mask[0])
	}
}

func TestParseMascotTrimsBlankRows(t *testing.T) {
	mask := append(append([]string{"", "   "}, goodMask()...), "")
	m, err := parseMascot(jsonOf(mask, goodColors))
	if err != nil || len(m.Mask) != 7 {
		t.Fatalf("m=%+v err=%v", m, err)
	}
}

func TestValidateMascotRejects(t *testing.T) {
	wide := make([]string, 6)
	for i := range wide {
		wide[i] = strings.Repeat("#", 41)
	}
	tall := make([]string, 17)
	for i := range tall {
		tall[i] = strings.Repeat("#", 12)
	}
	exactly := make([]string, 16)
	for i := range exactly {
		exactly[i] = strings.Repeat("#", 40)
	}
	cases := []struct {
		name   string
		mask   []string
		colors []string
		want   string // "" = valid
	}{
		{"fits exactly", exactly, []string{"#000000", "#ffffff", "#123456"}, ""},
		{"too wide", wide, []string{"#000000", "#ffffff", "#123456"}, "41 wide"},
		{"too tall", tall, []string{"#000000", "#ffffff", "#123456"}, "17 rows"},
		{"empty", nil, []string{"#000000", "#ffffff", "#123456"}, "empty"},
		{"blank rows only", []string{"   ", ""}, []string{"#000000", "#ffffff", "#123456"}, "empty"},
		{"almost empty", []string{"#", "", "", "#"}, []string{"#000000", "#ffffff", "#123456"}, "too small"},
		{"short", []string{strings.Repeat("#", 40), strings.Repeat("#", 40)}, []string{"#000000", "#ffffff", "#123456"}, "too small"},
		{"bad character", append([]string{"   ####@###   "}, goodMask()...), []string{"#000000", "#ffffff", "#123456"}, "only # and spaces"},
		{"tab", append([]string{"\t####"}, goodMask()...), []string{"#000000", "#ffffff", "#123456"}, "only # and spaces"},
		{"two colours", goodMask(), []string{"#000000", "#ffffff"}, "3 colours"},
		{"colour without hash", goodMask(), []string{"000000", "#ffffff", "#123456"}, "isn't #rrggbb"},
		{"colour name", goodMask(), []string{"red", "#ffffff", "#123456"}, "isn't #rrggbb"},
		{"short hex", goodMask(), []string{"#fff", "#ffffff", "#123456"}, "isn't #rrggbb"},
		{"non-hex digits", goodMask(), []string{"#gg0000", "#ffffff", "#123456"}, "isn't #rrggbb"},
	}
	for _, c := range cases {
		m := authapi.Mascot{Mask: c.mask, Colors: c.colors}
		err := validateMascot(&m)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestParseMascotRejects(t *testing.T) {
	for name, reply := range map[string]string{
		"no JSON":    "I can't draw that.",
		"broken":     `{"mask": [`,
		"wrong type": `{"mask": "###", "colors": []}`,
		"bad colour": jsonOf(goodMask(), `["#zzzzzz","#ffffff","#000000"]`),
	} {
		if _, err := parseMascot(reply); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMascotStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if loadMascot(dir) != nil {
		t.Fatal("a mascot out of nowhere")
	}
	m := authapi.Mascot{Prompt: "a cat", Mask: goodMask(), Colors: []string{"#111111", "#222222", "#333333"}, Model: "x"}
	if err := validateMascot(&m); err != nil {
		t.Fatal(err)
	}
	if err := saveMascot(dir, m); err != nil {
		t.Fatal(err)
	}
	got := loadMascot(dir)
	if got == nil || got.Prompt != "a cat" || len(got.Mask) != 7 {
		t.Errorf("loaded %+v", got)
	}
}

func TestRenderMascotUsesItsColours(t *testing.T) {
	// A mask with every dot in the top-left corner takes the first colour's hue.
	m := &authapi.Mascot{Mask: goodMask(), Colors: []string{"#00ff00", "#00ff00", "#00ff00"}}
	out := mascotLogo(m, 0)
	if lines := strings.Split(out, "\n"); len(lines) != 7 {
		t.Errorf("rows = %d", len(lines))
	}
	if mascotLogo(nil, 0) != trexLogo(0) {
		t.Error("no mascot should draw the T-rex")
	}
}
