package rux

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The overview ("monitor" screen) combines host, guest, device, viewer and usage
// data into one snapshot, pushed over SSE every few seconds while anyone watches.

type ovHost struct {
	Name     string    `json:"name"`
	CPUs     int       `json:"cpus"`
	MemBytes int64     `json:"mem_bytes"`
	Load     []float64 `json:"load"`
}

type ovGuest struct {
	Name        string  `json:"name"`
	Phase       string  `json:"phase"`
	IP          string  `json:"ip"`
	Project     string  `json:"project"`
	CPU         float64 `json:"cpu"` // percent
	MemBytes    int64   `json:"mem_bytes"`
	MemLimit    int64   `json:"mem_limit"`
	Procs       int     `json:"procs"`
	ThreadID    string  `json:"thread_id,omitempty"`
	ThreadTitle string  `json:"thread_title,omitempty"`
	Model       string  `json:"model,omitempty"`
	Status      string  `json:"status,omitempty"`
	Connected   bool    `json:"connected"`
	Streaming   bool    `json:"streaming"` // runner sent an event in the last few seconds
	Managed     bool    `json:"managed"`   // belongs to a rux thread
}

type ovDevice struct {
	Name    string `json:"name"`
	OS      string `json:"os"`
	Online  bool   `json:"online"`
	Self    bool   `json:"self"`
	Viewing bool   `json:"viewing"` // has the app open (requested it recently)
}

type ovWindow struct {
	Name        string  `json:"name"`
	Utilization float64 `json:"utilization"`
	ResetsAt    int64   `json:"resets_at"`
}

type ovUsage struct {
	Windows      []ovWindow `json:"windows"`
	TodayTurns   int        `json:"today_turns"`
	TodayCost    float64    `json:"today_cost"`
	TodayTools   int        `json:"today_tools"`
	TodayThreads int        `json:"today_threads"`
	Hourly       []int      `json:"hourly"` // tool calls per hour, oldest first, last 24h
}

type overview struct {
	At       int64          `json:"at"`
	Host     ovHost         `json:"host"`
	Guests   []ovGuest      `json:"guests"`
	Devices  []ovDevice     `json:"devices"`
	Usage    ovUsage        `json:"usage"`
	Statuses map[string]int `json:"statuses"`
	Other    []ovGuest      `json:"other"` // containers not owned by rux (e.g. buildkit)
}

// viewer tracking: tailnet IPs that requested the app recently.
type viewers struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (v *viewers) note(r *http.Request) {
	ip := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	if ip == "" {
		return
	}
	v.mu.Lock()
	if v.seen == nil {
		v.seen = map[string]time.Time{}
	}
	v.seen[ip] = time.Now()
	v.mu.Unlock()
}

func (v *viewers) active(within time.Duration) map[string]bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]bool{}
	for ip, t := range v.seen {
		if time.Since(t) < within {
			out[ip] = true
		}
	}
	return out
}

func runJSON(timeout time.Duration, v any, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

func tailscaleBin() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	return "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
}

func (s *server) hostInfo() ovHost { return hostInfo() }

// hostInfo reads this machine's name, CPUs, memory and load.
func hostInfo() ovHost {
	h := ovHost{}
	if b, err := exec.Command("scutil", "--get", "LocalHostName").Output(); err == nil {
		h.Name = strings.TrimSpace(string(b))
	} else {
		h.Name, _ = os.Hostname()
	}
	if b, err := exec.Command("sysctl", "-n", "hw.ncpu", "hw.memsize", "vm.loadavg").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) >= 3 {
			h.CPUs, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
			h.MemBytes, _ = strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
			for _, f := range strings.Fields(strings.Trim(lines[2], "{} ")) {
				if x, err := strconv.ParseFloat(f, 64); err == nil {
					h.Load = append(h.Load, x)
				}
			}
		}
	}
	return h
}

