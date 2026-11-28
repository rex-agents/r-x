package rux

import (
	"reflect"
	"testing"
)

func TestSplitQuorumArgs(t *testing.T) {
	for _, c := range []struct {
		args []string
		url  string
		rest []string
		err  bool
	}{
		{[]string{"join", "h:7710", "code", "--url", "http://x:7710"}, "http://x:7710", []string{"join", "h:7710", "code"}, false},
		{[]string{"--url=http://x", "join", "h", "code"}, "http://x", []string{"join", "h", "code"}, false},
		{[]string{"join", "-url", "x", "h", "code"}, "x", []string{"join", "h", "code"}, false},
		{[]string{"create", "--bogus"}, "", nil, true},
		{[]string{"join", "h", "--url"}, "", nil, true},
		{[]string{"policy", "allow", "b", "--", "--odd-name"}, "", []string{"policy", "allow", "b", "--odd-name"}, false},
	} {
		url, _, _, rest, err := splitQuorumArgs(c.args)
		if (err != nil) != c.err || url != c.url || (!c.err && !reflect.DeepEqual(rest, c.rest)) {
			t.Errorf("%v: url %q rest %v err %v", c.args, url, rest, err)
		}
	}
}
