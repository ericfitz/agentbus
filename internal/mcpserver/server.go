// Package mcpserver exposes the bus as MCP tools over stdio.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/cli"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/repoconfig"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is the release version. Release builds override it with
// -ldflags "-X github.com/ericfitz/agentbus/internal/mcpserver.Version=<v>".
var Version = "1.8.1"

type registerIn struct {
	Name    string `json:"name" jsonschema:"persistent identity name, for example Sam"`
	Parent  string `json:"parent,omitempty" jsonschema:"display name of the parent identity when registering a subagent"`
	Context string `json:"context,omitempty" jsonschema:"repository hint; defaults to the working directory basename"`
	Resume  *bool  `json:"resume,omitempty" jsonschema:"default true; false drops this name's existing subscriptions, inbox included, so only messages sent after this register are delivered"`
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
	As         string   `json:"as,omitempty"`
	Channel    string   `json:"channel,omitempty"`
	From       string   `json:"from,omitempty" jsonschema:"now (default) or oldest"`
	Persistent bool     `json:"persistent,omitempty" jsonschema:"also add the channel to this repository's .local/agentbus.json so register subscribes it in later sessions"`
	Tags       []string `json:"tags,omitempty" jsonschema:"instead of channel: 1-10 tags that must all be on a message for it to be delivered (an AND set); subscribe again with another set for OR"`
}
type unsubscribeIn struct {
	As         string   `json:"as,omitempty"`
	Channel    string   `json:"channel,omitempty"`
	Persistent bool     `json:"persistent,omitempty" jsonschema:"also remove the channel from this repository's .local/agentbus.json"`
	Tags       []string `json:"tags,omitempty" jsonschema:"instead of channel: 1-10 tags that must all be on a message for it to be delivered (an AND set); subscribe again with another set for OR"`
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
	As      string   `json:"as,omitempty"`
	Channel string   `json:"channel"`
	Before  *int64   `json:"before,omitempty"`
	After   *int64   `json:"after,omitempty"`
	Count   int      `json:"count,omitempty"`
	Tags    []string `json:"tags,omitempty" jsonschema:"only messages carrying at least one of these tags"`
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
type taskCreateIn struct {
	As string `json:"as,omitempty"`
	bus.TaskCreateInput
}
type taskUpdateIn struct {
	As string `json:"as,omitempty"`
	bus.TaskPatch
}
type taskClaimIn struct {
	As             string `json:"as,omitempty"`
	TaskID         int64  `json:"task_id"`
	LeasedUntil    int64  `json:"leased_until,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
type taskReleaseIn struct {
	As             string `json:"as,omitempty"`
	TaskID         int64  `json:"task_id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
type taskGetIn struct {
	As     string `json:"as,omitempty"`
	TaskID int64  `json:"task_id"`
}
type taskListIn struct {
	As string `json:"as,omitempty"`
	bus.TaskListInput
}

// result marshals v as the tool's text content. On error it returns the
// error itself: AddTool's generated handler treats a returned error as a
// tool failure, setting CallToolResult.IsError and using the error's own
// Error() text as content (see go-sdk mcp.CallToolResult.SetError) — for a
// *bus.Error that text is already the {code,message,retryable} JSON the
// agent needs, so no separate error-mapping step is required here.
// result returns err unchanged: a bus error already carries its JSON
// envelope, and any non-bus error reaching here is an SDK argument error.
// Anything else must be wrapped as `internal` before being returned (ADR 0006).
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

// applyPersistent subscribes reg.Sender to the repository's persistent
// channel list (from the nearest .local/agentbus.json above cwd, or
// repoconfig.DefaultChannels when there is none) and records the outcome on
// reg. A file that cannot be read counts as absent, so register still
// succeeds; the problem is surfaced in SubscribeFailed under the key
// ".local/agentbus.json".
func applyPersistent(b *bus.Bus, cwd string, reg *bus.Registration) {
	reg.Subscribed = []string{}
	reg.MemoryChannels = []string{}
	channels := repoconfig.DefaultChannels
	f, err := repoconfig.Find(cwd)
	if err != nil {
		reg.SubscribeFailed = map[string]string{".local/agentbus.json": err.Error()}
	} else if f != nil {
		var bad []string
		channels, bad = f.Channels()
		for _, c := range bad {
			if reg.SubscribeFailed == nil {
				reg.SubscribeFailed = map[string]string{}
			}
			reg.SubscribeFailed[c] = "invalid channel name in " + f.Path
		}
	}
	for _, c := range channels {
		// A prefixed name implies its kind, so a listed project channel the
		// tick reaped while empty and unsubscribed comes back here rather
		// than failing until someone reruns agentbus init.
		if _, ok := bus.PrefixKind(c); ok {
			_ = b.EnsureChannel(c, "")
		}
		// Memory channels are searched, not pushed (ADR 0008): report them
		// and drop any subscription an earlier register left behind, so a
		// resumed identity stops receiving memories too. Task lists are
		// memory-kind but event-driven, so they stay subscribed.
		if kind, _ := b.ChannelKind(c); kind == "memory" && !bus.IsTaskChannel(c) {
			_ = b.Unsubscribe(reg.Sender, c)
			reg.MemoryChannels = append(reg.MemoryChannels, c)
			continue
		}
		if err := b.Subscribe(reg.Sender, c, "now"); err != nil {
			if reg.SubscribeFailed == nil {
				reg.SubscribeFailed = map[string]string{}
			}
			var be *bus.Error
			if errors.As(err, &be) {
				reg.SubscribeFailed[c] = be.Message
			} else {
				reg.SubscribeFailed[c] = err.Error()
			}
			continue
		}
		reg.Subscribed = append(reg.Subscribed, c)
	}
	if f != nil {
		sets, bad := f.TagSubscriptions()
		for _, s := range bad {
			if reg.SubscribeFailed == nil {
				reg.SubscribeFailed = map[string]string{}
			}
			reg.SubscribeFailed["tags "+s] = "invalid tag set in " + f.Path
		}
		for _, s := range sets {
			if err := b.SubscribeTags(reg.Sender, s); err != nil {
				if reg.SubscribeFailed == nil {
					reg.SubscribeFailed = map[string]string{}
				}
				reg.SubscribeFailed["tags "+strings.Join(s, ",")] = err.Error()
				continue
			}
			reg.TagSubscriptions = append(reg.TagSubscriptions, s)
		}
	}
}

// persistFile returns the repository file for persistent subscribe and
// unsubscribe: the nearest .local/agentbus.json above cwd, or a new one at
// the nearest git root (identity = that directory's basename) when none
// exists. Outside a git repository it fails.
func persistFile(cwd string) (*repoconfig.File, error) {
	f, err := repoconfig.Find(cwd)
	if err != nil || f != nil {
		return f, err
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return repoconfig.Create(dir, filepath.Base(dir))
		}
		if filepath.Dir(dir) == dir || dir == "" {
			return nil, errors.New("not inside a git repository; nothing to persist to")
		}
	}
}

// persistErr wraps a repo-file problem in the bus's error envelope so the
// agent sees the same {code,message,retryable} shape as every other failure.
// A filesystem failure (unreadable or unwritable file) is internal and
// retryable, matching how the bus reports its own I/O errors; a bad channel
// name, a malformed file, or no enclosing git repository is validation.
func persistErr(err error) error {
	msg := bus.TruncateErrorMessage(err.Error())
	var pe *os.PathError
	if errors.As(err, &pe) {
		return &bus.Error{Code: "internal", Message: msg, Retryable: true}
	}
	return &bus.Error{Code: "validation", Message: msg, Retryable: false}
}

// wrapSchemaErrorsInEnvelope normalizes go-sdk's own JSON-schema validation
// failures (a missing required field, a wrong-typed argument) into the same
// {code,message,retryable} JSON envelope every bus.Error already produces.
// AddTool's generated handler validates/unmarshals arguments before a tool's
// own code ever runs; when that fails it sets CallToolResult.IsError with
// plain SDK error text as content, not our envelope — so without this,
// agents see two different error shapes depending on which validation step
// rejected the call. A result whose text is already a JSON object with a
// "code" field (a real bus.Error) is left untouched.
func wrapSchemaErrorsInEnvelope(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if method != "tools/call" || err != nil {
			return res, err
		}
		ctr, ok := res.(*mcp.CallToolResult)
		if !ok || !ctr.IsError || len(ctr.Content) == 0 {
			return res, err
		}
		tc, ok := ctr.Content[0].(*mcp.TextContent)
		if !ok {
			return res, err
		}
		var probe struct {
			Code string `json:"code"`
		}
		if json.Unmarshal([]byte(tc.Text), &probe) == nil && probe.Code != "" {
			return res, err // already a bus.Error envelope
		}
		envelope, marshalErr := json.Marshal(map[string]any{
			"code":      "validation",
			"message":   bus.TruncateErrorMessage(tc.Text),
			"retryable": false,
		})
		if marshalErr != nil {
			return res, err
		}
		ctr.Content[0] = &mcp.TextContent{Text: string(envelope)}
		return ctr, err
	}
}

