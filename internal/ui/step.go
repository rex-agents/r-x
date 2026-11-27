package ui

import (
	"fmt"
	"strings"
	"time"
)

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Step is one unit of work shown as a spinner, then as a single result line.
type Step struct {
	title   string
	note    string
	started time.Time
	frame   int
	stop    chan struct{}
	stopped chan struct{}
	ended   bool
	parent  *Step
}

// Start shows a spinner for work that is under way. Finish it with Done, Skip, Warn or Fail.
// Without a terminal, nothing prints until the step finishes.
func Start(format string, args ...any) *Step {
	s := &Step{title: fmt.Sprintf(format, args...), started: time.Now()}
	mu.Lock()
	defer mu.Unlock()
	if active != nil {
		// Nested steps take over the spinner and give it back when they end.
		active.pauseLocked()
		s.parent = active
	}
	active = s
	if fancy {
		s.stop, s.stopped = make(chan struct{}), make(chan struct{})
		drawSpinnerLocked()
		go s.spin()
	}
	return s
}

// Run runs fn as a step, finishing it with done on success.
func Run(title, done string, fn func(s *Step) error) error {
	s := Start("%s", title)
	if err := fn(s); err != nil {
		s.Fail(err)
		return err
	}
	if !s.ended {
		s.Done("%s", done)
	}
	return nil
}

func (s *Step) spin() {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	defer close(s.stopped)
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			mu.Lock()
			if active == s {
				s.frame++
				drawSpinnerLocked()
			}
			mu.Unlock()
		}
	}
}

// Note changes the dim text after the step's title, such as what it's waiting for.
func (s *Step) Note(text string) {
	mu.Lock()
	defer mu.Unlock()
	s.note = text
	if active == s {
		drawSpinnerLocked()
	}
}

// Title changes what the step says it is doing.
func (s *Step) Title(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	s.title = fmt.Sprintf(format, args...)
	if active == s {
		drawSpinnerLocked()
	}
}

// Elapsed is how long the step has run.
func (s *Step) Elapsed() time.Duration { return time.Since(s.started) }

func (s *Step) finish(icon, text string, timed bool) {
	mu.Lock()
	if s.ended {
		mu.Unlock()
		return
	}
	s.ended = true
	s.stopLocked()
	clearSpinnerLocked()
	line := "  " + icon + " " + text
	if timed && time.Since(s.started) >= 500*time.Millisecond {
		line = padRight(line, 56) + "  " + styleFaint.Render(Duration(time.Since(s.started)))
	}
	fmt.Fprintln(errOut, line)
	if active == s {
		active = s.parent
		if active != nil {
			active.resumeLocked()
		}
	}
	drawSpinnerLocked()
	mu.Unlock()
}

// Done finishes the step as a success.
func (s *Step) Done(format string, args ...any) {
	s.finish(styleGood.Render(iconDone), fmt.Sprintf(format, args...), true)
}

// Skip finishes a step that turned out to have nothing to do.
func (s *Step) Skip(format string, args ...any) {
	s.finish(styleFaint.Render(iconSkip), styleMuted.Render(fmt.Sprintf(format, args...)), false)
}

// Warn finishes a step that had a problem the command can live with.
func (s *Step) Warn(format string, args ...any) {
	s.finish(styleWarn.Render(iconWarn), styleWarn.Render(fmt.Sprintf(format, args...)), true)
}

// Fail finishes the step as a failure. The command's error is reported separately.
func (s *Step) Fail(err error) {
	s.finish(styleBad.Render(iconFail), s.title, true)
}

// End finishes the step silently, leaving no line behind.
func (s *Step) End() {
	mu.Lock()
	defer mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	s.stopLocked()
	clearSpinnerLocked()
	if active == s {
		active = s.parent
		if active != nil {
			active.resumeLocked()
		}
	}
	drawSpinnerLocked()
}

func (s *Step) stopLocked() {
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
}

func (s *Step) pauseLocked() {}

func (s *Step) resumeLocked() {}

func clearSpinnerLocked() {
	if fancy && active != nil && !active.ended {
		fmt.Fprint(errOut, "\r\x1b[2K")
	}
}

func drawSpinnerLocked() {
	s := active
	if !fancy || s == nil || s.ended {
		return
	}
	frame := styleAccent.Render(frames[s.frame%len(frames)])
	line := "  " + frame + " " + s.title
	if s.note != "" {
		line += "  " + styleMuted.Render(s.note)
	}
	if elapsed := time.Since(s.started); elapsed >= 2*time.Second {
		line += "  " + styleFaint.Render(Duration(elapsed))
	}
	fmt.Fprint(errOut, "\r\x1b[2K"+truncateVisible(line, width()))
}

// Suspend clears the spinner while fn talks to the terminal itself, such as a prompt.
func Suspend(fn func() error) error {
	mu.Lock()
	clearSpinnerLocked()
	saved := active
	active = nil
	mu.Unlock()
	defer func() {
		mu.Lock()
		active = saved
		drawSpinnerLocked()
		mu.Unlock()
	}()
	return fn()
}

// Stream prints a line of captured subprocess output in verbose mode.
func Stream(line string) {
	if !verbose {
		return
	}
	writeLine("    " + styleFaint.Render("┆ "+truncate(StripANSI(strings.TrimRight(line, "\r\n")), 200)))
}

func padRight(s string, n int) string {
	visible := len([]rune(StripANSI(s)))
	if visible >= n {
		return s
	}
	return s + strings.Repeat(" ", n-visible)
}

func truncateVisible(s string, n int) string {
	if n <= 0 || len([]rune(StripANSI(s))) <= n {
		return s
	}
	// Styling makes byte positions unreliable, so fall back to the plain text.
	plain := []rune(StripANSI(s))
	return string(plain[:n-1]) + "…"
}
