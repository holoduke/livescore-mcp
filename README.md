# LiveScore MCP

Free [Model Context Protocol](https://modelcontextprotocol.io/) server for real-time football scores, fixtures, team stats, and player data from 1000+ leagues worldwide.

**[livescoremcp.com](https://livescoremcp.com)**

## Quick Start

Connect any MCP client to the Streamable HTTP endpoint:

```
https://livescoremcp.com/mcp
```

Clients that only support the older SSE transport use `https://livescoremcp.com/sse`. No API key or account needed.

### Claude Code

```bash
claude mcp add --transport http livescore https://livescoremcp.com/mcp
```

### Claude Desktop / claude.ai

Settings → Connectors → Add custom connector → `https://livescoremcp.com/mcp`

### Cursor, Windsurf, VS Code, Cline and others

```json
{
  "mcpServers": {
    "livescore": {
      "url": "https://livescoremcp.com/mcp"
    }
  }
}
```

## Available Tools

| Tool | Description |
|------|-------------|
| `get_live_scores` | Matches in play right now, with score and minute; filter by country or league |
| `get_day_fixtures` | All matches on a date (results, live, kick-off times); filter by country or league |
| `get_league_fixtures` | A competition's latest results, live matches and upcoming fixtures, or one round |
| `get_standings` | League table: points, goal difference, form, qualification and relegation zones |
| `get_top_scorers` | Top scorers of a competition |
| `get_head_to_head` | All-time record of two teams, recent meetings and their next meeting |
| `get_match` | Match details: events, line-ups, substitutions, statistics, injuries, form, report and head-to-head |
| `get_team` | Team profile: results, fixtures, squad, injuries, transfers, coach, stadium, honours |
| `get_player` | Player profile: statistics per season and competition, market value, injuries, transfers |
| `search` | Find teams, players and competitions and get the IDs and league keys the other tools use |
| `get_team_image` | Team logo URL |
| `get_fixtures` | Alias of `get_league_fixtures` (kept for 1.0 clients) |
| `health` | Connectivity check |

Answers are compact text written for language models: IDs for follow-up calls and a [football-mania.com](https://football-mania.com) link for every match, team, player and competition. Every tool is read-only.

- **Names or IDs**: teams, players and competitions can be passed by name (`"Arsenal"`, `"La Liga"`); the best search match is used.
- **Time zones**: pass `timezone` (IANA, e.g. `Europe/London`) and kick-off times and "today" follow it; the default is UTC.
- **Languages**: `language` (en, es, de, fr, pt, pt-BR, ar, …) sets team and competition names and the language of the links.
- **Raw data**: `format: "json"` returns the underlying feed.

## Prompts

Clients that support MCP prompts offer these as ready-made commands:

| Prompt | Arguments |
|--------|-----------|
| `match_preview` | `team_a`, `team_b` |
| `league_roundup` | `league` |
| `team_report` | `team` |
| `live_now` | `country` (optional) |

## Example Queries

Once connected, just ask your AI assistant:

- "What are the live scores right now?"
- "Show me the Premier League table"
- "Who are the top scorers in La Liga?"
- "How did Barcelona do this week, and who do they play next?"
- "Who scored in the Champions League tonight?"
- "What matches are on this Saturday in Brazil?"
- "Tell me about Ajax's squad and injuries"
- "What's the head-to-head between Real Madrid and Barcelona?"

## Data Source

All football data is provided by **[football-mania.com](https://football-mania.com)** - a comprehensive football data platform covering 1000+ leagues and competitions worldwide with real-time scores, fixtures, team statistics, player profiles, and match details.

Football Mania is also available as a mobile app:
- [Google Play](https://play.google.com/store/apps/details?id=holoduke.soccer_gen)
- [App Store](https://apps.apple.com/us/app/football-mania-soccer-scores/id896357542)

## Usage Policy

LiveScore MCP is **free for personal and non-commercial use**.

- Rate limits are enforced (30 tool calls/min per IP)
- Bulk scraping and automated data harvesting are not allowed
- Commercial use requires permission - open an issue to discuss

## Tech Stack

- **Go** with [mcp-go](https://github.com/mark3labs/mcp-go)
- **Streamable HTTP** (`/mcp`, stateless) and **SSE** (`/sse`) transports
- Deployed on [Coolify](https://coolify.io)

## Self-Hosting

The football data comes from football-mania.com's own API, which serves the hosted instance at livescoremcp.com; a self-hosted copy is meant for development and testing.

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