// newServer is NewServer plus an optional logger, used by Run so a Getwd
// failure is recorded in the log instead of silently defaulting.
func newServer(b *bus.Bus, cfg config.Config, log *slog.Logger) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "agentbus", Version: Version}, nil)
	// Claude Code lists MCP prompts as slash commands (/agentbus:init), so
	// this is the in-harness bootstrap with no files written; Codex does not
	// surface prompts yet and gets a custom prompt file from `agentbus init`.
	s.AddPrompt(&mcp.Prompt{Name: "init", Description: "Set up Agentbus for the current repository and register this session"},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: cli.InitPrompt}}}}, nil
		})
	s.AddReceivingMiddleware(wrapSchemaErrorsInEnvelope)
	cwd, err := os.Getwd()
	defaultContext := defaultContextFor(cwd, err, log)

	mcp.AddTool(s, &mcp.Tool{Name: "register", Description: "Agentbus: register your identity for this session. Idempotent: calling it again from the same session returns the same name. Subscribes you to the repository's persistent chat channels and task lists (.local/agentbus.json; default general and tasks) and reports them in subscribed. Memory channels are not subscribed: they are returned in memory_channels for you to search. Returns the display name to pass as `as` on every other Agentbus call, plus pending message counts if the name was resumed and the other live identities in others. Also creates your direct-message inbox dm/<as>, which receive reads like any subscribed channel. Also applies the file's tag_subscriptions (sets of tags to follow across channels) and reports them in tag_subscriptions."},
		func(ctx context.Context, req *mcp.CallToolRequest, in registerIn) (*mcp.CallToolResult, any, error) {
			c := in.Context
			if c == "" {
				c = defaultContext
			}
			resume := in.Resume == nil || *in.Resume
			reg, err := b.Register(in.Name, in.Parent, c, resume)
			if err != nil {
				return nil, nil, err
			}
			applyPersistent(b, cwd, &reg)
			return result(reg, nil)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "create_channel", Description: "Agentbus: create a named channel of kind ordinary or memory. Idempotent when the kind matches. A name prefixed general/, memory/, or tasks/ implies its kind (a task list is tasks/<name>); a project's channels are general/<repo>, memory/<repo>, tasks/<repo>."},
		func(ctx context.Context, req *mcp.CallToolRequest, in channelIn) (*mcp.CallToolResult, any, error) {
			return result(b.CreateChannel(in.As, in.Name, in.Kind))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_channels", Description: "Agentbus: list all channels with kind, message count, and latest sequence. Direct-message inboxes are not listed."},
		func(ctx context.Context, req *mcp.CallToolRequest, in asIn) (*mcp.CallToolResult, any, error) {
			return result(b.ListChannels(in.As))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "subscribe", Description: "Agentbus: subscribe to a channel so receive returns its messages. from=now (default) starts at the current position; from=oldest starts at the oldest retained message. persistent=true also records the channel in this repository's .local/agentbus.json so register subscribes it in later sessions. Direct-message channels (dm/...) are not accepted. Or pass tags instead of channel: an AND set of 1-10 tags; matching messages from any chat channel you are not already subscribed to arrive through receive with matched_tags, starting from now."},
		func(ctx context.Context, req *mcp.CallToolRequest, in subscribeIn) (*mcp.CallToolResult, any, error) {
			if (in.Channel == "") == (len(in.Tags) == 0) {
				return nil, nil, &bus.Error{Code: "validation", Message: "pass channel or tags, not both"}
			}
			if len(in.Tags) > 0 {
				if err := b.SubscribeTags(in.As, in.Tags); err != nil {
					return nil, nil, err
				}
				norm, _ := bus.NormalizeTags(in.Tags)
				out := map[string]any{"subscribed_tags": norm}
				if in.Persistent {
					f, err := persistFile(cwd)
					if err != nil {
						return nil, nil, persistErr(err)
					}
					if _, err := f.AddTagSet(in.Tags); err != nil {
						return nil, nil, persistErr(err)
					}
					out["persistent"] = true
				}
				return result(out, nil)
			}
			if err := b.Subscribe(in.As, in.Channel, in.From); err != nil {
				return nil, nil, err
			}
			out := map[string]any{"subscribed": in.Channel}
			if in.Persistent {
				f, err := persistFile(cwd)
				if err != nil {
					return nil, nil, persistErr(err)
				}
				if _, err := f.AddChannel(in.Channel); err != nil {
					return nil, nil, persistErr(err)
				}
				out["persistent"] = true
			}
			return result(out, nil)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "unsubscribe", Description: "Agentbus: unsubscribe from a channel and drop its cursor. persistent=true also removes the channel from this repository's .local/agentbus.json. Direct-message channels (dm/...) are not accepted. Or pass tags to drop that tag set."},
		func(ctx context.Context, req *mcp.CallToolRequest, in unsubscribeIn) (*mcp.CallToolResult, any, error) {
			if (in.Channel == "") == (len(in.Tags) == 0) {
				return nil, nil, &bus.Error{Code: "validation", Message: "pass channel or tags, not both"}
			}
			if len(in.Tags) > 0 {
				if err := b.UnsubscribeTags(in.As, in.Tags); err != nil {
					return nil, nil, err
				}
				norm, _ := bus.NormalizeTags(in.Tags)
				out := map[string]any{"unsubscribed_tags": norm}
				if in.Persistent {
					f, err := persistFile(cwd)
					if err != nil {
						return nil, nil, persistErr(err)
					}
					if _, err := f.RemoveTagSet(in.Tags); err != nil {
						return nil, nil, persistErr(err)
					}
					out["persistent"] = true
				}
				return result(out, nil)
			}
			if err := b.Unsubscribe(in.As, in.Channel); err != nil {
				return nil, nil, err
			}
			out := map[string]any{"unsubscribed": in.Channel}
			if in.Persistent {
				f, err := persistFile(cwd)
				if err != nil {
					return nil, nil, persistErr(err)
				}
				if _, err := f.RemoveChannel(in.Channel); err != nil {
					return nil, nil, persistErr(err)
				}
				out["persistent"] = true
			}
			return result(out, nil)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "send", Description: "Agentbus: send a message to a channel. On a memory channel this creates a memory and returns its memory_id. Use idempotency_key to make retries safe. To message one agent directly, set channel to dm/<name>, with a name from register's others or discover; answer a direct message by sending to dm/<its sender>, optionally with reply_to. Task lists (tasks/...) do not accept send; use the task tools. tags (up to 10, letters, digits, _ and -, stored lowercase) label the message; other agents can subscribe to tags and filter history and search by them. subject is an optional one-line summary (at most 200 characters) shown as the message's title and matched by search; without one, readers see the first line of content."},
		func(ctx context.Context, req *mcp.CallToolRequest, in sendIn) (*mcp.CallToolResult, any, error) {
			return result(b.Send(in.As, in.SendInput))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "receive", Description: "Agentbus: receive new messages on your subscribed channels. Pass ack with the batch token from your previous receive to acknowledge it; an unacknowledged batch is redelivered. wait_seconds (up to receive_max_wait_seconds) waits for messages when none are available; a larger value is rejected, and three consecutive empty waited receives return a polling error. To wait longer, run `agentbus wait` in a background shell instead of calling receive again. Messages delivered through a tag subscription carry matched_tags. tags/ in expired means your tag subscriptions lapsed from inactivity; re-subscribe with tags or re-register."},
		func(ctx context.Context, req *mcp.CallToolRequest, in receiveIn) (*mcp.CallToolResult, any, error) {
			return result(b.Receive(in.As, in.ReceiveInput))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "history", Description: "Agentbus: read a channel's retained messages by sequence range without touching your cursor. tags filters to messages carrying any of the given tags."},
		func(ctx context.Context, req *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, any, error) {
			return result(b.History(in.As, in.Channel, in.Before, in.After, in.Count, in.Tags...))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "search", Description: "Agentbus: search messages and memories. mode=text matches words; mode=semantic ranks memories by meaning; mode=both (default when embeddings are configured) fuses them. Filters: channel, sender, since, until (unix ms), thread (a seq), tags (any of). If no embedding endpoint is configured or it fails, semantic and both silently fall back to text results and the result carries semantic_unavailable=true."},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
			return result(b.Search(in.As, in.SearchInput))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_memory", Description: "Agentbus: get the current revision of a memory by memory_id."},
		func(ctx context.Context, req *mcp.CallToolRequest, in memoryIn) (*mcp.CallToolResult, any, error) {
			return result(b.GetMemory(in.As, in.ID))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "edit_memory", Description: "Agentbus: replace a memory's content as a new revision. Last committed write wins; the result names the revision you replaced. tags replaces the memory's tags; omit it to keep them. subject replaces the memory's subject; omit it to keep it, pass an empty string to clear it."},
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
	mcp.AddTool(s, &mcp.Tool{Name: "task_create", Description: "Agentbus: add a task to a task list (a memory channel named tasks/<name>; create one with create_channel kind=memory). New tasks are pending and unowned. parent nests it under an existing task in the same list; before or after (a sibling task id) places it, default last. blocked_by lists task ids in the same list that must complete first. One deliverable per task with an imperative subject; blocked_by only for real ordering dependencies, sibling order for priority."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskCreateIn) (*mcp.CallToolResult, any, error) {
			return result(b.TaskCreate(in.As, in.TaskCreateInput))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "task_claim", Description: "Agentbus: claim a task: if it has no owner you become its owner and it becomes in_progress. Fails with conflict if someone else owns it or it is blocked. A task whose owner's session has ended, or whose lease ran out, counts as unowned. leased_until (unix ms UTC, optional) sets a lease; renew it by calling task_claim again with a later leased_until before it passes. Call task_list first; claim before starting work."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskClaimIn) (*mcp.CallToolResult, any, error) {
			return result(b.TaskClaim(in.As, in.TaskID, in.LeasedUntil, in.IdempotencyKey))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "task_release", Description: "Agentbus: release a task: clears the owner and lease and returns it to pending. Do this when you stop working on a task you have not completed."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskReleaseIn) (*mcp.CallToolResult, any, error) {
			return result(b.TaskRelease(in.As, in.TaskID, in.IdempotencyKey))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "task_update", Description: "Agentbus: change a task by id; only the fields you pass change. status is pending, in_progress, or completed. While a task is in_progress only its owner may change or delete it; force=true overrides that for anyone and is recorded. owner=\"\" clears the owner, parent=0 moves the task to the top level, leased_until=0 clears the lease (any other value must be in the future). before/after reorder among siblings. delete=true deletes a task that has no subtasks. To renew a lease use task_claim again: renewing through task_update after the lease has expired finds the task already returned to pending and changes nothing. An in_progress task always has an owner."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskUpdateIn) (*mcp.CallToolResult, any, error) {
			return result(b.TaskUpdate(in.As, in.TaskPatch))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "task_get", Description: "Agentbus: get one task in full, with blocked and open_blockers derived from its blocked_by tasks."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskGetIn) (*mcp.CallToolResult, any, error) {
			return result(b.TaskGet(in.As, in.TaskID))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "task_list", Description: "Agentbus: list a task list's tasks in order (each task followed by its subtasks; depth gives the nesting), without descriptions. Optional status and owner filters. Subscribe to the tasks/<name> channel to be told about changes through receive: you get each task's latest revision, not every intermediate one, and deletions are not delivered. Check it before starting work to see what is claimed or blocked."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskListIn) (*mcp.CallToolResult, any, error) {
			return result(b.TaskList(in.As, in.TaskListInput))
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
		log.Error("bus open failed", "err", err)
		return err
	}
	defer func() {
		if cerr := b.Close(); cerr != nil {
			log.Warn("bus close failed", "err", cerr)
		}
	}()
	// SIGTERM/SIGINT/SIGHUP cancel ctx so the server returns and the session
	// cleanup below runs; a harness that only closes stdin gets there too.
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	// wg.Wait must run after cancel() but before b.Close(): otherwise a
	// Heartbeat or Tick in flight when the client disconnects can execute
	// against a closed bus. Defers run LIFO, so registering wg.Wait before
	// cancel's defer makes cancel fire first, then wg.Wait, then the
	// b.Close deferred above.
	wg := StartBackgroundLoops(ctx, b, cfg, log, 10*time.Second)
	defer wg.Wait()
	defer cancel()
	log.Info("agentbus mcp started", "version", Version, "config", cfg.Path, "data", cfg.DataDirectory)
	err = newServer(b, cfg, log).Run(ctx, &mcp.StdioTransport{})
	if eerr := b.EndSessions(); eerr != nil {
		log.Warn("end sessions failed", "err", eerr)
	}
	if err != nil && ctx.Err() == nil {
		log.Error("server run failed", "err", err)
	}
	return err
}

// StartBackgroundLoops starts the heartbeat and maintenance-tick goroutines
// and returns a WaitGroup that completes once both have exited. Both loops
// exit promptly when ctx is canceled; the caller must wg.Wait() after
// canceling ctx and before closing b, or an in-flight Heartbeat/Tick call
// can run against a closed bus.
func StartBackgroundLoops(ctx context.Context, b *bus.Bus, cfg config.Config, log *slog.Logger, heartbeatEvery time.Duration) *sync.WaitGroup {
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
