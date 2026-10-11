package main

// Usage counters (per tool, since start) for /health, and one log line per
// tool call. No arguments or client addresses are kept.

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

var (
	started    = time.Now()
	statsMu    sync.Mutex
	toolCalls  = map[string]int{}
	toolErrors = map[string]int{}
)

func statsMiddleware(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t := time.Now()
		res, err := next(ctx, req)
		failed := err != nil || (res != nil && res.IsError)
		statsMu.Lock()
		toolCalls[req.Params.Name]++
		if failed {
			toolErrors[req.Params.Name]++
		}
		statsMu.Unlock()
		log.Printf("tool=%s ms=%d error=%v", req.Params.Name, time.Since(t).Milliseconds(), failed)
		return res, err
	}
}

func statsSnapshot() map[string]any {
	statsMu.Lock()
	defer statsMu.Unlock()
	calls, errs, total := map[string]int{}, map[string]int{}, 0
	for k, v := range toolCalls {
		calls[k] = v
		total += v
	}
	for k, v := range toolErrors {
		errs[k] = v
	}
	cacheMu.Lock()
	cached := len(cache)
	cacheMu.Unlock()
	return map[string]any{
		"status": "ok", "server": serverName, "version": serverVersion,
		"uptime_seconds": int(time.Since(started).Seconds()),
		"tool_calls":     total, "calls_by_tool": calls, "errors_by_tool": errs, "cached_responses": cached,
	}
}
