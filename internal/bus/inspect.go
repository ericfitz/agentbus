package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"
)

const hookOutputLimit = 64 * 1024

// hookWaitDelay bounds how long cmd.Wait waits for a killed hook's I/O
// pipes to close after the inspection deadline fires, so a hook that
// ignores its context deadline (e.g. spawns a grandchild holding the pipe
// open) cannot hang the caller past the configured timeout.
const hookWaitDelay = 1 * time.Second

// capped is a writer that fails once more than limit bytes are written.
type capped struct {
	buf   bytes.Buffer
	limit int
}

var errHookOutput = errors.New("hook output exceeded 64 KiB")

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		return 0, errHookOutput
	}
	return c.buf.Write(p)
}

// inspect runs the configured hook, if any, and translates its decision.
// inspectCalls is incremented on every call, whether or not a hook is
// configured, so tests can assert the hook is (or isn't) reached (C1).
func (b *Bus) inspect(kind, as string, payload any) error {
	b.inspectCalls.Add(1)
	argv := b.cfg.InspectionCommand
	if len(argv) == 0 {
		return nil
	}
	in, err := json.Marshal(map[string]any{"operation": kind, "sender": as, "payload": payload})
	if err != nil {
		b.log.Warn("inspection hook payload could not be marshaled", "operation", kind, "sender", as, "err", err)
		return errf("inspection_unavailable", true, "failed to prepare inspection payload: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(b.cfg.InspectionTimeoutSeconds*float64(time.Second)))
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = hookWaitDelay
	cmd.Stdin = bytes.NewReader(in)
	stdout := &capped{limit: hookOutputLimit}
	stderr := &capped{limit: hookOutputLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	if err := cmd.Start(); err != nil {
		b.log.Warn("inspection hook failed to start", "operation", kind, "sender", as, "err", err)
		return errf("inspection_unavailable", true, "inspection hook failed to start: %v", err)
	}
	err = cmd.Wait()
	switch {
	case ctx.Err() != nil:
		b.log.Warn("inspection hook timed out", "operation", kind, "sender", as)
		return errf("inspection_unavailable", true, "inspection hook timed out after %vs", b.cfg.InspectionTimeoutSeconds)
	case err != nil:
		b.log.Warn("inspection hook failed", "operation", kind, "sender", as, "err", err)
		return errf("inspection_unavailable", true, "inspection hook failed: %v", err)
	}
	var d struct {
		Allow  bool   `json:"allow"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.buf.Bytes()), &d); err != nil {
		b.log.Warn("inspection hook returned malformed output", "operation", kind, "sender", as)
		return errf("inspection_unavailable", true, "inspection hook returned malformed output")
	}
	if !d.Allow {
		b.log.Info("inspection rejected", "operation", kind, "sender", as, "reason", d.Reason)
		return errf("inspection_rejected", false, "rejected by inspection hook: %s", d.Reason)
	}
	return nil
}
