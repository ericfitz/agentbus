package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/ericfitz/agentbus/internal/cli"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/mcpserver"
	"github.com/ericfitz/agentbus/internal/tui"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agentbus <init|mcp|tui|status|reset|delete-channel|identity|subscribe|unsubscribe|wait|version> [flags]")
		os.Exit(2)
	}
	code := run(os.Args[1], os.Args[2:])
	os.Exit(code)
}

func loadConfig(args []string) (config.Config, error) {
	fs := flag.NewFlagSet("agentbus", flag.ContinueOnError)
	path := fs.String("config", "", "configuration file")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, err
	}
	cfg, _, err := config.Load(*path)
	return cfg, err
}

// run dispatches a subcommand.
func run(cmd string, args []string) int {
	switch cmd {
	case "mcp":
		cfg, err := loadConfig(args)
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
		fs.StringVar(&o.Harness, "harness", "", "configure only this harness (claude or codex), even if it is not detected")
		fs.BoolVar(&o.DryRun, "dry-run", false, "print what would change without writing anything")
		path := fs.String("config", "", "configuration file")
		if err := fs.Parse(args); err != nil {
			return 2
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
		if err := fs.Parse(args); err != nil {
			return 2
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
		fs.StringVar(&o.As, "as", "", "identity to wait for (default: what `agentbus identity` reports)")
		fs.Func("channel", "only this channel (repeatable; default: all subscribed)", func(s string) error { o.Channels = append(o.Channels, s); return nil })
		fs.BoolVar(&o.IncludeOwn, "include-own", false, "also wake for the identity's own messages")
		fs.StringVar(&o.Filter, "filter", "", "regexp on content; only matching messages wake (e.g. '@myname'), except direct messages")
		fs.DurationVar(&o.Timeout, "timeout", 0, "give up after this long, exit 1 (default: wait forever)")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		cfg, _, err := config.Load(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 2
		}
		o.Config = cfg
		if o.As == "" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintln(os.Stderr, "agentbus:", err)
				return 2
			}
			o.As = cli.IdentityName(cwd, os.Stderr)
		}
		switch err := cli.Wait(o, os.Stdout); {
		case errors.Is(err, cli.ErrWaitTimeout):
			return 1
		case err != nil:
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 2
		}
		return 0
	case "version":
		fmt.Println(mcpserver.Version)
		return 0
	case "identity":
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
		cfg, err := loadConfig(args)
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
		cfg, err := loadConfig(args)
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
		if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
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
		if len(args) != 1 {
			fmt.Fprintf(os.Stderr, "usage: agentbus %s <channel>\n", cmd)
			return 2
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		if cmd == "subscribe" {
			err = cli.Subscribe(cwd, args[0], os.Stdout)
		} else {
			err = cli.Unsubscribe(cwd, args[0], os.Stdout)
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
