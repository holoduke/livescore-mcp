package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	baseURL        = "https://uitslagen.live/footapi"
	defaultLang    = "en"
	defaultVersion = 2800
	serverName     = "livescore-mcp"
	serverVersion  = "1.0.0"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	publicURL := os.Getenv("PUBLIC_URL")
	if publicURL == "" {
		publicURL = fmt.Sprintf("http://localhost:%s", port)
	}

	s := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, false),
	)

	registerTools(s)
	registerResources(s)

	sseServer := server.NewSSEServer(s,
		server.WithBaseURL(publicURL),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "" {
			serveLandingPage(w, r)
			return
		}
		sseServer.ServeHTTP(w, r)
	})
	mux.HandleFunc("/sse", sseServer.ServeHTTP)
	mux.HandleFunc("/message", sseServer.ServeHTTP)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","server":"livescore-mcp","version":"1.0.0"}`))
	})

	log.Printf("LiveScore MCP Server %s starting on :%s", serverVersion, port)
	if err := (&http.Server{Addr: ":" + port, Handler: mux}).ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func serveLandingPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, landingHTML)
}

const landingHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>LiveScore MCP - Football Live Scores via MCP</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
    background: #0a0e17;
    color: #e0e6ed;
    min-height: 100vh;
  }
  .hero {
    text-align: center;
    padding: 80px 20px 60px;
    background: linear-gradient(135deg, #0a0e17 0%, #1a1f2e 50%, #0d1a0d 100%);
  }
  .hero h1 {
    font-size: 3rem;
    font-weight: 800;
    background: linear-gradient(135deg, #4ade80, #22d3ee);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
    margin-bottom: 16px;
  }
  .hero p {
    font-size: 1.25rem;
    color: #94a3b8;
    max-width: 600px;
    margin: 0 auto 32px;
  }
  .badge {
    display: inline-block;
    background: rgba(74, 222, 128, 0.1);
    border: 1px solid rgba(74, 222, 128, 0.3);
    color: #4ade80;
    padding: 6px 16px;
    border-radius: 20px;
    font-size: 0.85rem;
    font-weight: 600;
  }
  .container { max-width: 900px; margin: 0 auto; padding: 40px 20px; }
  .section { margin-bottom: 48px; }
  .section h2 {
    font-size: 1.5rem;
    margin-bottom: 20px;
    color: #f1f5f9;
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .section h2 span { font-size: 1.5rem; }
  .connect-box {
    background: #111827;
    border: 1px solid #1e293b;
    border-radius: 12px;
    padding: 24px;
    margin-bottom: 16px;
  }
  .connect-box h3 { color: #22d3ee; margin-bottom: 12px; font-size: 1rem; }
  pre {
    background: #0d1117;
    border: 1px solid #1e293b;
    border-radius: 8px;
    padding: 16px;
    overflow-x: auto;
    font-size: 0.85rem;
    line-height: 1.6;
    color: #c9d1d9;
  }
  .tools-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: 16px;
  }
  .tool-card {
    background: #111827;
    border: 1px solid #1e293b;
    border-radius: 10px;
    padding: 20px;
    transition: border-color 0.2s;
  }
  .tool-card:hover { border-color: #4ade80; }
  .tool-card h3 {
    font-family: monospace;
    color: #4ade80;
    font-size: 0.95rem;
    margin-bottom: 8px;
  }
  .tool-card p { color: #94a3b8; font-size: 0.85rem; line-height: 1.5; }
  .footer {
    text-align: center;
    padding: 40px 20px;
    color: #475569;
    font-size: 0.85rem;
    border-top: 1px solid #1e293b;
  }
  .footer a { color: #4ade80; text-decoration: none; }
  .endpoint-url {
    font-family: monospace;
    background: rgba(74, 222, 128, 0.1);
    color: #4ade80;
    padding: 2px 8px;
    border-radius: 4px;
    font-size: 0.9rem;
  }
</style>
</head>
<body>
<div class="hero">
  <h1>LiveScore MCP</h1>
  <p>Real-time football scores, fixtures, team &amp; player data via the Model Context Protocol</p>
  <span class="badge">SSE Transport &bull; 10 Tools &bull; Multi-language</span>
</div>

<div class="container">
  <div class="section">
    <h2><span>&#9889;</span> Connect</h2>
    <div class="connect-box">
      <h3>SSE Endpoint</h3>
      <p style="margin-bottom:12px">Connect any MCP client to: <span class="endpoint-url">https://livescoremcp.com/sse</span></p>
    </div>
    <div class="connect-box">
      <h3>Claude Desktop / claude_desktop_config.json</h3>
      <pre>{
  "mcpServers": {
    "livescore": {
      "url": "https://livescoremcp.com/sse"
    }
  }
}</pre>
    </div>
    <div class="connect-box">
      <h3>Health Check</h3>
      <pre>curl https://livescoremcp.com/health</pre>
    </div>
  </div>

  <div class="section">
    <h2><span>&#9917;</span> Available Tools</h2>
    <div class="tools-grid">
      <div class="tool-card">
        <h3>get_live_scores</h3>
        <p>Currently live matches with real-time scores and minute-by-minute updates</p>
      </div>
      <div class="tool-card">
        <h3>get_fixtures</h3>
        <p>Competition fixtures &mdash; Champions League, Europa League, World Cup, etc.</p>
      </div>
      <div class="tool-card">
        <h3>search</h3>
        <p>Search teams, players, or competitions by name with optional country filter</p>
      </div>
      <div class="tool-card">
        <h3>get_league_fixtures</h3>
        <p>League-specific fixtures &mdash; Eredivisie, Premier League, La Liga, etc.</p>
      </div>
      <div class="tool-card">
        <h3>get_team</h3>
        <p>Detailed team info: squad, stats, upcoming matches, and recent results</p>
      </div>
      <div class="tool-card">
        <h3>get_player</h3>
        <p>Player profiles with career stats, current team, and transfer history</p>
      </div>
      <div class="tool-card">
        <h3>get_match</h3>
        <p>Full match details: events, lineups, stats, and head-to-head records</p>
      </div>
      <div class="tool-card">
        <h3>get_day_fixtures</h3>
        <p>All matches for a specific date across every league and competition</p>
      </div>
      <div class="tool-card">
        <h3>get_team_image</h3>
        <p>Team logo PNG URL for embedding in responses and applications</p>
      </div>
      <div class="tool-card">
        <h3>health</h3>
        <p>Connectivity check &mdash; echo back a message to verify the server is alive</p>
      </div>
    </div>
  </div>

  <div class="section">
    <h2><span>&#127760;</span> Languages</h2>
    <p style="color:#94a3b8">All tools support a <code style="color:#22d3ee">language</code> parameter: <strong>en</strong>, nl, de, fr, es, pt, it, and more. Timestamps are GMT/UTC.</p>
  </div>
</div>

<div class="footer">
  <p>Built with <a href="https://github.com/mark3labs/mcp-go">mcp-go</a> &bull; <a href="https://github.com/holoduke/livescore-mcp">Source on GitHub</a></p>
</div>
</body>
</html>`

// --- Helpers ---

func toMap(args any) map[string]interface{} {
	if m, ok := args.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{}
}

func getStr(args any, key, fallback string) string {
	m := toMap(args)
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

func getInt(args any, key string, fallback int) int {
	m := toMap(args)
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return fallback
}

func buildURL(path string, args any, extra ...string) string {
	u, _ := url.Parse(baseURL)
	u.Path, _ = url.JoinPath(u.Path, path)

	q := url.Values{}
	q.Set("lang", getStr(args, "language", defaultLang))
	q.Set("version", strconv.Itoa(getInt(args, "version", defaultVersion)))

	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}

	u.RawQuery = q.Encode()
	return u.String()
}

func apiRequest(apiURL, title string) (*mcp.CallToolResult, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("request error: %v", err)), nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "LiveScore-MCP/1.0")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("read error: %v", err)), nil
	}

	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(fmt.Sprintf("API error (status %d): %s", resp.StatusCode, string(body))), nil
	}

	var data interface{}
	if err := json.Unmarshal(body, &data); err == nil {
		if pretty, err := json.MarshalIndent(data, "", "  "); err == nil {
			return mcp.NewToolResultText(fmt.Sprintf("%s:\n\n%s", title, string(pretty))), nil
		}
	}

	return mcp.NewToolResultText(fmt.Sprintf("%s:\n\n%s", title, string(body))), nil
}

// --- Tool Registration ---

func registerTools(s *server.MCPServer) {
	// Health check
	s.AddTool(
		mcp.NewTool("health",
			mcp.WithDescription("Health check - echo back a message"),
			mcp.WithString("message", mcp.Required(), mcp.Description("Message to echo")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			msg := getStr(req.Params.Arguments, "message", "ok")
			return mcp.NewToolResultText(fmt.Sprintf("Echo: %s", msg)), nil
		},
	)

	// Live scores
	s.AddTool(
		mcp.NewTool("get_live_scores",
			mcp.WithDescription("Get currently live football matches and scores. All timestamps are GMT/UTC."),
			mcp.WithString("language", mcp.Description("Language code (en, nl, de, etc.). Default: en")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return apiRequest(
				buildURL("fixtures/feed_livenow.json", req.Params.Arguments),
				"Live Scores",
			)
		},
	)

	// Competition fixtures
	s.AddTool(
		mcp.NewTool("get_fixtures",
			mcp.WithDescription("Get fixtures for a specific competition (e.g. EurocupsUEFAChampionsLeague_small). All timestamps are GMT/UTC."),
			mcp.WithString("competition", mcp.Required(), mcp.Description("Competition identifier")),
			mcp.WithString("language", mcp.Description("Language code (en, nl, de, etc.)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			comp := getStr(req.Params.Arguments, "competition", "")
			return apiRequest(
				buildURL(fmt.Sprintf("fixtures_v2/%s.json", comp), req.Params.Arguments),
				fmt.Sprintf("Fixtures for %s", comp),
			)
		},
	)

	// Search
	s.AddTool(
		mcp.NewTool("search",
			mcp.WithDescription("Search for teams, players, or competitions by name"),
			mcp.WithString("q", mcp.Required(), mcp.Description("Search term (team, player, or competition name)")),
			mcp.WithString("language", mcp.Description("Language code (en, nl, de, etc.)")),
			mcp.WithString("country", mcp.Description("Country filter (e.g. Netherlands, England)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query := getStr(req.Params.Arguments, "q", "")

			u, _ := url.Parse(baseURL)
			u.Path, _ = url.JoinPath(u.Path, "search_v3")
			q := url.Values{}
			q.Set("q", query)
			q.Set("lang", getStr(req.Params.Arguments, "language", defaultLang))
			q.Set("version", strconv.Itoa(getInt(req.Params.Arguments, "version", defaultVersion)))
			if country := getStr(req.Params.Arguments, "country", ""); country != "" {
				q.Set("country", country)
			}
			u.RawQuery = q.Encode()

			return apiRequest(u.String(), fmt.Sprintf("Search results for '%s'", query))
		},
	)

	// League fixtures
	s.AddTool(
		mcp.NewTool("get_league_fixtures",
			mcp.WithDescription("Get fixtures for a specific league (e.g. NetherlandsEredivisie). All timestamps are GMT/UTC."),
			mcp.WithString("league_key", mcp.Required(), mcp.Description("League key from search results")),
			mcp.WithString("language", mcp.Description("Language code (en, nl, de, etc.)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			key := getStr(req.Params.Arguments, "league_key", "")
			return apiRequest(
				buildURL(fmt.Sprintf("fixtures_v2/%s_small.json", key), req.Params.Arguments),
				fmt.Sprintf("League fixtures for %s", key),
			)
		},
	)

	// Team info
	s.AddTool(
		mcp.NewTool("get_team",
			mcp.WithDescription("Get detailed team information (squad, stats) by team ID"),
			mcp.WithString("id", mcp.Required(), mcp.Description("Team ID from search results (e.g. 13183 for Ajax)")),
			mcp.WithString("language", mcp.Description("Language code (en, nl, de, etc.)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := getStr(req.Params.Arguments, "id", "")
			return apiRequest(
				buildURL(fmt.Sprintf("team_gs/%s.json", id), req.Params.Arguments),
				fmt.Sprintf("Team info for ID %s", id),
			)
		},
	)

	// Player info
	s.AddTool(
		mcp.NewTool("get_player",
			mcp.WithDescription("Get detailed player information (stats, career) by player ID"),
			mcp.WithString("id", mcp.Required(), mcp.Description("Player ID (e.g. 474972)")),
			mcp.WithString("language", mcp.Description("Language code (en, nl, de, etc.)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := getStr(req.Params.Arguments, "id", "")
			return apiRequest(
				buildURL(fmt.Sprintf("players/%s.json", id), req.Params.Arguments),
				fmt.Sprintf("Player info for ID %s", id),
			)
		},
	)

	// Match info
	s.AddTool(
		mcp.NewTool("get_match",
			mcp.WithDescription("Get detailed match information (events, lineups, stats) with optional head-to-head data"),
			mcp.WithString("id", mcp.Required(), mcp.Description("Match ID from live scores or fixtures")),
			mcp.WithString("language", mcp.Description("Language code (en, nl, de, etc.)")),
			mcp.WithNumber("h2h", mcp.Description("Include head-to-head data: 1=yes, 0=no. Default: 1")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := getStr(req.Params.Arguments, "id", "")
			h2h := strconv.Itoa(getInt(req.Params.Arguments, "h2h", 1))
			return apiRequest(
				buildURL(fmt.Sprintf("matches/%s.json", id), req.Params.Arguments, "h2h", h2h),
				fmt.Sprintf("Match info for ID %s", id),
			)
		},
	)

	// Day fixtures
	s.AddTool(
		mcp.NewTool("get_day_fixtures",
			mcp.WithDescription("Get all fixtures for a specific date. All timestamps are GMT/UTC."),
			mcp.WithString("date", mcp.Required(), mcp.Description("Date in DD/MM/YYYY format (e.g. 30/08/2025)")),
			mcp.WithString("language", mcp.Description("Language code (en, nl, de, etc.)")),
			mcp.WithNumber("tzoffset", mcp.Description("Timezone offset in minutes (e.g. 120 for UTC+2). Default: 0")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			date := getStr(req.Params.Arguments, "date", "")
			tzOffset := strconv.Itoa(getInt(req.Params.Arguments, "tzoffset", 0))
			return apiRequest(
				buildURL("fixtures/feed_matches_aggregated.json", req.Params.Arguments, "date", date, "tzoffset", tzOffset),
				fmt.Sprintf("Fixtures for %s", date),
			)
		},
	)

	// Team image
	s.AddTool(
		mcp.NewTool("get_team_image",
			mcp.WithDescription("Get team logo PNG URL by team ID"),
			mcp.WithString("id", mcp.Required(), mcp.Description("Team ID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := getStr(req.Params.Arguments, "id", "")
			u, _ := url.Parse(baseURL)
			u.Path, _ = url.JoinPath(u.Path, "images", "teams_gs", id+".png")
			imageURL := u.String()

			httpReq, err := http.NewRequest("HEAD", imageURL, nil)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("error: %v", err)), nil
			}
			httpReq.Header.Set("User-Agent", "LiveScore-MCP/1.0")

			resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(httpReq)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("error checking image: %v", err)), nil
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return mcp.NewToolResultError(fmt.Sprintf("image not available (status %d) for team ID %s", resp.StatusCode, id)), nil
			}

			return mcp.NewToolResultText(fmt.Sprintf("Team logo URL for ID %s:\n%s", id, imageURL)), nil
		},
	)
}

// --- Resource Registration ---

func registerResources(s *server.MCPServer) {
	s.AddResource(
		mcp.NewResource(
			"server://info",
			"LiveScore MCP Server Info",
			mcp.WithMIMEType("text/plain"),
		),
		func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			info := `LiveScore MCP Server v1.0.0

A football livescore MCP providing real-time data about matches, teams, players, fixtures, standings, goals, events, lineups, and stats.

Available Tools:
- health: Echo test for connectivity check
- get_live_scores: Currently live matches with real-time scores
- get_fixtures: Competition fixtures (e.g. Champions League)
- search: Search teams, players, or competitions by name
- get_league_fixtures: League fixtures by league key (e.g. NetherlandsEredivisie)
- get_team: Detailed team info (squad, stats) by team ID
- get_player: Detailed player info (career, stats) by player ID
- get_match: Match details (events, lineups, stats, h2h) by match ID
- get_day_fixtures: All fixtures for a specific date
- get_team_image: Team logo PNG URL by team ID

All timestamps are in GMT/UTC - convert to local timezone as needed.
Supports multiple languages: en, nl, de, fr, es, pt, it, etc.

Example Queries:
- "Show me live football matches right now"
- "Get Champions League fixtures"
- "Search for Ajax"
- "Get Eredivisie fixtures"
- "Show matches for today"
- "Get detailed info about player 474972"`

			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "server://info",
					MIMEType: "text/plain",
					Text:     info,
				},
			}, nil
		},
	)
}
