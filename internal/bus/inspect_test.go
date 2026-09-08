package bus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hookScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInspectionDecisions(t *testing.T) {
	cases := []struct {
		name, body, wantCode string
	}{
		{"allow", `cat >/dev/null; echo '{"allow": true}'`, ""},
		{"deny", `cat >/dev/null; echo '{"allow": false, "reason": "no secrets"}'`, "inspection_rejected"},
		{"timeout", `sleep 3`, "inspection_unavailable"},
		{"crash", `exit 3`, "inspection_unavailable"},
		{"malformed", `echo not-json`, "inspection_unavailable"},
		{"overflow", `head -c 70000 /dev/zero | tr '\0' 'x'; echo`, "inspection_unavailable"},
		{"sees payload", `IN=$(cat); echo "$IN" | grep -q '"content":"hello"' && echo "$IN" | grep -q '"sender":"Sam"' && echo '{"allow":true}' || echo '{"allow":false,"reason":"bad input"}'`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newTestBus(t)
			b.cfg.InspectionCommand = []string{hookScript(t, c.body)}
			b.cfg.InspectionTimeoutSeconds = 0.5
			sam := reg(t, b, "Sam")
			b.CreateChannel(sam, "dev", "ordinary")
			_, err := b.Send(sam, SendInput{Channel: "dev", Content: "hello"})
			if c.wantCode == "" && err != nil {
				t.Fatal(err)
			}
			if c.wantCode != "" && (err == nil || !strings.Contains(err.Error(), c.wantCode)) {
				t.Fatalf("want %s, got %v", c.wantCode, err)
			}
			if c.name == "deny" && !strings.Contains(err.Error(), "no secrets") {
				t.Fatal("reason must be surfaced")
			}
		})
	}
}