func (s *server) buildOverview() overview {
	ov := overview{At: time.Now().UnixMilli(), Statuses: map[string]int{}}
	var wg sync.WaitGroup
	// rox ls --json prints {"schema": 1, "guests": [...]}.
	var roxLs struct {
		Guests []struct {
			Name, Phase, IP, Projects string
		} `json:"guests"`
	}
	stats := s.containerStats()
	var ts struct {
		Self struct {
			HostName, DNSName, OS string
			TailscaleIPs          []string
			UserID                int64
		}
		Peer map[string]struct {
			HostName, DNSName, OS string
			Online                bool
			TailscaleIPs          []string
			UserID                int64
		}
		User map[string]struct{ LoginName string }
	}
	wg.Add(3)
	go func() { defer wg.Done(); ov.Host = s.hostInfo() }()
	go func() { defer wg.Done(); runJSON(10*time.Second, &roxLs, s.roxBin, "ls", "--all-projects", "--json") }()
	go func() { defer wg.Done(); runJSON(5*time.Second, &ts, tailscaleBin(), "status", "--json") }()
	wg.Wait()

	// threads: statuses, guest ownership, usage
	s.mu.Lock()
	ths := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		ths = append(ths, t)
	}
	s.mu.Unlock()
	byGuest := map[string]*thread{}
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	hourStart := now.Truncate(time.Hour).Add(-23 * time.Hour).UnixMilli()
	ov.Usage.Hourly = make([]int, 24)
	var latestRL int64
	windows := map[string]ovWindow{}
	streaming := map[string]bool{}
	for _, t := range ths {
		t.mu.Lock()
		m := t.meta
		ov.Statuses[m.Status]++
		if m.Guest != "" {
			byGuest[m.Guest] = t
		}
		if m.Created.UnixMilli() >= dayStart {
			ov.Usage.TodayThreads++
		}
		foundRL := false
		for i := len(t.events) - 1; i >= 0; i-- {
			line := t.events[i]
			var se struct {
				TS  int64 `json:"ts"`
				Src string
				Ev  struct {
					Type    string  `json:"type"`
					Turns   int     `json:"num_turns"`
					Cost    float64 `json:"total_cost_usd"`
					Message struct {
						Content []struct{ Type string } `json:"content"`
					} `json:"message"`
					RL struct {
						Windows map[string]struct {
							Utilization float64 `json:"utilization"`
							ResetsAt    int64   `json:"resetsAt"`
						} `json:"unifiedWindows"`
					} `json:"rate_limit_info"`
				} `json:"ev"`
			}
			if json.Unmarshal(line, &se) != nil {
				continue
			}
			// Newest first: past the 24h window we only still want this thread's
			// latest rate-limit event, and nothing older than a week.
			if se.TS < hourStart {
				if foundRL || se.TS < hourStart-7*24*3600*1000 {
					break
				}
				if se.Ev.Type != "rate_limit_event" {
					continue
				}
			}
			if se.Src == "runner" && now.UnixMilli()-se.TS < 6000 {
				streaming[m.ID] = true
			}
			switch se.Ev.Type {
			case "result":
				if se.TS >= dayStart {
					ov.Usage.TodayTurns += se.Ev.Turns
					ov.Usage.TodayCost += se.Ev.Cost
				}
			case "assistant":
				for _, c := range se.Ev.Message.Content {
					if c.Type == "tool_use" {
						if h := int((se.TS - hourStart) / 3600000); h >= 0 && h < 24 {
							ov.Usage.Hourly[h]++
						}
						if se.TS >= dayStart {
							ov.Usage.TodayTools++
						}
					}
				}
			case "rate_limit_event":
				foundRL = true
				if se.TS > latestRL {
					latestRL = se.TS
					windows = map[string]ovWindow{}
					for name, w := range se.Ev.RL.Windows {
						windows[name] = ovWindow{Name: name, Utilization: w.Utilization, ResetsAt: w.ResetsAt}
					}
				}
			}
		}
		t.mu.Unlock()
	}
	for _, w := range windows {
		ov.Usage.Windows = append(ov.Usage.Windows, w)
	}
	sort.Slice(ov.Usage.Windows, func(i, j int) bool { return ov.Usage.Windows[i].Name < ov.Usage.Windows[j].Name })

	// guests: rox workspaces + container stats + owning thread
	statByID := map[string]int{}
	for i, st := range stats {
		statByID[st.ID] = i
	}
	seen := map[string]bool{}
	for _, g := range roxLs.Guests {
		og := ovGuest{Name: g.Name, Phase: g.Phase, IP: g.IP, Project: g.Projects}
		if i, ok := statByID[g.Name]; ok {
			st := stats[i]
			og.CPU, og.MemBytes, og.MemLimit, og.Procs = st.CPU*100, st.MemBytes, st.MemLimit, st.Procs
		}
		if t := byGuest[g.Name]; t != nil {
			v := t.view()
			og.ThreadID, og.ThreadTitle, og.Status, og.Connected, og.Managed = v.ID, v.Title, v.Status, v.Connected, true
			og.Model = v.Model
			og.Streaming = streaming[v.ID]
		}
		seen[g.Name] = true
		ov.Guests = append(ov.Guests, og)
	}
	for _, st := range stats {
		if !seen[st.ID] {
			ov.Other = append(ov.Other, ovGuest{Name: st.ID, CPU: st.CPU * 100, MemBytes: st.MemBytes, MemLimit: st.MemLimit, Procs: st.Procs, Phase: "running"})
		}
	}

	// devices: this user's tailnet nodes, marked if they have the app open
	active := s.viewers.active(20 * time.Second)
	login := func(uid int64) string { return ts.User[fmt.Sprint(uid)].LoginName }
	mine := func(uid int64) bool {
		return s.allowUser == "" || strings.EqualFold(login(uid), s.allowUser)
	}
	viewing := func(ips []string) bool {
		for _, ip := range ips {
			if active[ip] {
				return true
			}
		}
		return false
	}
	devName := func(host, dns string) string {
		if first, _, _ := strings.Cut(dns, "."); first != "" {
			return first
		}
		return host
	}
	// The host itself is drawn in the hosts column, so it is not listed as a device.
	for _, p := range ts.Peer {
		if !mine(p.UserID) || strings.HasSuffix(p.HostName, "-rex") {
			continue
		}
		ov.Devices = append(ov.Devices, ovDevice{Name: devName(p.HostName, p.DNSName), OS: p.OS, Online: p.Online, Viewing: viewing(p.TailscaleIPs)})
	}
	sort.Slice(ov.Devices, func(i, j int) bool {
		a, b := ov.Devices[i], ov.Devices[j]
		if a.Viewing != b.Viewing {
			return a.Viewing
		}
		if a.Online != b.Online {
			return a.Online
		}
		return a.Name < b.Name
	})
	return ov
}

