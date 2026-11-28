package rux

import (
	"fmt"
	"regexp"
	"strings"
)

func modelShort(m string) string {
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		return m[i+1:]
	}
	return m
}

func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

var claudeID = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable|mythos)-(\d+)-(\d+)$`)

// prettyModel names an engine for people: "Claude Code · Opus", "Codex", "Opus 5.5".
func prettyModel(spec string) string {
	title := func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToUpper(s[:1]) + s[1:]
	}
	if cli, model, ok := agentCLI(spec); ok {
		name := map[string]string{"claude": "Claude Code", "codex": "Codex"}[cli]
		if model == "" {
			return name
		}
		if cli == "claude" {
			model = title(model)
		}
		return name + " · " + model
	}
	m := modelShort(spec)
	if mm := claudeID.FindStringSubmatch(m); mm != nil {
		return title(mm[1]) + " " + mm[2] + "." + mm[3]
	}
	return m
}

var effortNames = map[string]string{"": "Auto", "low": "Low", "medium": "Medium", "high": "High", "xhigh": "Extra High", "max": "Max"}
var effortOrder = []string{"", "low", "medium", "high", "xhigh", "max"}
