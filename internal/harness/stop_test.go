package harness

import (
	"context"
	"testing"
	"time"
)

func TestStopIsStickyAndRejectsMessages(t *testing.T) {
	a := NewAgent("a", "a", "sub")
	a.Send("lead", "task")
	a.Stop()
	if st, _ := a.Status(); st != Stopped {
		t.Fatalf("status = %s", st)
	}
	// Provisioning finishing later can't revive it.
	a.Attach(&scripted{}, "", nil, "guest")
	a.SetStatus(Failed, "late failure")
	a.SetStatus(Waiting, "")
	if st, _ := a.Status(); st != Stopped {
		t.Errorf("revived to %s", st)
	}
	if err := a.TrySend("lead", "hi"); err == nil {
		t.Error("TrySend to a stopped agent succeeded")
	}
	if !a.Terminal() {
		t.Error("stopped agent isn't terminal")
	}
}

type blockingProvider struct{ scripted }

func (b *blockingProvider) New(string) Conversation { return b }
func (b *blockingProvider) Step(ctx context.Context, _ []ToolSpec, _ func(Delta)) (Turn, error) {
	<-ctx.Done()
	return Turn{}, ctx.Err()
}

func TestStopDuringTurnStaysStopped(t *testing.T) {
	a := NewAgent("a", "a", "sub")
	ended := make(chan struct{}, 1)
	a.OnTurnEnd = func(*Agent, string, error) { ended <- struct{}{} }
	a.Attach(&blockingProvider{}, "", nil, "guest")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	a.Send("lead", "go")
	for end := time.Now().Add(5 * time.Second); !a.Busy(); {
		if time.Now().After(end) {
			t.Fatal("never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.Stop()
	time.Sleep(200 * time.Millisecond)
	if st, _ := a.Status(); st != Stopped {
		t.Errorf("status = %s, want stopped", st)
	}
	select {
	case <-ended:
		t.Error("OnTurnEnd reported an interrupt for a stopped agent")
	default:
	}
}
