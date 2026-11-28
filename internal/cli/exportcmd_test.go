package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

var exportKeys = []string{"channel", "revision", "sender", "created_at", "subject", "content", "tags",
	"memory_id", "accessed_at", "served_count", "used_count", "used_at", "grace_from", "grace_served_base",
	"incorrect_at", "incorrect_by", "incorrect_served_base", "pruned_at", "pruned_reason"}

// exportEnv seeds two memories through a peer bus and returns their ids.
func exportEnv(t *testing.T) (cfg config.Config, ids []int64) {
	t.Helper()
	cfg = statusEnv(t)
	p := peerBus(t, cfg, "Mem")
	if _, err := p.CreateChannel("Mem", "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"one", "two"} {
		r, err := p.Send("Mem", bus.SendInput{Channel: "mem", Content: c})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, *r.MemoryID)
	}
	return cfg, ids
}

func exportLines(t *testing.T, cfg config.Config, pruned bool) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := ExportMemories(cfg, pruned, &out); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		got = append(got, m)
	}
	return got
}

func TestExportMemoriesWritesOneObjectPerLine(t *testing.T) {
	cfg, _ := exportEnv(t)
	got := exportLines(t, cfg, false)
	if len(got) != 2 {
		t.Fatalf("lines = %d, want 2", len(got))
	}
	for _, m := range got {
		if len(m) != len(exportKeys) {
			t.Errorf("keys = %d, want %d: %v", len(m), len(exportKeys), m)
		}
		for _, k := range exportKeys {
			if _, ok := m[k]; !ok {
				t.Errorf("missing key %q", k)
			}
		}
		if v, ok := m["used_at"]; !ok || v != nil {
			t.Errorf("used_at = %v, want present nil", v)
		}
	}
}

func TestExportMemoriesPrunedOnly(t *testing.T) {
	cfg, ids := exportEnv(t)
	dsn, err := bus.SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("UPDATE memory_access SET pruned_at=1, pruned_reason='noise' WHERE memory_id=?", ids[1]); err != nil {
		t.Fatal(err)
	}
	got := exportLines(t, cfg, true)
	if len(got) != 1 || got[0]["memory_id"] != float64(ids[1]) || got[0]["pruned_reason"] != "noise" {
		t.Fatalf("pruned output %v", got)
	}
}
