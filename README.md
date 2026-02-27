# LiveScore MCP

Free [Model Context Protocol](https://modelcontextprotocol.io/) server for real-time football scores, fixtures, team stats, and player data from 1000+ leagues worldwide.

**[livescoremcp.com](https://livescoremcp.com)**

## Quick Start

Connect any MCP client to the SSE endpoint:

```
https://livescoremcp.com/sse
```

### Claude Desktop

Add to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "livescore": {
      "url": "https://livescoremcp.com/sse"
    }
  }
}
```

### Claude Code

```bash
claude mcp add livescore https://livescoremcp.com/sse
```

Works with any MCP-compatible client: **Claude Desktop**, **Claude Code**, **Cursor**, **Windsurf**, **Cline**, and more.

## Available Tools

| Tool | Description |
|------|-------------|
| `get_live_scores` | Currently live matches with real-time scores and minute-by-minute updates |
| `get_fixtures` | Competition fixtures (Champions League, Europa League, World Cup, etc.) |
| `get_league_fixtures` | League-specific fixtures (e.g. Eredivisie, Premier League) |
| `get_day_fixtures` | All fixtures for a specific date |
| `get_match` | Detailed match info with events, lineups, stats, and head-to-head data |
| `get_team` | Team details including squad and statistics |
| `get_player` | Player profiles with career stats |
| `get_team_image` | Team logo URL |
| `search` | Search teams, players, or competitions by name |
| `health` | Connectivity check |

## Example Queries

Once connected, just ask your AI assistant:

- "What are the live scores right now?"
- "Show me the Premier League table"
- "How is Barcelona doing this week?"
- "Who scored in the Champions League tonight?"
- "What matches are on this Saturday?"
- "Tell me about Ajax's squad"

## Data Source

All football data is provided by **[football-mania.com](https://football-mania.com)** - a comprehensive football data platform covering 1000+ leagues and competitions worldwide with real-time scores, fixtures, team statistics, player profiles, and match details.

Football Mania is also available as a mobile app:
- [Google Play](https://play.google.com/store/apps/details?id=holoduke.soccer_gen)
- [App Store](https://apps.apple.com/us/app/football-mania-soccer-scores/id896357542)

## Usage Policy

LiveScore MCP is **free for personal and non-commercial use**.

- Rate limits are enforced (30 requests/min per IP)
- Bulk scraping and automated data harvesting are not allowed
- Commercial use requires permission - open an issue to discuss

## Tech Stack

- **Go** with [mcp-go](https://github.com/mark3labs/mcp-go)
- **SSE transport** for real-time communication
- Deployed on [Coolify](https://coolify.io)

## Self-Hosting

```bash
git clone https://github.com/holoduke/livescore-mcp.git
cd livescore-mcp
go build -o livescore-mcp .
PORT=8080 ./livescore-mcp
```

Or with Docker:

```bash
docker build -t livescore-mcp .
docker run -p 8080:8080 livescore-mcp
```

## License

MIT
