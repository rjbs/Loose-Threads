package main

import (
	"context"
	"fmt"

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
	return mcpserver.New(s, def).Run(context.Background(), &mcp.StdioTransport{})
}
