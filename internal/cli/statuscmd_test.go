package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// statusEnv has three live sessions with statuses set at distinct times.
func statusEnv(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	base := time.Now()
	for i, s := range []struct{ name, tag, text string }{
		{"Cy", "planning", "map it"}, {"Al", "implementing", "build it"}, {"Bo", "planning", "think"},
	} {
		p := peerBus(t, cfg, s.name)
		p.Now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
		if _, err := p.SetStatus(s.name, s.tag, s.text, ""); err != nil {
			t.Fatal(err)
		}
		p.Now = time.Now
	}
	peerBus(t, cfg, "Zed") // live, never set
	return cfg
}

func names(out string) []string {
	var n []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		f := strings.Fields(l)
		n = append(n, f[1])
	}
	return n
}

func TestSetStatusCmdJoinsWords(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	peerBus(t, cfg, "Pat")
	var out bytes.Buffer
	if err := SetStatusCmd(cfg, "Pat", t.TempDir(), "implementing", "fix the board", "", &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if out.String() != "status for Pat: revision 1\n" {
		t.Fatalf("output %q", out.String())
	}
	b, _ := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = b.Close() }()
	if st, _ := b.LatestStatus("Pat"); st == nil || st.Text != "fix the board" || st.Tag != "implementing" {
		t.Fatalf("stored %+v", st)
	}
	if err := SetStatusCmd(cfg, "Pat", t.TempDir(), "offline", "x", "", &out, io.Discard); err == nil {
		t.Fatal("offline tag must be rejected")
	}
}

func TestStatusesTable(t *testing.T) {
	cfg := statusEnv(t)
	for _, c := range []struct {
		sort string
		want string
	}{
		{"", "Bo Al Cy Zed"},
		{"time", "Bo Al Cy Zed"},
		{"name", "Al Bo Cy Zed"},
		{"tag", "Al Bo Cy Zed"}, // implementing < planning (Bo, Cy by name) < — (never set)
	} {
		var out bytes.Buffer
		if err := Statuses(cfg, "", c.sort, false, &out); err != nil {
			t.Fatal(err)
		}
		// The board shows the newest per identity: Bo set last (2m), then Al (1m), Cy (0m).
		if got := strings.Join(names(out.String()), " "); got != c.want {
			t.Fatalf("sort %q: %q, want %q\n%s", c.sort, got, c.want, out.String())
		}
		if !strings.Contains(out.String(), "—") {
			t.Fatalf("never-set tag must print —:\n%s", out.String())
		}
	}
}

func TestStatusesBadSort(t *testing.T) {
	cfg := statusEnv(t)
	err := Statuses(cfg, "", "age", false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "time, name, tag") {
		t.Fatalf("got %v", err)
	}
	if SortBoard(nil, "age") == nil {
		t.Fatal("SortBoard must reject an unknown mode")
	}
}

func TestStatusesJSON(t *testing.T) {
	cfg := statusEnv(t)
	var out bytes.Buffer
	if err := Statuses(cfg, "", "name", true, &out); err != nil {
		t.Fatal(err)
	}
	var got []bus.BoardEntry
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 4 || got[0].Identity != "Al" || !got[0].Live {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestStatusesHistory(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	p := peerBus(t, cfg, "Pat")
	for _, text := range []string{"first", "second"} {
		if _, err := p.SetStatus("Pat", "planning", text, ""); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := Statuses(cfg, "Pat", "", false, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if i, j := strings.Index(s, "first"), strings.Index(s, "second"); i < 0 || j < i {
		t.Fatalf("oldest first expected:\n%s", s)
	}
	// Each entry shows its revision and when it was set (#46).
	hist, _, err := p.StatusHistory("Pat")
	if err != nil || len(hist) != 2 {
		t.Fatalf("history %+v %v", hist, err)
	}
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "REV") || !strings.Contains(lines[0], "TIME") {
		t.Fatalf("want header plus two rows:\n%s", s)
	}
	for i, h := range hist {
		at := time.UnixMilli(h.SetAt).Local().Format("2006-01-02 15:04:05")
		if f := strings.Fields(lines[i+1]); len(f) < 4 || f[0] != strconv.FormatInt(h.Revision, 10) || f[1]+" "+f[2] != at {
			t.Fatalf("row %d = %q, want revision %d at %s", i, lines[i+1], h.Revision, at)
		}
	}
}

func TestStatusesBadSortWithName(t *testing.T) {
	cfg := statusEnv(t)
	for _, name := range []string{"", "Al"} {
		if err := Statuses(cfg, name, "age", false, io.Discard); err == nil || !strings.Contains(err.Error(), "time, name, tag") {
			t.Fatalf("name %q: got %v", name, err)
		}
	}
}
