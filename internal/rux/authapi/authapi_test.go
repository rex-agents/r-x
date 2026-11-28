package authapi

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func keys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The web app mirrors these names: they are the contract.
func TestWireFieldNames(t *testing.T) {
	cases := []struct {
		what string
		v    any
		want []string
	}{
		{"GuestInfo", GuestInfo{Name: "g", Phase: "running", Backend: "container", MemLimit: 1, Thread: "t"},
			[]string{"backend", "cpu", "mem_bytes", "mem_limit", "name", "phase", "thread"}},
		{"GuestInfo (omitempty)", GuestInfo{Name: "g", Phase: "stopped"}, []string{"cpu", "mem_bytes", "name", "phase"}},
		{"ClientInfo", ClientInfo{}, []string{"authority", "device", "id", "kind", "last_seen", "since"}},
		{"Pack", Pack{}, []string{"authorities", "clients", "leader", "term", "vault_host", "vault_ok"}},
		{"AuthorityInfo with guests", AuthorityInfo{Guests: []GuestInfo{{}}}, []string{"guests", "host", "local", "name", "status", "url"}},
		{"ThreadSummary manager", ThreadSummary{Kind: "manager"}, []string{"authority", "cost", "id", "kind", "placement", "status", "subagents", "title", "updated"}},
		{"ThreadSummary worker", ThreadSummary{}, []string{"authority", "cost", "id", "placement", "status", "subagents", "title", "updated"}},
	}
	for _, c := range cases {
		if got := keys(t, c.v); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: JSON keys %v, want %v", c.what, got, c.want)
		}
	}
	if EventPackClients != "pack.clients" {
		t.Errorf("event name %q", EventPackClients)
	}
}
