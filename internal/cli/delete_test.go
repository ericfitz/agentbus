package cli

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

func TestDeleteChannelConfirms(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureChannel("scratch", "memory"); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, ".local"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ".local", "agentbus.json"), []byte(`{"identity":"x","channels":["scratch"]}`), 0o600)

	var out bytes.Buffer
	if err := DeleteChannel(cfg, root, "scratch", false, strings.NewReader("n\n"), &out); err == nil {
		t.Fatal("must cancel without y")
	}
	for _, want := range []string{"PERMANENTLY", "memories", "[y/N]", ".local/agentbus.json"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("prompt lacks %q:\n%s", want, out.String())
		}
	}
	if err := DeleteChannel(cfg, root, "general", true, nil, &out); err == nil || !strings.Contains(err.Error(), "default channel") {
		t.Fatalf("default channel: %v", err)
	}
	out.Reset()
	if err := DeleteChannel(cfg, root, "scratch", true, nil, &out); err != nil || !strings.Contains(out.String(), "deleted channel") {
		t.Fatalf("%q %v", out.String(), err)
	}
	if err := DeleteChannel(cfg, root, "scratch", true, nil, &out); err == nil {
		t.Fatal("second delete must report not found")
	}
}
