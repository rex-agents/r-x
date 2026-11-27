package contract

import (
	"encoding/json"
	"testing"
)

func TestWantCredentials(t *testing.T) {
	no := false
	for _, c := range []struct {
		l    *Launch
		want bool
	}{{nil, true}, {&Launch{}, true}, {&Launch{Credentials: &no}, false}} {
		if got := c.l.WantCredentials(); got != c.want {
			t.Errorf("%+v: %v", c.l, got)
		}
	}
	var l Launch
	if err := json.Unmarshal([]byte(`{"credentials":false}`), &l); err != nil || l.WantCredentials() {
		t.Errorf("credentials:false should decline credentials: %+v %v", l, err)
	}
}
