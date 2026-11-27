package rux

import "time"

// roxGuest is an existing guest for the "where" step and the monitor.
type roxGuest struct {
	Name     string `json:"name"`
	Backend  string `json:"backend"`
	Phase    string `json:"phase"`
	Projects string `json:"projects"`
	Tailnet  string `json:"tailnet"`
}

func listRoxGuests() ([]roxGuest, error) {
	var out struct {
		Guests []roxGuest `json:"guests"`
	}
	err := runJSON(30*time.Second, &out, "rox", "ls", "--json", "--all-projects")
	return out.Guests, err
}
