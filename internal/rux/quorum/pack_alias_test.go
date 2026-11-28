package quorum

import "testing"

// A snapshot handed out of the cache must not share memory a later snapshot writes to.
func TestCachedSnapshotIsNotAliased(t *testing.T) {
	n := &Node{cfg: Config{ReportInterval: 100 * 1e6}}
	h := newPackHub(n)
	n.pack = h
	in := PackSnapshot{Leader: "a", Authorities: []AuthorityInfo{{Name: "a"}, {Name: "b"}}}
	h.setCached(in)
	in.Authorities[0].Name = "changed" // the caller's slice is its own
	if h.cached.Authorities[0].Name != "a" {
		t.Fatal("setCached kept the caller's slice")
	}
	first := cloneSnapshot(*h.cached)
	second := cloneSnapshot(*h.cached)
	second.Authorities[0].Local = true
	if first.Authorities[0].Local || h.cached.Authorities[0].Local {
		t.Fatal("a copy shares its authorities with the cache")
	}
}