type containerStat struct {
	ID       string  `json:"id"`
	CPU      float64 `json:"cpuUtilization"`
	MemBytes int64   `json:"memoryUsageBytes"`
	MemLimit int64   `json:"memoryLimitBytes"`
	Procs    int     `json:"numProcesses"`
}

// `container stats` samples CPU over ~2s, so it runs in the background while a
// monitor screen is open and snapshots read the latest result.
var (
	statsCache struct {
		sync.Mutex
		at time.Time
		v  []containerStat
	}
	monitorWatchers atomic.Int32
)

func fetchContainerStats() {
	var v []containerStat
	if err := runJSON(15*time.Second, &v, "container", "stats", "--no-stream", "--format", "json"); err != nil {
		return
	}
	statsCache.Lock()
	statsCache.v, statsCache.at = v, time.Now()
	statsCache.Unlock()
}

func (s *server) statsLoop() {
	for {
		if monitorWatchers.Load() > 0 {
			fetchContainerStats()
		}
		time.Sleep(time.Second)
	}
}

func (s *server) containerStats() []containerStat { return cachedContainerStats() }

// cachedContainerStats is the latest container stats, fetching them once if there are none.
func cachedContainerStats() []containerStat {
	statsCache.Lock()
	stale := time.Since(statsCache.at) > time.Minute
	v := statsCache.v
	statsCache.Unlock()
	if stale && v == nil {
		fetchContainerStats() // first call: block once rather than show nothing
		statsCache.Lock()
		v = statsCache.v
		statsCache.Unlock()
	}
	return v
}

var ovCache struct {
	sync.Mutex
	at time.Time
	ov overview
}

// snapshot returns a shared overview at most ~2.5s old, so several open
// monitor screens don't each shell out to rox/container/tailscale.
func (s *server) snapshot() overview {
	ovCache.Lock()
	defer ovCache.Unlock()
	if time.Since(ovCache.at) > 2500*time.Millisecond {
		ovCache.ov = s.buildOverview()
		ovCache.at = time.Now()
	}
	return ovCache.ov
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, 200, s.snapshot())
}

// handleOverviewStream pushes a fresh snapshot every 3 seconds.
func (s *server) handleOverviewStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	monitorWatchers.Add(1)
	defer monitorWatchers.Add(-1)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		s.viewers.note(r) // a watched monitor screen counts as viewing
		b, _ := json.Marshal(s.snapshot())
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
		select {
		case <-tick.C:
		case <-r.Context().Done():
			return
		}
	}
}
