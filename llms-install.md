# Installing LiveScore MCP

LiveScore MCP is a hosted remote server. There is nothing to clone, build or install, and no API key.

## Steps

1. Add a remote MCP server named `livescore` with this URL:

   ```
   https://livescoremcp.com/mcp
   ```

   Transport: Streamable HTTP. For clients that only support SSE, use `https://livescoremcp.com/sse` instead.

2. Cline (`cline_mcp_settings.json`):

   ```json
   {
     "mcpServers": {
       "livescore": {
         "type": "streamableHttp",
         "url": "https://livescoremcp.com/mcp",
         "disabled": false,
         "autoApprove": []
       }
     }
   }
   ```

   Other clients (Claude Desktop, Cursor, Windsurf, VS Code) use the same URL under `mcpServers.livescore.url`. Claude Code: `claude mcp add --transport http livescore https://livescoremcp.com/mcp`.

3. Verify: call the `health` tool, or `get_live_scores`. The tool list has 14 tools: get_live_scores, get_day_fixtures, get_league_fixtures, get_fixtures, get_standings, get_top_scorers, get_head_to_head, get_match, get_team, get_player, search, fetch, get_team_image and health.

## Notes

- All tools are read-only. Times are UTC unless you pass `timezone` (IANA name).
- Rate limit: 30 tool calls per minute per IP.
- No environment variables or credentials are needed.
