package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerResources(s *server.MCPServer) {
	s.AddResource(
		mcp.NewResource("server://info", "LiveScore MCP server info", mcp.WithMIMEType("text/plain")),
		func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{mcp.TextResourceContents{
				URI:      "server://info",
				MIMEType: "text/plain",
				Text:     "LiveScore MCP " + serverVersion + "\n\n" + serverInstructions + "\n\nData: " + siteURL,
			}}, nil
		},
	)
}
