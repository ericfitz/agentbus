package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ericfitz/agentbus/internal/cli"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/mcpserver"
	"github.com/ericfitz/agentbus/internal/tui"
)

// splitTags splits a comma-separated tag list, trimming space around each
// tag so "-tags a, b" (the natural style) does not fail NormalizeTags (or,
// on the CLI path AddTagSet/RemoveTagSet -> NormalizeTagPatterns), which
// reject a tag carrying its own leading/trailing space (ADR 0009).
func splitTags(v string) []string {
	parts := strings.Split(v, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// usage is the top-level usage line, printed to stdout for help and when no
// command is given.
const usage = "usage: agentbus <init|mcp|tui|status|reset|delete-channel|identity|subscribe|unsubscribe|wait|watch (deprecated)|park|unpark|stop-hook|subagent-hook|version> [flags]"

func main() {
	// Bare agentbus is help: winget validation runs the executable with no
	// arguments and needs exit 0.
	cmd := "help"
	if len(os.Args) >= 2 {
		cmd = os.Args[1]
	}
	code := run(cmd, os.Args[min(2, len(os.Args)):])
	os.Exit(code)
}

func loadConfig(cmd string, args []string) (config.Config, error) {
	fs := flag.NewFlagSet("agentbus "+cmd, flag.ContinueOnError)
	path := fs.String("config", "", "configuration file")
	if err := parseFlags(fs, args); err != nil {
		return config.Config{}, err
	}
	cfg, _, err := config.Load(*path)
	return cfg, err
}

// errUnexpectedArg is parseFlags' error for a positional argument given to
// a command that takes flags only.
var errUnexpectedArg = errors.New("unexpected argument")

// parseFlags parses args for a command that takes flags only. A leftover
// positional argument is reported like a bad flag (the error, then the
// flags) and returns errUnexpectedArg, so it is never silently ignored.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(fs.Output(), "%s: unexpected argument %q; it takes flags only\n", fs.Name(), fs.Arg(0))
		fs.Usage()
		return errUnexpectedArg
	}
	return nil
}

// noArgs rejects arguments to a command that takes none (help, version).
func noArgs(cmd string, args []string) int {
	if len(args) == 0 {
		return 0
	}
	fmt.Fprintf(os.Stderr, "agentbus %s: unexpected argument %q; it takes none\n", cmd, args[0])
	return 2
}

// parseExit maps a flag parse error to an exit code: -h/--help is success.
func parseExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

