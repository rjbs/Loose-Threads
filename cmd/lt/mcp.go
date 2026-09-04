package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rjbs/loosethreads/internal/mcpserver"
	"github.com/rjbs/loosethreads/internal/project"
	"github.com/rjbs/loosethreads/internal/store"
)

func init() {
	register(&command{"mcp", "serve the store to Claude Code over MCP on stdio", runMCP})
}

func runMCP(args []string) error {
	fs := newFlagSet("mcp", "")
	projectID := fs.String("project", "", "default project for tool calls (default: derived from the working directory)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		fs.Usage()
		return fmt.Errorf("mcp takes no arguments")
	}

	s, err := store.Open("")
	if err != nil {
		return err
	}
	def := *projectID
	if def == "" {
		if id, err := project.Identify("."); err == nil {
			def = id.ID
		}
	}
	err = mcpserver.New(s, def).Run(context.Background(), &mcp.StdioTransport{})
	// When the client hangs up, the SDK reports its internal "server is
	// closing" error with the EOF formatted in as text rather than wrapped,
	// and the sentinel lives in an internal package, so matching the
	// message is the only handle we have.  A hangup is how every stdio
	// session ends, so it is not an error worth reporting.
	// -- claude, 2026-09-04
	if err != nil && strings.Contains(err.Error(), "server is closing") {
		return nil
	}
	return err
}
