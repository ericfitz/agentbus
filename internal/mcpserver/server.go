// Package mcpserver exposes the bus as MCP tools over stdio.
package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ericfitz/agentbus-local/internal/bus"
	"github.com/ericfitz/agentbus-local/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.1.0"

type registerIn struct {
	Name    string `json:"name" jsonschema:"persistent identity name, for example Sam"`
	Parent  string `json:"parent,omitempty" jsonschema:"display name of the parent identity when registering a subagent"`
	Context string `json:"context,omitempty" jsonschema:"repository hint; defaults to the working directory basename"`
	Resume  *bool  `json:"resume,omitempty" jsonschema:"default true; false drops this name's existing subscriptions"`
}

// asIn field is deliberately omitempty even though every non-register tool
// requires it: this lets a missing/empty as reach the bus's own auth check,
// which already returns a validation error naming the requirement, instead
// of a raw JSON-schema rejection with a different shape.
type asIn struct {
	As string `json:"as,omitempty" jsonschema:"your display name as returned by register"`
}
type channelIn struct {
	As   string `json:"as,omitempty"`
	Name string `json:"name"`
	Kind string `json:"kind" jsonschema:"ordinary or memory"`
}
type subscribeIn struct {
	As      string `json:"as,omitempty"`
	Channel string `json:"channel"`
	From    string `json:"from,omitempty" jsonschema:"now (default) or oldest"`
}
type unsubscribeIn struct {
	As      string `json:"as,omitempty"`
	Channel string `json:"channel"`
}
type sendIn struct {
	As string `json:"as,omitempty"`
	bus.SendInput
}
type receiveIn struct {
	As string `json:"as,omitempty"`
	bus.ReceiveInput
}
type historyIn struct {
	As      string `json:"as,omitempty"`
	Channel string `json:"channel"`
	Before  *int64 `json:"before,omitempty"`
	After   *int64 `json:"after,omitempty"`
	Count   int    `json:"count,omitempty"`
}
type searchIn struct {
	As string `json:"as,omitempty"`
	bus.SearchInput
}
type memoryIn struct {
	As string `json:"as,omitempty"`
	ID int64  `json:"id"`
}
type editIn struct {
	As string `json:"as,omitempty"`
	bus.EditInput
}
type deleteIn struct {
	As             string `json:"as,omitempty"`
	ID             int64  `json:"id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// result marshals v as the tool's text content. On error it returns the
// error itself: AddTool's generated handler treats a returned error as a
// tool failure, setting CallToolResult.IsError and using the error's own
// Error() text as content (see go-sdk mcp.CallToolResult.SetError) — for a
// *bus.Error that text is already the {code,message,retryable} JSON the
// agent needs, so no separate error-mapping step is required here.
func result(v any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	j, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(j)}}}, nil, nil
}

// NewServer registers every tool on a server without a transport.
func NewServer(b *bus.Bus, cfg config.Config) *mcp.Server {
	return newServer(b, cfg, nil)
}

// defaultContextFor computes register's default context from os.Getwd's
// result. On error it falls back to "." explicitly and, when log is
// non-nil, logs a warning naming the error.
func defaultContextFor(cwd string, err error, log *slog.Logger) string {
	if err != nil {
		if log != nil {
			log.Warn("os.Getwd failed; defaulting register context to \".\"", "err", err)
		}
		return "."
	}
	return filepath.Base(cwd)
}

// newServer is NewServer plus an optional logger, used by Run so a Getwd
// failure is recorded in the log instead of silently defaulting.
func newServer(b *bus.Bus, cfg config.Config, log *slog.Logger) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "agentbus", Version: Version}, nil)
	cwd, err := os.Getwd()
	defaultContext := defaultContextFor(cwd, err, log)

	mcp.AddTool(s, &mcp.Tool{Name: "register", Description: "Agentbus: register your identity for this session. Returns the display name to pass as `as` on every other Agentbus call, plus pending message counts if the name was resumed."},
		func(ctx context.Context, req *mcp.CallToolRequest, in registerIn) (*mcp.CallToolResult, any, error) {
			c := in.Context
			if c == "" {
				c = defaultContext
			}
			resume := in.Resume == nil || *in.Resume
			return result(b.Register(in.Name, in.Parent, c, resume))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "create_channel", Description: "Agentbus: create a named channel of kind ordinary or memory. Idempotent when the kind matches."},
		func(ctx context.Context, req *mcp.CallToolRequest, in channelIn) (*mcp.CallToolResult, any, error) {
			return result(b.CreateChannel(in.As, in.Name, in.Kind))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_channels", Description: "Agentbus: list all channels with kind, message count, and latest sequence."},
		func(ctx context.Context, req *mcp.CallToolRequest, in asIn) (*mcp.CallToolResult, any, error) {
			return result(b.ListChannels(in.As))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "subscribe", Description: "Agentbus: subscribe to a channel so receive returns its messages. from=now (default) starts at the current position; from=oldest starts at the oldest retained message."},
		func(ctx context.Context, req *mcp.CallToolRequest, in subscribeIn) (*mcp.CallToolResult, any, error) {
			return result(map[string]any{"subscribed": in.Channel}, b.Subscribe(in.As, in.Channel, in.From))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "unsubscribe", Description: "Agentbus: unsubscribe from a channel and drop its cursor."},
		func(ctx context.Context, req *mcp.CallToolRequest, in unsubscribeIn) (*mcp.CallToolResult, any, error) {
			return result(map[string]any{"unsubscribed": in.Channel}, b.Unsubscribe(in.As, in.Channel))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "send", Description: "Agentbus: send a message to a channel. On a memory channel this creates a memory and returns its memory_id. Use idempotency_key to make retries safe."},
		func(ctx context.Context, req *mcp.CallToolRequest, in sendIn) (*mcp.CallToolResult, any, error) {
			return result(b.Send(in.As, in.SendInput))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "receive", Description: "Agentbus: receive new messages on your subscribed channels. Pass ack with the batch token from your previous receive to acknowledge it; an unacknowledged batch is redelivered. wait_seconds waits for messages when none are available."},
		func(ctx context.Context, req *mcp.CallToolRequest, in receiveIn) (*mcp.CallToolResult, any, error) {
			return result(b.Receive(in.As, in.ReceiveInput))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "history", Description: "Agentbus: read a channel's retained messages by sequence range without touching your cursor."},
		func(ctx context.Context, req *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, any, error) {
			return result(b.History(in.As, in.Channel, in.Before, in.After, in.Count))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "search", Description: "Agentbus: search messages and memories. mode=text matches words; mode=semantic ranks memories by meaning; mode=both (default when embeddings are configured) fuses them. Filters: channel, sender, since, until (unix ms), thread (a seq)."},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
			return result(b.Search(in.As, in.SearchInput))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_memory", Description: "Agentbus: get the current revision of a memory by memory_id."},
		func(ctx context.Context, req *mcp.CallToolRequest, in memoryIn) (*mcp.CallToolResult, any, error) {
			return result(b.GetMemory(in.As, in.ID))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "edit_memory", Description: "Agentbus: replace a memory's content as a new revision. Last committed write wins; the result names the revision you replaced."},
		func(ctx context.Context, req *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, any, error) {
			return result(b.EditMemory(in.As, in.EditInput))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "delete_memory", Description: "Agentbus: delete a memory by memory_id. Its content stops appearing in get, receive, and search."},
		func(ctx context.Context, req *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, any, error) {
			return result(map[string]any{"deleted": in.ID}, b.DeleteMemory(in.As, in.ID, in.IdempotencyKey))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "discover", Description: "Agentbus: list live registered identities with their repository context."},
		func(ctx context.Context, req *mcp.CallToolRequest, in asIn) (*mcp.CallToolResult, any, error) {
			return result(b.Discover(in.As))
		})
	return s
}

// Run serves stdio until the harness closes the connection. Heartbeats and
// maintenance run in the background for the life of the process. Never
// writes to stdout or stderr; all diagnostics go to the log file.
func Run(ctx context.Context, cfg config.Config) error {
	log, err := OpenLog(cfg)
	if err != nil {
		return err
	}
	b, err := bus.Open(cfg, log)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := b.Close(); cerr != nil {
			log.Warn("bus close failed", "err", cerr)
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	// wg.Wait must run after cancel() but before b.Close(): otherwise a
	// Heartbeat or Tick in flight when the client disconnects can execute
	// against a closed bus. Defers run LIFO, so registering wg.Wait before
	// cancel's defer makes cancel fire first, then wg.Wait, then the
	// b.Close deferred above.
	wg := startBackgroundLoops(ctx, b, cfg, log, 10*time.Second)
	defer wg.Wait()
	defer cancel()
	log.Info("agentbus mcp started", "version", Version, "config", cfg.Path, "data", cfg.DataDirectory)
	err = newServer(b, cfg, log).Run(ctx, &mcp.StdioTransport{})
	if err != nil {
		log.Error("server run failed", "err", err)
	}
	return err
}

// startBackgroundLoops starts the heartbeat and maintenance-tick goroutines
// and returns a WaitGroup that completes once both have exited. Both loops
// exit promptly when ctx is canceled; the caller must wg.Wait() after
// canceling ctx and before closing b, or an in-flight Heartbeat/Tick call
// can run against a closed bus.
func startBackgroundLoops(ctx context.Context, b *bus.Bus, cfg config.Config, log *slog.Logger, heartbeatEvery time.Duration) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := b.Heartbeat(); err != nil {
					log.Warn("heartbeat failed", "err", err)
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		interval := time.Duration(cfg.CleanupIntervalSeconds) * time.Second
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(rand.Int64N(int64(interval)))):
		}
		for {
			b.Tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
	return &wg
}