// run dispatches a subcommand.
func run(cmd string, args []string) int {
	switch cmd {
	case "mcp":
		cfg, err := loadConfig(cmd, args)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if err != nil {
			// The only stderr line the mcp command ever writes: a fatal
			// config error before serving.
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		if err := mcpserver.Run(context.Background(), cfg); err != nil {
			return 1
		}
		return 0
	case "init":
		fs := flag.NewFlagSet("agentbus init", flag.ContinueOnError)
		var o cli.InitOptions
		fs.BoolVar(&o.Global, "global", false, "configure the harnesses on this machine even when inside a git repository")
		fs.StringVar(&o.Harness, "harness", "", "configure only this harness (claude, codex, or grok), even if it is not detected")
		fs.BoolVar(&o.DryRun, "dry-run", false, "print what would change without writing anything")
		path := fs.String("config", "", "configuration file")
		if err := parseFlags(fs, args); err != nil {
			return parseExit(err)
		}
		cfg, _, err := config.Load(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		o.Config = cfg
		if err := cli.Init(o, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		return 0
	case "tui":
		fs := flag.NewFlagSet("agentbus tui", flag.ContinueOnError)
		path := fs.String("config", "", "configuration file")
		as := fs.String("as", "", "identity to register as (default: tui_name from the config)")
		if err := parseFlags(fs, args); err != nil {
			return parseExit(err)
		}
		cfg, _, err := config.Load(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		if err := tui.Run(cfg, *as, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		return 0
	case "wait":
		fs := flag.NewFlagSet("agentbus wait", flag.ContinueOnError)
		var o cli.WaitOptions
		path := fs.String("config", "", "configuration file")
		fs.StringVar(&o.As, "as", "", "identity to wait for (default: the session registered from the same harness, else what `agentbus identity` reports)")
		fs.Func("channel", "only this channel (repeatable; default: all subscribed)", func(s string) error { o.Channels = append(o.Channels, s); return nil })
		fs.BoolVar(&o.IncludeOwn, "include-own", false, "also wake for the identity's own messages")
		fs.StringVar(&o.Filter, "filter", "", "regexp on content; only matching messages wake (e.g. 'deploy'), except direct messages, whose content is withheld from the printed JSON (call receive to read it)")
		fs.BoolVar(&o.NoHarnessWatch, "no-harness-watch", false, "keep waiting after the harness that started this wait exits (default: exit 4, silently)")
		fs.DurationVar(&o.Timeout, "timeout", 0, "give up after this long, exit 1 (default: wait forever); exit 3 means a newer wait for this identity (or from the same harness) replaced this one, exit 4 that the harness that started it is gone")
		if err := parseFlags(fs, args); err != nil {
			return parseExit(err)
		}
		cfg, _, err := config.Load(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 2
		}
		o.Config = cfg
		o.IdentitySource = cli.SourceAs
		if o.As == "" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintln(os.Stderr, "agentbus:", err)
				return 2
			}
			if o.As, o.IdentitySource = cli.WaitIdentitySource(cfg, cwd, os.Stderr); o.As == "" {
				fmt.Fprintln(os.Stderr, "agentbus: this session has not registered, and another session holds this repository's name; register first or pass -as")
				return 2
			}
		}
		// Installed before Wait acquires the wait locks, so the
		// SIGTERM a replacing wait sends is always handled, never fatal.
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		defer stop()
		switch err := cli.Wait(ctx, o, os.Stdout); {
		case errors.Is(err, cli.ErrWaitTimeout):
			return 1
		case errors.Is(err, cli.ErrWaitReplaced):
			return 3 // replaced by a newer wait for this identity: silent, no wake-up
		case errors.Is(err, cli.ErrHarnessGone):
			return 4 // the harness that started this wait exited: silent
		case err != nil:
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 2
		}
		return 0
	case "watch":
		fs := flag.NewFlagSet("agentbus watch", flag.ContinueOnError)
		fs.Usage = func() {
			_, _ = fmt.Fprintln(fs.Output(), "Usage of agentbus watch (deprecated, ADR 0025: use wait):")
			fs.PrintDefaults()
		}
		var o cli.WatchOptions
		path := fs.String("config", "", "configuration file")
		fs.StringVar(&o.As, "as", "", "identity to watch for (default: the session registered from the same harness, else what `agentbus identity` reports)")
		fs.Func("channel", "only this channel (repeatable; default: all subscribed)", func(s string) error { o.Channels = append(o.Channels, s); return nil })
		fs.BoolVar(&o.IncludeOwn, "include-own", false, "also wake for the identity's own messages")
		fs.StringVar(&o.Filter, "filter", "", "regexp on content; only matching messages wake (e.g. 'deploy'), except direct messages")
		fs.BoolVar(&o.NoHarnessWatch, "no-harness-watch", false, "keep watching after the harness that started this watch exits (default: exit 4, silently)")
		if err := parseFlags(fs, args); err != nil {
			return parseExit(err)
		}
		cfg, _, err := config.Load(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 2
		}
		o.Config = cfg
		o.IdentitySource = cli.SourceAs
		if o.As == "" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintln(os.Stderr, "agentbus:", err)
				return 2
			}
			if o.As, o.IdentitySource = cli.WaitIdentitySource(cfg, cwd, os.Stderr); o.As == "" {
				fmt.Fprintln(os.Stderr, "agentbus: this session has not registered, and another session holds this repository's name; register first or pass -as")
				return 2
			}
		}
		// Installed before Watch acquires the wait locks, so the
		// SIGTERM a replacing wait or watch sends is always handled, never fatal.
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		defer stop()
		switch err := cli.Watch(ctx, o, os.Stdout); {
		case errors.Is(err, cli.ErrWaitReplaced):
			return 3 // replaced by a newer wait or watch for this identity: silent, no wake-up
		case errors.Is(err, cli.ErrHarnessGone):
			return 4 // the harness that started this watch exited: silent
		case err != nil:
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 2
		}
		return 2 // unreachable: Watch runs until it fails
	case "park", "unpark":
		// park: only direct messages wake this session until it is unparked
		// (ADR 0025 decision 3). unpark is the UserPromptSubmit hook: it never
		// prints and always exits 0 so a prompt is never blocked.
		fs := flag.NewFlagSet("agentbus "+cmd, flag.ContinueOnError)
		path := fs.String("config", "", "configuration file")
		as := fs.String("as", "", "identity to "+cmd+" (default: the session registered from the same harness, else what `agentbus identity` reports)")
		hook := cmd == "unpark"
		fail := 2
		if hook {
			fail = 0
		}
		var perr error
		if hook {
			// -h still prints usage; a stray argument or bad flag prints nothing.
			fs.SetOutput(io.Discard)
			if perr = fs.Parse(args); perr == nil && fs.NArg() > 0 {
				perr = errUnexpectedArg
			} else if errors.Is(perr, flag.ErrHelp) {
				fs.SetOutput(os.Stderr)
				fs.Usage()
			}
		} else {
			perr = parseFlags(fs, args)
		}
		if err := perr; err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return fail
		}
		cfg, _, err := config.Load(*path)
		if err != nil {
			if !hook {
				fmt.Fprintln(os.Stderr, "agentbus:", err)
			}
			return fail
		}
		cwd, err := os.Getwd()
		if err != nil {
			if !hook {
				fmt.Fprintln(os.Stderr, "agentbus:", err)
			}
			return fail
		}
		if hook {
			// As the UserPromptSubmit hook, resolve from the hook's cwd like
			// the stop hook does (ADR 0025 "Unpark hook").
			if c := cli.StdinHookCwd(os.Stdin); c != "" {
				cwd = c
			}
			cli.Unpark(cfg, *as, cwd, io.Discard)
			return 0
		}
		if err := cli.Park(cfg, *as, cwd, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 2
		}
		return 0
	case "stop-hook":
		// Installed as a Stop hook by init --global. Always exits 0 so a
		// broken bus never keeps a harness from stopping.
		cfg, err := loadConfig(cmd, args)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 0
		}
		cli.StopHook(cfg, os.Stdin, os.Stdout, os.Stderr)
		return 0
	case "subagent-hook":
		// Installed as a Claude Code SubagentStart hook by init --global
		// (ADR 0016). Always exits 0 and prints nothing on any failure.
		cfg, err := loadConfig(cmd, args)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 0
		}
		cli.SubagentHook(cfg, os.Stdin, os.Stdout, os.Stderr)
		return 0
	case "help", "-h", "--help":
		if code := noArgs(cmd, args); code != 0 {
			return code
		}
		fmt.Println(usage)
		return 0
	case "version", "-v", "--version":
		if code := noArgs(cmd, args); code != 0 {
			return code
		}
		fmt.Println(mcpserver.Version)
		return 0
	case "identity":
		if err := parseFlags(flag.NewFlagSet("agentbus identity", flag.ContinueOnError), args); err != nil {
			return parseExit(err)
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		if err := cli.Identity(cwd, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		return 0
	case "status":
		cfg, err := loadConfig(cmd, args)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		if err := cli.Status(cfg, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		return 0
	case "reset":
		cfg, err := loadConfig(cmd, args)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		if err := cli.Reset(cfg, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		return 0
	case "delete-channel":
		fs := flag.NewFlagSet("agentbus delete-channel", flag.ContinueOnError)
		path := fs.String("config", "", "configuration file")
		yes := fs.Bool("y", false, "skip the confirmation prompt")
		if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
			return 0
		} else if err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: agentbus delete-channel [-y] <channel>")
			return 2
		}
		cfg, _, err := config.Load(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		cwd, _ := os.Getwd()
		if err := cli.DeleteChannel(cfg, cwd, fs.Arg(0), *yes, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		return 0
	case "subscribe", "unsubscribe":
		fs := flag.NewFlagSet("agentbus "+cmd, flag.ContinueOnError)
		tags := fs.String("tags", "", "comma-separated tag set to follow instead of a channel; a tag ending in * matches every tag with that prefix (env:*)")
		if err := fs.Parse(args); err != nil {
			return parseExit(err)
		}
		if (fs.NArg() != 1) == (*tags == "") {
			fmt.Fprintf(os.Stderr, "usage: agentbus %s <channel> | agentbus %s -tags <tag|prefix*>[,<tag|prefix*>...]\n", cmd, cmd)
			return 2
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		switch {
		case *tags != "" && cmd == "subscribe":
			err = cli.SubscribeTags(cwd, splitTags(*tags), os.Stdout)
		case *tags != "":
			err = cli.UnsubscribeTags(cwd, splitTags(*tags), os.Stdout)
		case cmd == "subscribe":
			err = cli.Subscribe(cwd, fs.Arg(0), os.Stdout)
		default:
			err = cli.Unsubscribe(cwd, fs.Arg(0), os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		return 2
	}
}
