package quorum

import "net/http"

// Authenticate returns the member named by r's bearer credential (the one members send with
// every request to /quorum/*). The authority uses it for the endpoints it adds to the quorum
// listener, such as the proxy to a thread's owning authority.
func (n *Node) Authenticate(r *http.Request) (string, bool) { return n.authenticate(r) }

// MemberURL is the quorum URL of a member.
func (n *Node) MemberURL(name string) (string, bool) {
	m, ok := n.memberByName(name)
	return m.URL, ok
}

// AuthHeader is the Authorization header this authority sends to other members.
func (n *Node) AuthHeader() http.Header { return n.dialHeader() }
