package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agentbus <mcp|status|reset|identity> [--config path]")
		os.Exit(2)
	}
	code := run(os.Args[1], os.Args[2:])
	os.Exit(code)
}

// run dispatches a subcommand. Later tasks add cases.
func run(cmd string, args []string) int {
	switch cmd {
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		return 2
	}
}
