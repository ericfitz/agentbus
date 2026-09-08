package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ericfitz/agentbus/internal/cli"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/mcpserver"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agentbus <init|mcp|status|reset|identity|version> [flags]")
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
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if err := cli.Init(o, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
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
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		return 2
	}
}
