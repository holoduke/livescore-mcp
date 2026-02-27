package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"golang.org/x/time/rate"
)

//go:embed static/*
var staticFiles embed.FS

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

	// 30 requests/min per IP, burst of 10
	rl := newRateLimiter(rate.Every(2*time.Second), 10)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "" {
			serveLandingPage(w, r)
			return
		}
		sseServer.ServeHTTP(w, r)
	})
	mux.HandleFunc("/sse", sseServer.ServeHTTP)
	mux.HandleFunc("/message", rl.middleware(sseServer.ServeHTTP))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","server":"livescore-mcp","version":"1.0.0"}`))
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, robotsTxt)
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, sitemapXML)
	})
	mux.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.FileServer(http.FS(staticFiles)).ServeHTTP(w, r)
	}))
	mux.HandleFunc("/privacy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fmt.Fprint(w, privacyHTML)
	})
	mux.HandleFunc("/terms", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fmt.Fprint(w, termsHTML)
	})

	handler := securityHeaders(mux)

	log.Printf("LiveScore MCP Server %s starting on :%s", serverVersion, port)
	if err := (&http.Server{Addr: ":" + port, Handler: handler}).ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func serveLandingPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	fmt.Fprint(w, landingHTML)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

// --- Rate Limiter ---

type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*ipLimiter
	rate     rate.Limit
	burst    int
}

func newRateLimiter(r rate.Limit, burst int) *rateLimiter {
	rl := &rateLimiter{
		visitors: make(map[string]*ipLimiter),
		rate:     r,
		burst:    burst,
	}
	go rl.cleanup()
	return rl
}

func (rl *rateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, exists := rl.visitors[ip]
	if !exists {
		limiter := rate.NewLimiter(rl.rate, rl.burst)
		rl.visitors[ip] = &ipLimiter{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}
	v.lastSeen = time.Now()
	return v.limiter
}

func (rl *rateLimiter) cleanup() {
	for {
		time.Sleep(5 * time.Minute)
		rl.mu.Lock()
		for ip, v := range rl.visitors {
			if time.Since(v.lastSeen) > 10*time.Minute {
				delete(rl.visitors, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *rateLimiter) middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip == "" {
			ip = r.RemoteAddr
		}
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			ip = fwd
		}

		limiter := rl.getLimiter(ip)
		if !limiter.Allow() {
			log.Printf("Rate limit exceeded for %s on %s", ip, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded","retry_after":60}`))
			return
		}
		next(w, r)
	}
}

const robotsTxt = `User-agent: *
Allow: /
Disallow: /sse
Disallow: /message
Disallow: /health

Sitemap: https://livescoremcp.com/sitemap.xml
`

const sitemapXML = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>https://livescoremcp.com/</loc>
    <lastmod>2026-02-27</lastmod>
    <changefreq>weekly</changefreq>
    <priority>1.0</priority>
  </url>
  <url>
    <loc>https://livescoremcp.com/privacy</loc>
    <lastmod>2026-02-24</lastmod>
    <changefreq>monthly</changefreq>
    <priority>0.3</priority>
  </url>
  <url>
    <loc>https://livescoremcp.com/terms</loc>
    <lastmod>2026-02-26</lastmod>
    <changefreq>monthly</changefreq>
    <priority>0.3</priority>
  </url>
</urlset>
`

const landingHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="google-site-verification" content="-pqJ43CJw50bMGSEVUOCp70hPo68NYDT6GB1qGQJFPM">
<!-- Google Analytics -->
<script async src="https://www.googletagmanager.com/gtag/js?id=G-3J7HVJS6ZB"></script>
<script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag('js',new Date());gtag('config','G-3J7HVJS6ZB');</script>
<meta name="theme-color" content="#06080f">
<link rel="icon" href="/static/favicon.svg" type="image/svg+xml">
<link rel="apple-touch-icon" href="/static/og-image.png">

<!-- Primary Meta Tags -->
<title>LiveScore MCP - Football Live Scores API for AI Agents</title>
<meta name="title" content="LiveScore MCP - Football Live Scores API for AI Agents">
<meta name="description" content="Free MCP server for real-time football scores, fixtures, team stats and player data. Connect Claude, Cursor or any AI agent to 1000+ leagues worldwide.">
<meta name="keywords" content="MCP server, football live scores, Model Context Protocol, AI football data, live scores API, soccer API, Claude MCP, football fixtures, SSE transport">
<meta name="author" content="holoduke">
<meta name="robots" content="index, follow">
<link rel="canonical" href="https://livescoremcp.com/">

<!-- Open Graph / Facebook -->
<meta property="og:type" content="website">
<meta property="og:url" content="https://livescoremcp.com/">
<meta property="og:title" content="LiveScore MCP - Football Live Scores for AI Agents">
<meta property="og:description" content="Free MCP server with 10 tools for real-time football scores, fixtures, team stats and player data. Works with Claude, Cursor and any MCP client.">
<meta property="og:image" content="https://livescoremcp.com/static/og-image.png">
<meta property="og:image:width" content="1024">
<meta property="og:image:height" content="1024">
<meta property="og:image:alt" content="LiveScore MCP - Football Live Scores API for AI Agents">
<meta property="og:site_name" content="LiveScore MCP">
<meta property="og:locale" content="en_US">

<!-- Twitter -->
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:url" content="https://livescoremcp.com/">
<meta name="twitter:title" content="LiveScore MCP - Football Live Scores for AI Agents">
<meta name="twitter:description" content="Free MCP server with 10 tools for real-time football scores, fixtures, team stats and player data. Works with Claude, Cursor and any MCP client.">
<meta name="twitter:image" content="https://livescoremcp.com/static/og-image.png">
<meta name="twitter:image:alt" content="LiveScore MCP - Football Live Scores API for AI Agents">

<!-- Schema.org JSON-LD: SoftwareApplication -->
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "SoftwareApplication",
  "name": "LiveScore MCP",
  "url": "https://livescoremcp.com",
  "applicationCategory": "DeveloperApplication",
  "operatingSystem": "Any",
  "description": "Free MCP server providing real-time football live scores, fixtures, team statistics, player data, and match details via the Model Context Protocol. Supports 1000+ leagues worldwide with SSE transport.",
  "offers": {
    "@type": "Offer",
    "price": "0",
    "priceCurrency": "USD"
  },
  "author": {
    "@type": "Organization",
    "name": "holoduke",
    "url": "https://github.com/holoduke"
  },
  "softwareVersion": "1.0.0",
  "datePublished": "2026-02-20",
  "dateModified": "2026-02-27",
  "codeRepository": "https://github.com/holoduke/livescore-mcp",
  "programmingLanguage": "Go",
  "screenshot": "https://livescoremcp.com/static/og-image.png",
  "installUrl": "https://livescoremcp.com/",
  "keywords": ["MCP", "Model Context Protocol", "football", "live scores", "soccer", "API", "AI", "Claude", "SSE"]
}
</script>

<!-- Schema.org JSON-LD: FAQPage -->
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "FAQPage",
  "mainEntity": [
    {
      "@type": "Question",
      "name": "What is LiveScore MCP?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "LiveScore MCP is a free Model Context Protocol (MCP) server that provides real-time football live scores, fixtures, team statistics, player data, and match details. It connects AI agents like Claude, Cursor, and other MCP-compatible clients to comprehensive football data from 1000+ leagues worldwide."
      }
    },
    {
      "@type": "Question",
      "name": "How do I connect to LiveScore MCP?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Connect any MCP client to the SSE endpoint at https://livescoremcp.com/sse. For Claude Desktop, add the URL to your claude_desktop_config.json under mcpServers with the key livescore and url https://livescoremcp.com/sse."
      }
    },
    {
      "@type": "Question",
      "name": "What tools does LiveScore MCP provide?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "LiveScore MCP provides 10 tools: get_live_scores for real-time match scores, get_fixtures for competition fixtures, search for finding teams/players/competitions, get_league_fixtures for league-specific data, get_team for team details, get_player for player profiles, get_match for full match details with head-to-head data, get_day_fixtures for all matches on a date, get_team_image for team logos, and a health check tool."
      }
    },
    {
      "@type": "Question",
      "name": "Is LiveScore MCP free to use?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Yes, LiveScore MCP is free for personal and non-commercial use. The source code is available on GitHub at github.com/holoduke/livescore-mcp. Rate limits apply. For commercial use or higher rate limits, contact gillis.haasnoot@gmail.com."
      }
    },
    {
      "@type": "Question",
      "name": "What leagues and competitions are supported?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "LiveScore MCP covers 1000+ football leagues and competitions worldwide, including the Premier League, La Liga, Serie A, Bundesliga, Eredivisie, Ligue 1, Champions League, Europa League, World Cup, and many more domestic and international tournaments."
      }
    }
  ]
}
</script>

<!-- Schema.org JSON-LD: WebSite -->
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "WebSite",
  "name": "LiveScore MCP",
  "url": "https://livescoremcp.com",
  "description": "Free MCP server for real-time football scores, fixtures, team stats and player data for AI agents.",
  "publisher": {
    "@type": "Organization",
    "name": "holoduke",
    "url": "https://github.com/holoduke"
  }
}
</script>

<link rel="dns-prefetch" href="https://github.com">

<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  html, body { width: 100%; min-height: 100vh; background: #06080f; overflow-x: hidden; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif; color: #e0e6ed; }

  #grid-container {
    width: 100%;
    display: grid;
    gap: 6px;
  }

  .grid-cell {
    position: relative;
    overflow: hidden;
    transition: filter 0.3s ease, transform 0.15s ease;
    cursor: pointer;
    background-size: cover;
    background-position: center;
  }

  .grid-cell::after {
    content: '';
    position: absolute;
    inset: 0;
    background:
      repeating-linear-gradient(0deg, transparent, transparent 2px, rgba(0,255,200,0.03) 2px, rgba(0,255,200,0.03) 4px),
      linear-gradient(160deg, rgba(0,30,60,0.55) 0%, rgba(0,15,30,0.45) 50%, rgba(0,40,50,0.5) 100%);
    pointer-events: none;
    z-index: 1;
    transition: opacity 0.3s ease;
  }

  .grid-cell:hover {
    filter: brightness(1.3);
    z-index: 10;
    transform: scale(1.02);
  }

  .grid-cell:hover::after {
    opacity: 0.4;
  }

  .grid-cell.pulse::after {
    animation: cellPulse 3s ease-in-out forwards;
  }

  @keyframes cellPulse {
    0% { opacity: 1; }
    35% { opacity: 0.3; }
    65% { opacity: 0.3; }
    100% { opacity: 1; }
  }

  /* Content overlay */
  #overlay {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    z-index: 100;
    display: flex;
    flex-direction: column;
    align-items: center;
    pointer-events: none;
    padding: 80px 20px 60px;
  }

  #title {
    font-family: 'Arial Black', 'Impact', sans-serif;
    font-size: clamp(48px, 8vw, 120px);
    font-weight: 900;
    color: #fff;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    text-align: center;
    line-height: 1.05;
    -webkit-text-stroke: 5px rgba(0,0,0,0.8);
    paint-order: stroke fill;
    text-shadow:
      0 0 40px rgba(0,0,0,0.9),
      0 0 80px rgba(0,0,0,0.6),
      0 6px 0 rgba(0,0,0,0.7);
  }

  .card {
    background: rgba(0,0,0,0.75);
    backdrop-filter: blur(16px);
    -webkit-backdrop-filter: blur(16px);
    border: 1px solid rgba(255,255,255,0.12);
    border-radius: 16px;
    padding: 24px 28px;
    max-width: 640px;
    width: 92%;
    pointer-events: auto;
  }

  #chat-card { margin-top: 32px; height: 280px; overflow-y: auto; }
  #steps-card { margin-top: 20px; }

  #steps-card h3 {
    font-size: 13px;
    font-weight: 700;
    color: rgba(255,255,255,0.45);
    text-transform: uppercase;
    letter-spacing: 0.1em;
    margin-bottom: 18px;
  }

  .step { display: flex; gap: 14px; margin-bottom: 16px; }
  .step:last-child { margin-bottom: 0; }

  .step-num {
    flex-shrink: 0; width: 28px; height: 28px; border-radius: 50%;
    background: rgba(59,130,246,0.2); border: 1px solid rgba(59,130,246,0.3);
    color: rgba(147,187,252,0.9); font-size: 13px; font-weight: 700;
    display: flex; align-items: center; justify-content: center;
  }

  .step-content { font-size: 14px; line-height: 1.5; color: rgba(255,255,255,0.85); }
  .step-content strong { color: #fff; }

  .endpoint {
    display: inline-block; margin-top: 6px; padding: 4px 10px;
    background: rgba(255,255,255,0.08); border: 1px solid rgba(255,255,255,0.12);
    border-radius: 6px; font-family: 'SF Mono', 'Fira Code', monospace;
    font-size: 12px; color: rgba(139,233,160,0.9); word-break: break-all;
  }

  .code-block {
    margin-top: 8px; padding: 12px 14px; background: rgba(0,0,0,0.5);
    border: 1px solid rgba(255,255,255,0.08); border-radius: 8px;
    font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px;
    line-height: 1.6; color: rgba(255,255,255,0.75); overflow-x: auto;
  }

  .code-block .ck { color: rgba(147,187,252,0.9); }
  .code-block .cv { color: rgba(139,233,160,0.9); }

  .chat-messages { display: flex; flex-direction: column; gap: 12px; }

  .chat-msg {
    font-size: 14px; line-height: 1.55; padding: 10px 14px;
    border-radius: 12px; max-width: 88%; opacity: 0;
    transform: translateY(6px); animation: chatIn 0.25s ease forwards;
  }

  .chat-msg.user {
    align-self: flex-end; background: rgba(59,130,246,0.22);
    border: 1px solid rgba(59,130,246,0.25); color: rgba(255,255,255,0.95);
    border-bottom-right-radius: 4px;
  }

  .chat-msg.bot {
    align-self: flex-start; background: rgba(255,255,255,0.06);
    border: 1px solid rgba(255,255,255,0.08); color: rgba(255,255,255,0.88);
    border-bottom-left-radius: 4px;
  }

  .chat-msg .label { font-size: 10px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.1em; margin-bottom: 3px; }
  .chat-msg.user .label { color: rgba(147,187,252,0.6); }
  .chat-msg.bot .label { color: rgba(139,233,160,0.6); }
  .chat-msg .body { min-height: 1.55em; }

  .cursor {
    display: inline-block; width: 2px; height: 1em;
    background: rgba(255,255,255,0.7); margin-left: 1px;
    vertical-align: text-bottom; animation: blink 0.6s step-end infinite;
  }

  @keyframes blink { 50% { opacity: 0; } }
  @keyframes chatIn { to { opacity: 1; transform: translateY(0); } }
  @keyframes gradientShift { 0%,100% { background-position: 0% 50%; } 50% { background-position: 100% 50%; } }
  @keyframes livePulse { 0%,100% { opacity: 1; transform: scale(1); } 50% { opacity: 0.5; transform: scale(1.4); } }

  /* --- Sections --- */
  .section { max-width: 780px; width: 92%; padding: 40px 28px 48px; margin-top: 20px; pointer-events: auto; background: rgba(0,0,0,0.75); backdrop-filter: blur(16px); -webkit-backdrop-filter: blur(16px); border: 1px solid rgba(255,255,255,0.12); border-radius: 16px; }
  .section-label { display: inline-block; font-size: 0.75rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.1em; color: #4ade80; background: rgba(74,222,128,0.1); padding: 6px 14px; border-radius: 100px; margin-bottom: 16px; }
  .section-title { font-size: clamp(1.5rem,3vw,2rem); font-weight: 800; color: #f1f5f9; margin-bottom: 12px; letter-spacing: -0.02em; background: linear-gradient(135deg,#f1f5f9 0%,#4ade80 50%,#22d3ee 100%); background-size: 200% 200%; animation: gradientShift 6s ease infinite; -webkit-background-clip: text; -webkit-text-fill-color: transparent; background-clip: text; }
  .section-desc { color: #94a3b8; font-size: 1rem; line-height: 1.7; max-width: 600px; }

  /* --- Tools Grid --- */
  .tools-grid { display: grid; grid-template-columns: repeat(auto-fill,minmax(260px,1fr)); gap: 16px; margin-top: 32px; }
  .tool-card { background: rgba(255,255,255,0.03); border: 1px solid rgba(255,255,255,0.06); border-left: 3px solid; border-image: linear-gradient(180deg,#4ade80,#22d3ee) 1; border-radius: 14px; padding: 24px; transition: all 0.3s ease; cursor: default; }
  .tool-card:hover { transform: translateY(-4px); box-shadow: 0 0 0 2px rgba(74,222,128,0.15), 0 12px 40px rgba(74,222,128,0.12); border-color: rgba(74,222,128,0.25); }
  .tool-icon { font-size: 1.5rem; margin-bottom: 12px; display: block; }
  .tool-card h3 { font-family: 'SF Mono', Consolas, monospace; color: #4ade80; font-size: 0.9rem; margin-bottom: 8px; font-weight: 700; }
  .tool-card p { color: #94a3b8; font-size: 0.82rem; line-height: 1.6; }

  .live-dot { display: inline-block; width: 8px; height: 8px; background: #4ade80; border-radius: 50%; margin-right: 6px; animation: livePulse 1.5s ease-in-out infinite; vertical-align: middle; box-shadow: 0 0 8px rgba(74,222,128,0.6); }

  /* --- Powered By --- */
  .powered-card { display: flex; align-items: center; gap: 24px; background: rgba(255,255,255,0.03); border: 1px solid rgba(255,255,255,0.06); border-radius: 16px; padding: 32px; margin-top: 32px; transition: border-color 0.3s; }
  .powered-card:hover { border-color: rgba(74,222,128,0.2); }
  .powered-icon { font-size: 2.5rem; flex-shrink: 0; }
  .powered-card h3 { font-size: 1rem; font-weight: 700; color: #f1f5f9; margin-bottom: 6px; }
  .powered-card h3 a { color: #4ade80; text-decoration: none; transition: color 0.2s; }
  .powered-card h3 a:hover { color: #22d3ee; text-decoration: underline; }
  .powered-card p { color: #94a3b8; font-size: 0.85rem; line-height: 1.6; }

  /* --- Get the App --- */
  .app-badges { display: flex; flex-wrap: wrap; justify-content: center; gap: 16px; margin-top: 32px; }
  .app-badge { display: inline-flex; align-items: center; gap: 12px; padding: 14px 28px; border-radius: 14px; background: rgba(255,255,255,0.05); border: 1px solid rgba(255,255,255,0.1); text-decoration: none; color: #e0e6ed; font-weight: 600; font-size: 0.9rem; transition: all 0.3s ease; }
  .app-badge:hover { transform: translateY(-3px); box-shadow: 0 0 0 2px rgba(74,222,128,0.2), 0 12px 32px rgba(74,222,128,0.15); border-color: rgba(74,222,128,0.3); background: rgba(255,255,255,0.08); }
  .app-badge svg { flex-shrink: 0; }
  .app-badge-text { display: flex; flex-direction: column; line-height: 1.2; }
  .app-badge-small { font-size: 0.65rem; font-weight: 400; color: #94a3b8; text-transform: uppercase; letter-spacing: 0.05em; }
  .app-badge-store { font-size: 1rem; font-weight: 700; color: #fff; }
  .app-tagline { text-align: center; margin-top: 20px; color: #94a3b8; font-size: 0.9rem; font-style: italic; }

  /* --- Usage Policy --- */
  .policy-grid { display: grid; grid-template-columns: repeat(auto-fill,minmax(180px,1fr)); gap: 16px; margin-top: 32px; }
  .policy-card { background: rgba(255,255,255,0.03); border: 1px solid rgba(255,255,255,0.06); border-radius: 14px; padding: 24px; transition: border-color 0.3s; }
  .policy-card:hover { border-color: rgba(255,255,255,0.12); }
  .policy-icon { font-size: 1.5rem; margin-bottom: 12px; display: block; }
  .policy-card h3 { font-size: 0.95rem; font-weight: 700; color: #f1f5f9; margin-bottom: 8px; }
  .policy-card p { color: #94a3b8; font-size: 0.85rem; line-height: 1.7; }
  .policy-card a { color: #4ade80; text-decoration: none; font-weight: 600; }
  .policy-card a:hover { text-decoration: underline; }
  .policy-note { margin-top: 24px; padding: 20px 24px; background: rgba(234,179,8,0.06); border: 1px solid rgba(234,179,8,0.15); border-radius: 12px; color: #94a3b8; font-size: 0.85rem; line-height: 1.7; }
  .policy-note strong { color: #eab308; }

  /* --- Footer --- */
  .site-footer { max-width: 780px; width: 92%; border-radius: 16px; padding: 40px 28px; pointer-events: auto; background: rgba(0,0,0,0.75); backdrop-filter: blur(16px); -webkit-backdrop-filter: blur(16px); border: 1px solid rgba(255,255,255,0.12); margin-bottom: 40px; }
  .footer-inner { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 16px; }
  .footer-links { display: flex; gap: 24px; flex-wrap: wrap; }
  .footer-links a { color: #64748b; text-decoration: none; font-size: 0.85rem; font-weight: 500; transition: color 0.2s; }
  .footer-links a:hover { color: #4ade80; }
  .footer-built { color: #475569; font-size: 0.82rem; }
  .footer-built a { color: #64748b; text-decoration: none; font-weight: 500; }
  .footer-built a:hover { color: #4ade80; }

  /* --- noscript --- */
  .noscript-content { max-width: 700px; margin: 60px auto; padding: 0 24px; color: #94a3b8; }
  .noscript-content h2 { color: #f1f5f9; margin: 24px 0 8px; }
  .noscript-content p { margin-bottom: 12px; line-height: 1.7; }
  .noscript-content a { color: #4ade80; }
  .noscript-content code { color: #22d3ee; background: rgba(34,211,238,0.1); padding: 2px 8px; border-radius: 4px; font-size: 0.9rem; }

  /* Mobile responsive */
  @media (max-width: 768px) {
    #overlay { padding: 40px 12px 40px; }
    #title { -webkit-text-stroke: 3px rgba(0,0,0,0.8); }
    .card { padding: 18px 18px; border-radius: 12px; }
    #chat-card { height: 240px; }
    .chat-msg { font-size: 13px; padding: 8px 12px; max-width: 92%; }
    .step { gap: 10px; }
    .step-content { font-size: 13px; }
    .code-block { font-size: 11px; padding: 10px 12px; }
    .endpoint { font-size: 11px; }
    .tools-grid { grid-template-columns: 1fr; }
    .section { padding: 32px 20px 40px; }
    .section, .site-footer { width: 96%; }
    .policy-grid { grid-template-columns: 1fr; }
    .policy-note { padding: 16px; }
    .powered-card { flex-direction: column; text-align: center; }
    .footer-inner { flex-direction: column; text-align: center; }
    .footer-links { justify-content: center; }
    .footer-built { text-align: center; font-size: 0.75rem; }
    .site-footer { padding: 32px 20px; }
  }

  @media (max-width: 480px) {
    #overlay { padding: 24px 8px 30px; }
    #title { font-size: clamp(32px, 10vw, 56px); -webkit-text-stroke: 2px rgba(0,0,0,0.8); }
    .card { padding: 14px 14px; max-width: 100%; width: 96%; }
    #chat-card { height: 200px; margin-top: 20px; }
    #steps-card { margin-top: 14px; }
    .chat-msg { font-size: 12px; padding: 7px 10px; }
    .chat-msg .label { font-size: 9px; }
    .step-num { width: 24px; height: 24px; font-size: 11px; }
    .step-content { font-size: 12px; }
    .code-block { font-size: 10px; padding: 8px 10px; }
    .section { padding: 24px 16px 32px; }
    .section, .site-footer { width: 98%; }
    .app-badges { flex-direction: column; align-items: center; }
    .app-badge { width: 100%; justify-content: center; }
    .site-footer { padding: 24px 16px; }
  }
</style>
</head>
<body>

<div id="overlay">
  <h1 id="title">Football<br>Livescore MCP</h1>

  <div class="card" id="chat-card" aria-label="Live demo of AI football queries">
    <div class="chat-messages" id="chat"></div>
  </div>

  <div class="card" id="steps-card">
    <h3>Get Started</h3>
    <div class="step">
      <div class="step-num">1</div>
      <div class="step-content">
        <strong>Connect your MCP client</strong> to the SSE endpoint:
        <div class="endpoint">https://livescoremcp.com/sse</div>
      </div>
    </div>
    <div class="step">
      <div class="step-num">2</div>
      <div class="step-content">
        <strong>Add to Claude Desktop</strong> &mdash; edit your config file:
        <div class="code-block">
{<br>
&nbsp;&nbsp;<span class="ck">"mcpServers"</span>: {<br>
&nbsp;&nbsp;&nbsp;&nbsp;<span class="ck">"livescore"</span>: {<br>
&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;<span class="ck">"url"</span>: <span class="cv">"https://livescoremcp.com/sse"</span><br>
&nbsp;&nbsp;&nbsp;&nbsp;}<br>
&nbsp;&nbsp;}<br>
}
        </div>
      </div>
    </div>
    <div class="step">
      <div class="step-num">3</div>
      <div class="step-content">
        <strong>Start asking!</strong> Live scores, fixtures, team stats, player data &mdash; 1000+ leagues worldwide, all free.
      </div>
    </div>
  </div>

  <!-- Capabilities -->
  <section class="section" id="tools">
    <span class="section-label">Capabilities</span>
    <h2 class="section-title">Available Football Data Tools</h2>
    <p class="section-desc">10 powerful tools to access real-time football data from leagues worldwide.</p>
    <div class="tools-grid">
      <article class="tool-card">
        <span class="tool-icon">&#9889;</span>
        <h3><span class="live-dot"></span>get_live_scores</h3>
        <p>Currently live matches with real-time scores and minute-by-minute updates from leagues worldwide</p>
      </article>
      <article class="tool-card">
        <span class="tool-icon">&#128197;</span>
        <h3>get_fixtures</h3>
        <p>Competition fixtures for Champions League, Europa League, World Cup, and 1000+ tournaments</p>
      </article>
      <article class="tool-card">
        <span class="tool-icon">&#128269;</span>
        <h3>search</h3>
        <p>Search teams, players, or competitions by name with optional country filter</p>
      </article>
      <article class="tool-card">
        <span class="tool-icon">&#127942;</span>
        <h3>get_league_fixtures</h3>
        <p>League-specific fixtures for Eredivisie, Premier League, La Liga, Serie A, Bundesliga and more</p>
      </article>
      <article class="tool-card">
        <span class="tool-icon">&#128101;</span>
        <h3>get_team</h3>
        <p>Detailed team info including squad, statistics, upcoming matches, and recent results</p>
      </article>
      <article class="tool-card">
        <span class="tool-icon">&#9939;</span>
        <h3>get_player</h3>
        <p>Player profiles with career statistics, current team, transfer history, and performance data</p>
      </article>
      <article class="tool-card">
        <span class="tool-icon">&#128202;</span>
        <h3>get_match</h3>
        <p>Full match details with events, lineups, statistics, and head-to-head records</p>
      </article>
      <article class="tool-card">
        <span class="tool-icon">&#128467;</span>
        <h3>get_day_fixtures</h3>
        <p>All matches for a specific date across every league and competition worldwide</p>
      </article>
      <article class="tool-card">
        <span class="tool-icon">&#127912;</span>
        <h3>get_team_image</h3>
        <p>Team logo PNG URL for embedding in responses and AI-powered applications</p>
      </article>
      <article class="tool-card">
        <span class="tool-icon">&#128154;</span>
        <h3>health</h3>
        <p>Connectivity check &mdash; echo back a message to verify the MCP server is alive</p>
      </article>
    </div>
  </section>

  <!-- Powered By -->
  <section class="section" id="powered-by">
    <span class="section-label">Data Source</span>
    <h2 class="section-title">Powered By</h2>
    <p class="section-desc">LiveScore MCP is built on top of comprehensive football data.</p>
    <div class="powered-card">
      <span class="powered-icon">&#9917;</span>
      <div>
        <h3><a href="https://football-mania.com" target="_blank" rel="noopener">football-mania.com</a></h3>
        <p>Comprehensive football data platform providing real-time scores, fixtures, team statistics, player profiles, and match details from 1000+ leagues and competitions worldwide.</p>
      </div>
    </div>
  </section>

  <!-- Get the App -->
  <section class="section" id="get-app" style="text-align:center">
    <span class="section-label">Mobile App</span>
    <h2 class="section-title">Get the App</h2>
    <p class="section-desc" style="margin:0 auto 8px">Download Football Mania for live scores on the go.</p>
    <div class="app-badges">
      <a href="https://play.google.com/store/apps/details?id=holoduke.soccer_gen&amp;hl=en_IE" class="app-badge" target="_blank" rel="noopener">
        <svg width="28" height="28" viewBox="0 0 24 24" fill="none"><path d="M3.18 1.47l8.83 8.83L3.18 19.13c-.44-.78-.18-1.76.58-2.2L3.18 1.47zM14.5 12.79l2.63 2.63-10.72 6.19c-.42.24-.93.26-1.37.08l9.46-8.9zM21.02 10.45l-3.93-2.27-2.92 2.92 2.92 2.92 3.93-2.27c.78-.45 1.06-1.45.62-2.23l-.62.93zM5.02.38l10.72 6.19-2.63 2.63L3.65.31C4.09.12 4.6.14 5.02.38z" fill="#fff"/></svg>
        <span class="app-badge-text"><span class="app-badge-small">Get it on</span><span class="app-badge-store">Google Play</span></span>
      </a>
      <a href="https://apps.apple.com/us/app/football-mania-soccer-scores/id896357542" class="app-badge" target="_blank" rel="noopener">
        <svg width="28" height="28" viewBox="0 0 24 24" fill="#fff"><path d="M18.71 19.5c-.83 1.24-1.71 2.45-3.05 2.47-1.34.03-1.77-.79-3.29-.79-1.53 0-2 .77-3.27.82-1.31.05-2.3-1.32-3.14-2.53C4.25 17 2.94 12.45 4.7 9.39c.87-1.52 2.43-2.48 4.12-2.51 1.28-.02 2.5.87 3.29.87.78 0 2.26-1.07 3.8-.91.65.03 2.47.26 3.64 1.98-.09.06-2.17 1.28-2.15 3.81.03 3.02 2.65 4.03 2.68 4.04-.03.07-.42 1.44-1.37 2.83zM13 3.5c.73-.83 1.94-1.46 2.94-1.5.13 1.17-.34 2.35-1.04 3.19-.69.85-1.83 1.51-2.95 1.42-.15-1.15.41-2.35 1.05-3.11z"/></svg>
        <span class="app-badge-text"><span class="app-badge-small">Download on the</span><span class="app-badge-store">App Store</span></span>
      </a>
    </div>
    <p class="app-tagline">Your home for live football &mdash; powered by football-mania.com</p>
  </section>

  <!-- Usage Policy -->
  <section class="section" id="usage-policy">
    <span class="section-label">Fair Use</span>
    <h2 class="section-title">Usage Policy</h2>
    <p class="section-desc">LiveScore MCP is free for personal and non-commercial use. Please respect the following guidelines.</p>
    <div class="policy-grid">
      <div class="policy-card">
        <span class="policy-icon">&#9889;</span>
        <h3>Rate Limits Apply</h3>
        <p>To ensure fair access for everyone, rate limits are enforced. Excessive or automated bulk requests may be throttled or blocked.</p>
      </div>
      <div class="policy-card">
        <span class="policy-icon">&#128188;</span>
        <h3>Commercial Use</h3>
        <p>Using LiveScore MCP data in commercial products, paid services, or for-profit applications requires a commercial license. Contact <a href="mailto:gillis.haasnoot@gmail.com">gillis.haasnoot@gmail.com</a> for details.</p>
      </div>
      <div class="policy-card">
        <span class="policy-icon">&#128156;</span>
        <h3>Be Respectful</h3>
        <p>Do not abuse the service, scrape data aggressively, or use it in ways that degrade the experience for others. Keep it fair and friendly.</p>
      </div>
    </div>
    <div class="policy-note">
      <strong>&#9888; Note:</strong> Abuse of the service &mdash; including excessive requests, data scraping, or circumventing rate limits &mdash; may result in your access being permanently revoked. For commercial inquiries or higher rate limits, reach out to <a href="mailto:gillis.haasnoot@gmail.com" style="color:#eab308;text-decoration:none;font-weight:600">gillis.haasnoot@gmail.com</a>.
    </div>
  </section>

  <!-- Footer -->
  <footer class="site-footer">
    <div class="footer-inner">
      <div class="footer-links">
        <a href="https://github.com/holoduke/livescore-mcp">GitHub</a>
        <a href="/privacy">Privacy Policy</a>
        <a href="/terms">Terms of Service</a>
      </div>
      <div class="footer-built">Powered by <a href="https://football-mania.com" target="_blank" rel="noopener noreferrer">football-mania.com</a> &bull; Built with <a href="https://github.com/mark3labs/mcp-go" target="_blank" rel="noopener noreferrer">mcp-go</a> &bull; <a href="https://github.com/holoduke/livescore-mcp" target="_blank" rel="noopener noreferrer">Source on GitHub</a></div>
    </div>
  </footer>
</div>

<div id="grid-container" aria-hidden="true"></div>

<!-- SEO: Noscript fallback with key content for crawlers -->
<noscript>
<div class="noscript-content">
  <h2>LiveScore MCP - Football Live Scores for AI Agents</h2>
  <p>LiveScore MCP is a free Model Context Protocol (MCP) server providing real-time football live scores, fixtures, team statistics, player data, and match details from 1000+ leagues worldwide.</p>
  <p>Connect any MCP-compatible AI client (Claude Desktop, Claude Code, Cursor, Windsurf, Cline) to the SSE endpoint at <code>https://livescoremcp.com/sse</code></p>
  <h2>Available Tools</h2>
  <p>get_live_scores, get_fixtures, search, get_league_fixtures, get_team, get_player, get_match, get_day_fixtures, get_team_image, health</p>
  <h2>Links</h2>
  <p><a href="https://github.com/holoduke/livescore-mcp">GitHub Repository</a> | <a href="https://football-mania.com">Powered by football-mania.com</a></p>
</div>
</noscript>

<script>
var container = document.getElementById('grid-container');
var CELL_UNIT = 80;
var MIN_SPAN = 1;
var MAX_SPAN = 5;
var TOTAL_ROWS = 80;

var images = [
  'academy-drill','acrobatic-celebration','aerial-night-city','ajax-cruyff-turn',
  'ajax-youth-goal','anfield-roar','arsenal-goal-celebration','arsenal-passing',
  'atletico-grit','away-fans','baby-celebration','ball-closeup','ball-net',
  'ball-rain','barca-goal-camp-nou','barca-tiki-taka','bayern-header',
  'bayern-pressing','benfica-eagle','bicycle-kick','boots-hanging-wire',
  'boots-pitch','celebration-knee','celtic-park-roar','champions-trophy',
  'chip-goal','city-possession','city-title-win','coach-tactics','corner-flag',
  'corner-kick','crowd-mosaic','dortmund-counter-goal','dortmund-yellow-wall',
  'dressing-room','dribble-skill','empty-stadium-dawn','fan-tears-joy',
  'fans-celebrating','feyenoord-de-kuip','finger-lips','floodlight-tower',
  'fog-stadium','formation-board','free-kick','gloves-grip','goal-line-tech',
  'goalkeeper-dive','grass-dew-morning','grass-divot','handshake-line',
  'header-goal','injury-time-goal','inter-derby-goal','juve-defensive',
  'juve-freekick-goal','keeper-punch','keeper-throw','keeper-wall','kid-fan',
  'kids-match','last-man-tackle','liverpool-counter-press','long-range-strike',
  'madrid-champions','madrid-counter','manager-touchline','matchday-program',
  'medal-ceremony','milan-celebration-corner','milan-san-siro','napoli-maradona',
  'net-texture','offside-line','old-boots','park-kickabout','penalty-kick',
  'pitch-invasion','pitch-lines','pitch-mowing','players-tunnel-lineup',
  'porto-dragao','pressing-trigger','psg-attack-trio','psg-skill-move',
  'rain-puddle','red-card','scoreboard-classic','shadow-player','shin-guards',
  'shirt-off','slide-tackle','snow-match','spotlight-player','stadium-aerial',
  'stadium-fireworks','stadium-night','striker-volley','substitution-board',
  'sunday-league','sunset-warmup','team-huddle','tears-defeat','through-ball',
  'training-session','trophy-room','tunnel-walkout','turnstile','ultras-smoke',
  'var-screen','wall-block','warmup-rondo','world-cup-lift'
];

function shuffle(arr) {
  var a = arr.slice();
  for (var i = a.length - 1; i > 0; i--) {
    var j = Math.floor(Math.random() * (i + 1));
    var t = a[i]; a[i] = a[j]; a[j] = t;
  }
  return a;
}

function generateGrid() {
  var viewportWidth = window.innerWidth;
  var cols = Math.floor(viewportWidth / CELL_UNIT);
  lastCols = cols;

  container.style.gridTemplateColumns = 'repeat(' + cols + ', ' + CELL_UNIT + 'px)';
  container.style.gridAutoRows = CELL_UNIT + 'px';
  container.innerHTML = '';

  var occupied = {};
  var cellImageMap = {};

  function isOcc(row, col, s) {
    for (var r = row; r < row + s; r++)
      for (var c = col; c < col + s; c++)
        if (c >= cols || occupied[r + ',' + c]) return true;
    return false;
  }

  function markOcc(row, col, s, idx) {
    for (var r = row; r < row + s; r++)
      for (var c = col; c < col + s; c++) {
        occupied[r + ',' + c] = true;
        cellImageMap[r + ',' + c] = idx;
      }
  }

  function getNeighborImages(row, col, span) {
    var used = {};
    for (var r = row - 1; r <= row + span; r++)
      for (var c = col - 1; c <= col + span; c++) {
        if (r >= row && r < row + span && c >= col && c < col + span) continue;
        var key = r + ',' + c;
        if (cellImageMap[key] !== undefined) used[cellImageMap[key]] = true;
      }
    return used;
  }

  function pickImage(row, col, span) {
    var neighborImgs = getNeighborImages(row, col, span);
    var candidates = [];
    for (var i = 0; i < images.length; i++)
      if (!neighborImgs[i]) candidates.push(i);
    if (candidates.length === 0) return Math.floor(Math.random() * images.length);
    return candidates[Math.floor(Math.random() * candidates.length)];
  }

  var cells = [];
  for (var row = 0; row < TOTAL_ROWS; row++) {
    for (var col = 0; col < cols; col++) {
      if (occupied[row + ',' + col]) continue;
      var maxS = Math.min(MAX_SPAN, cols - col, TOTAL_ROWS - row);
      var span;
      var rnd = Math.random();
      if (rnd < 0.15) span = 1;
      else if (rnd < 0.4) span = 2;
      else if (rnd < 0.65) span = 3;
      else if (rnd < 0.85) span = 4;
      else span = 5;
      span = Math.min(span, maxS);
      while (span > 1 && isOcc(row, col, span)) span--;
      if (isOcc(row, col, span)) continue;
      var imgIdx = pickImage(row, col, span);
      markOcc(row, col, span, imgIdx);
      cells.push({ row: row + 1, col: col + 1, span: span, image: '/static/grid/' + images[imgIdx] + '.webp' });
    }
  }

  var fragment = document.createDocumentFragment();
  for (var i = 0; i < cells.length; i++) {
    var cell = cells[i];
    var div = document.createElement('div');
    div.className = 'grid-cell';
    div.style.gridRow = cell.row + ' / span ' + cell.span;
    div.style.gridColumn = cell.col + ' / span ' + cell.span;
    div.style.backgroundImage = 'url(' + cell.image + ')';
    fragment.appendChild(div);
  }
  container.appendChild(fragment);
}

var lastCols = 0;
generateGrid();

function clipGrid() {
  var overlay = document.getElementById('overlay');
  if (overlay) container.style.maxHeight = overlay.offsetHeight + 'px';
  container.style.overflow = 'hidden';
}
setTimeout(clipGrid, 200);
setTimeout(clipGrid, 600);
window.addEventListener('load', function() { clipGrid(); setTimeout(clipGrid, 300); });
if (window.ResizeObserver) {
  new ResizeObserver(clipGrid).observe(document.getElementById('overlay'));
}

// Random cell pulse effect — one at a time
function pulseRandomCell() {
  var cells = container.querySelectorAll('.grid-cell:not(.pulse)');
  if (cells.length === 0) return;
  var idx = Math.floor(Math.random() * cells.length);
  var cell = cells[idx];
  cell.classList.add('pulse');
  cell.addEventListener('animationend', function() {
    this.classList.remove('pulse');
  }, { once: true });
}
setInterval(pulseRandomCell, 100);

var resizeTimer;
window.addEventListener('resize', function() {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(function() {
    var newCols = Math.floor(window.innerWidth / CELL_UNIT);
    if (newCols !== lastCols) generateGrid();
    clipGrid();
  }, 300);
});

// Chat simulation
var chatEl = document.getElementById('chat');
var chatCard = document.getElementById('chat-card');

var presets = [
  { q: 'How is Barcelona doing this week?', a: 'Barcelona had a fantastic week! They won 1-2 away at Real Madrid on Saturday \u2014 Lewandowski opened the scoring in the 23rd minute and Lamine Yamal sealed it with a curler in the 78th. They also beat Villarreal 3-0 midweek.' },
  { q: 'What are the live scores right now?', a: 'There are 14 matches currently in play. Manchester City leads Arsenal 2-1 (67\'), Bayern Munich and Dortmund are tied 1-1 (54\'), and Napoli is beating Juventus 3-0 (72\'). PSG vs Lyon just kicked off.' },
  { q: 'Who scored in the Champions League tonight?', a: 'Big night in the Champions League! Haaland scored a hat-trick for City vs PSG, Mbapp\u00e9 got two for Madrid against Atletico, Saka netted a brilliant free kick for Arsenal, and Musiala scored the winner for Bayern in the 89th minute.' },
  { q: 'Show me the Premier League table', a: 'The current Premier League standings: 1. Arsenal \u2014 58 pts, 2. Liverpool \u2014 55 pts, 3. Man City \u2014 53 pts, 4. Aston Villa \u2014 46 pts, 5. Tottenham \u2014 44 pts. Arsenal lead on goal difference after their win over Chelsea yesterday.' },
  { q: 'Any red cards today?', a: 'Two red cards so far today. Bruno Fernandes received a straight red in the 34th minute for a studs-up challenge on Rice during Man United vs Arsenal. Also Gvardiol got a second yellow in the 78th for pulling back Salah in City vs Liverpool.' },
  { q: 'What matches are on this Saturday?', a: 'Packed Saturday coming up! Premier League: Arsenal vs Chelsea (12:30), Man City vs Liverpool (17:30). La Liga: Barcelona vs Atletico (16:15). Serie A: AC Milan vs Inter (18:00). Bundesliga: Bayern vs Dortmund (18:30). Plus 23 more matches across Europe.' },
  { q: 'Tell me about Ajax\'s squad', a: 'Ajax\'s current squad features captain Steven Berghuis in midfield, Kenneth Taylor as the creative engine, and Brian Brobbey leading the attack. In defense, Josip Sutalo anchors the backline. Key youngsters include Jorrel Hato (17) at left-back and Mika Godts on the wing.' },
  { q: 'How did the Eredivisie matchday go?', a: 'Eredivisie Matchday 24 results: PSV 4-0 Twente (dominant), Feyenoord 2-1 AZ (Gimenez winner), Ajax 3-1 Utrecht (Brobbey brace), NEC 0-0 Heerenveen. PSV extend their lead to 8 points at the top. Ajax move into third.' },
  { q: 'Who is the top scorer in La Liga?', a: 'La Liga top scorers this season: 1. Robert Lewandowski (Barcelona) \u2014 18 goals, 2. Vin\u00edcius Jr (Real Madrid) \u2014 14 goals, 3. Antoine Griezmann (Atletico) \u2014 13 goals, 4. Ayoze P\u00e9rez (Villarreal) \u2014 11 goals, 5. Lamine Yamal (Barcelona) \u2014 10 goals.' },
  { q: 'What happened in the Manchester derby?', a: 'What a derby! Manchester City beat United 3-1 at Old Trafford. Haaland scored twice in the first half, Foden added a third after the break. Rashford pulled one back in stoppage time but it was too little too late. City dominated possession with 68%.' },
  { q: 'Give me head-to-head stats for Milan vs Inter', a: 'AC Milan vs Inter Milan \u2014 last 10 meetings: Inter have won 5, Milan 3, with 2 draws. Inter have scored 18 goals to Milan\'s 12 in those matches. Last meeting: Inter 1-2 Milan (Le\u00e3o brace). The Derby della Madonnina is this Sunday at San Siro.' },
  { q: 'Any upsets in the Champions League this week?', a: 'Massive upsets this week! Club Brugge knocked out PSG 4-2 on aggregate, Feyenoord beat Bayern Munich 3-1 in Munich, and Celtic held Real Madrid to a 0-0 draw at the Bernab\u00e9u. The standout was Brugge\'s incredible second-half comeback from 2-0 down.' },
  { q: 'What\'s the score in the Liverpool game?', a: 'Liverpool 2-0 Everton (62nd minute). Salah opened the scoring with a penalty in the 15th minute after a handball by Tarkowski. D\u00edaz doubled the lead with a fantastic solo run and finish in the 51st. Liverpool looking comfortable in the Merseyside derby.' },
  { q: 'Show me tomorrow\'s fixtures', a: 'Tomorrow\'s highlights: Serie A \u2014 Roma vs Lazio (Derby della Capitale, 15:00), Napoli vs Atalanta (18:00). Ligue 1 \u2014 PSG vs Marseille (Le Classique, 20:45). La Liga \u2014 Real Sociedad vs Athletic Bilbao (Basque derby, 16:15). Plus 18 more matches.' },
  { q: 'Who got injured in the Arsenal game?', a: 'Bad news for Arsenal \u2014 Bukayo Saka went down holding his hamstring in the 63rd minute and was substituted for Trossard. Early reports suggest it could be a Grade 2 strain, potentially ruling him out for 4-6 weeks. Martin \u00d8degaard also took a knock but played on.' },
  { q: 'How is Napoli doing in Serie A?', a: 'Napoli are flying! Currently 2nd in Serie A, just 3 points behind Inter. They\'ve won 8 of their last 10 matches, with Kvara and Osimhen combining for 24 goals. Their defense has been the best in the league \u2014 only 18 goals conceded in 26 matches.' },
  { q: 'What are the Europa League results?', a: 'Europa League Round of 16 results: Roma 2-1 Benfica, West Ham 0-3 Leverkusen, Atalanta 4-0 Sporting CP, Marseille 1-1 Brighton. Leverkusen look unstoppable with their 28-match unbeaten run extending. Atalanta\'s Lookman scored a hat-trick.' },
  { q: 'Tell me about the World Cup qualifiers', a: 'World Cup 2026 qualifying update \u2014 Europe: France leads Group B with 16 pts, Netherlands top Group D with 14 pts, England struggling in 2nd in Group C behind Serbia. South America: Argentina 1st with 21 pts, Colombia 2nd. Next matchday is in March.' },
  { q: 'How did the cup final go?', a: 'The League Cup final was a thriller! Chelsea beat Liverpool 2-1 at Wembley. Palmer gave Chelsea the lead from the spot, Salah equalized in the 67th, but Jackson scored an absolute screamer from 25 yards in the 84th minute to win it. Chelsea\'s first trophy in 2 years.' },
  { q: 'What\'s new with transfers?', a: 'Transfer window heating up! Victor Osimhen is in advanced talks with PSG for a \u20ac120M move. Arsenal are closing in on Florian Wirtz from Leverkusen. Manchester United have bid \u20ac75M for Joao Neves from Benfica. Barcelona are trying to sign Joshua Kimmich on a free.' }
];

function shuffleArray(arr) {
  for (var i = arr.length - 1; i > 0; i--) {
    var j = Math.floor(Math.random() * (i + 1));
    var t = arr[i]; arr[i] = arr[j]; arr[j] = t;
  }
}
var presetQueue = presets.slice();
shuffleArray(presetQueue);
var queueIdx = 0;

function getNextPreset() {
  if (queueIdx >= presetQueue.length) {
    presetQueue = presets.slice();
    shuffleArray(presetQueue);
    queueIdx = 0;
  }
  return presetQueue[queueIdx++];
}

function createBubble(role) {
  var div = document.createElement('div');
  div.className = 'chat-msg ' + role;
  var label = document.createElement('div');
  label.className = 'label';
  label.textContent = role === 'user' ? 'You' : 'LiveScore MCP';
  div.appendChild(label);
  var body = document.createElement('div');
  body.className = 'body';
  div.appendChild(body);
  chatEl.appendChild(div);
  chatCard.scrollTop = chatCard.scrollHeight;
  return body;
}

function streamText(el, text, speed) {
  return new Promise(function(resolve) {
    var words = text.split(' ');
    var i = 0;
    var cursor = document.createElement('span');
    cursor.className = 'cursor';
    el.appendChild(cursor);
    function tick() {
      if (i < words.length) {
        if (i > 0) el.insertBefore(document.createTextNode(' '), cursor);
        el.insertBefore(document.createTextNode(words[i]), cursor);
        i++;
        chatCard.scrollTop = chatCard.scrollHeight;
        var jitter = speed + Math.random() * 30 - 15;
        setTimeout(tick, Math.max(15, jitter));
      } else {
        cursor.remove();
        resolve();
      }
    }
    tick();
  });
}

function typeUser(el, text) {
  return new Promise(function(resolve) {
    var chars = text.split('');
    var i = 0;
    var cursor = document.createElement('span');
    cursor.className = 'cursor';
    el.appendChild(cursor);
    function tick() {
      if (i < chars.length) {
        el.insertBefore(document.createTextNode(chars[i]), cursor);
        i++;
        chatCard.scrollTop = chatCard.scrollHeight;
        setTimeout(tick, 25 + Math.random() * 25);
      } else {
        cursor.remove();
        resolve();
      }
    }
    tick();
  });
}

async function runChat() {
  while (true) {
    var preset = getNextPreset();
    var userBody = createBubble('user');
    await typeUser(userBody, preset.q);
    await new Promise(function(r) { setTimeout(r, 600 + Math.random() * 400); });
    var botBody = createBubble('bot');
    await streamText(botBody, preset.a, 35);
    await new Promise(function(r) { setTimeout(r, 3000); });
    chatEl.style.transition = 'opacity 0.4s';
    chatEl.style.opacity = '0';
    await new Promise(function(r) { setTimeout(r, 450); });
    chatEl.innerHTML = '';
    chatEl.style.opacity = '1';
    await new Promise(function(r) { setTimeout(r, 300); });
  }
}

setTimeout(runChat, 800);
</script>

</body>
</html>`

const privacyHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Privacy Policy - LiveScore MCP</title>
<script async src="https://www.googletagmanager.com/gtag/js?id=G-3J7HVJS6ZB"></script>
<script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag('js',new Date());gtag('config','G-3J7HVJS6ZB');</script>
<meta name="description" content="Privacy Policy for LiveScore MCP - Football Live Scores API for AI Agents">
<meta name="robots" content="index, follow">
<link rel="canonical" href="https://livescoremcp.com/privacy">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800;900&display=swap" rel="stylesheet">
<style>
  *{margin:0;padding:0;box-sizing:border-box}
  html{scroll-behavior:smooth}
  body{font-family:'Inter',system-ui,-apple-system,sans-serif;background:#06080f;color:#e0e6ed;min-height:100vh}
  .nav{position:fixed;top:0;left:0;right:0;z-index:100;padding:0 24px;height:56px;display:flex;align-items:center;justify-content:space-between;background:rgba(6,8,15,0.85);backdrop-filter:blur(20px);-webkit-backdrop-filter:blur(20px);border-bottom:1px solid rgba(255,255,255,0.06)}
  .nav-logo{font-weight:800;font-size:1.1rem;color:#fff;text-decoration:none;display:flex;align-items:center;gap:8px}
  .nav-logo svg{flex-shrink:0}
  .container{max-width:720px;margin:0 auto;padding:100px 24px 64px}
  h1{font-size:clamp(1.8rem,4vw,2.4rem);font-weight:900;margin-bottom:8px;background:linear-gradient(135deg,#f1f5f9 0%,#4ade80 50%,#22d3ee 100%);-webkit-background-clip:text;-webkit-text-fill-color:transparent;background-clip:text}
  .updated{color:#64748b;font-size:0.85rem;margin-bottom:40px}
  h2{font-size:1.2rem;font-weight:700;color:#f1f5f9;margin:36px 0 12px;padding-top:20px;border-top:1px solid rgba(255,255,255,0.06)}
  h2:first-of-type{border-top:none;margin-top:0}
  p,li{color:#94a3b8;font-size:0.92rem;line-height:1.8;margin-bottom:12px}
  ul{padding-left:20px;margin-bottom:16px}
  a{color:#4ade80;text-decoration:none;font-weight:500}
  a:hover{text-decoration:underline}
  .back{display:inline-flex;align-items:center;gap:6px;margin-top:40px;color:#4ade80;font-weight:600;font-size:0.9rem}
  .footer{border-top:1px solid rgba(255,255,255,0.06);padding:32px 24px;text-align:center;color:#475569;font-size:0.82rem;margin-top:32px}
  .footer a{color:#64748b;font-weight:500}
  .footer a:hover{color:#4ade80}
</style>
</head>
<body>
<nav class="nav">
  <a href="/" class="nav-logo"><svg width="24" height="21" viewBox="0 0 159.83 139.7" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M121.35,34.77c-1.38-1.63-3.4-2.57-5.52-2.57h-60.88c-3.39,0-6.3,2.42-6.91,5.75l-11.16,61.01c-.38,2.1.19,4.25,1.57,5.9,1.39,1.66,3.41,2.62,5.56,2.62h61.97c3.46,0,6.37-2.47,6.93-5.87l10.07-61.01c.34-2.08-.25-4.21-1.63-5.83ZM68.74,42.53c5.65-.23,11.13.79,16.34,3.03,5.21,2.24,9.73,5.53,13.44,9.77l.95,1.08-17.51-3.83-14.66-10,1.44-.06ZM57.38,82.64l-.26-.13v-.29c-.12-7.38.16-12.31,1.12-19.57l.04-.32.32-.08c7.55-1.82,12.74-2.71,20.57-3.54l.27-.03.16.21c4.78,6.25,7.7,10.63,11.59,17.36l.16.28-.21.25c-4.6,5.68-7.98,9.29-13.42,14.28l-.21.19-.27-.1c-7.62-2.74-12.63-4.89-19.87-8.54ZM46.86,49.79c4.27-3.22,9.27-5.53,14.84-6.52l-.03.36c-2.03.59-3.97,1.37-5.83,2.3l-5.56,12.35-5.64,3.71,2.23-12.19ZM37.83,99.13l2.43-13.28,5.92,4.34,2.32,16.31h-4.5c-3.89,0-6.87-3.56-6.17-7.37ZM99.23,106.5h-23.11l5.03-4.72,13.13,2.52c1.67-1.54,3.21-3.27,4.57-5.2,1.33-1.84,2.46-3.8,3.42-5.83l-2.45-13.71,5.41-13.23.42,1.17c.22.61.38,1.23.56,1.84,4.6,12.1,1.81,26.93-6.98,37.15Z" fill="#fff"/></svg> LiveScore MCP</a>
</nav>
<div class="container">
  <h1>Privacy Policy</h1>
  <p class="updated">Last updated: February 24, 2026</p>

  <h2>Overview</h2>
  <p>LiveScore MCP ("the Service") is a free Model Context Protocol server providing real-time football data. This Privacy Policy explains what data we collect, how we use it, and your rights.</p>

  <h2>Data We Collect</h2>
  <p>When you use the Service, we may collect:</p>
  <ul>
    <li><strong>Request metadata:</strong> IP address, timestamp, user agent, and requested endpoint. This is standard server logging.</li>
    <li><strong>Query parameters:</strong> Language preferences and search terms sent to the API. These are not linked to personal identifiers.</li>
  </ul>
  <p>We do <strong>not</strong> collect personal information, account credentials, cookies, or tracking identifiers. There are no user accounts or sign-ups.</p>

  <h2>How We Use Your Data</h2>
  <p>Collected data is used exclusively to:</p>
  <ul>
    <li>Operate and maintain the Service</li>
    <li>Monitor for abuse and enforce rate limits</li>
    <li>Diagnose technical issues and improve reliability</li>
  </ul>

  <h2>Data Sharing</h2>
  <p>We do not sell, rent, or share your data with third parties. Server logs may be stored on our hosting infrastructure (Hetzner, Germany) and are subject to their data processing policies.</p>

  <h2>Data Retention</h2>
  <p>Server logs are retained for a maximum of 30 days and then automatically deleted. No long-term personal data storage takes place.</p>

  <h2>Third-Party Services</h2>
  <p>The landing page uses Google Fonts for typography. Google may collect basic usage data when fonts are loaded. The football data is sourced from <a href="https://football-mania.com" target="_blank" rel="noopener">football-mania.com</a>. No other third-party analytics or tracking services are used.</p>

  <h2>Rate Limits &amp; Fair Use</h2>
  <p>Rate limits are enforced to ensure fair access. Excessive or automated bulk requests may be throttled or blocked based on IP address. For commercial use or higher rate limits, contact <a href="mailto:gillis.haasnoot@gmail.com">gillis.haasnoot@gmail.com</a>.</p>

  <h2>Your Rights</h2>
  <p>Since we do not collect personal data beyond standard server logs, there is generally no personal data to access, correct, or delete. If you have concerns about data associated with your IP address, contact us and we will address your request.</p>

  <h2>Children</h2>
  <p>The Service is not directed at children under 16. We do not knowingly collect data from minors.</p>

  <h2>Changes to This Policy</h2>
  <p>We may update this policy from time to time. Changes will be reflected on this page with an updated date.</p>

  <h2>Contact</h2>
  <p>For privacy questions, commercial licensing, or any other inquiries:</p>
  <p><a href="mailto:gillis.haasnoot@gmail.com">gillis.haasnoot@gmail.com</a></p>

  <a href="/" class="back">&larr; Back to LiveScore MCP</a>
</div>
<footer class="footer">
  Powered by <a href="https://football-mania.com">football-mania.com</a> &bull; <a href="https://github.com/holoduke/livescore-mcp">Source on GitHub</a>
</footer>
</body>
</html>`

const termsHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="theme-color" content="#06080f">
<title>Terms of Service - LiveScore MCP</title>
<script async src="https://www.googletagmanager.com/gtag/js?id=G-3J7HVJS6ZB"></script>
<script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag('js',new Date());gtag('config','G-3J7HVJS6ZB');</script>
<meta name="description" content="Terms of Service for LiveScore MCP - Free football live scores API for AI agents via the Model Context Protocol.">
<meta name="robots" content="index, follow">
<link rel="canonical" href="https://livescoremcp.com/terms">
<link rel="icon" href="/static/favicon.svg" type="image/svg+xml">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&display=swap" rel="stylesheet">
<style>
  *{margin:0;padding:0;box-sizing:border-box}
  body{font-family:'Inter',system-ui,-apple-system,sans-serif;background:#06080f;color:#e0e6ed;min-height:100vh}
  .nav{position:fixed;top:0;left:0;right:0;z-index:100;padding:0 24px;height:56px;display:flex;align-items:center;background:rgba(6,8,15,0.8);backdrop-filter:blur(20px);border-bottom:1px solid rgba(255,255,255,0.06)}
  .nav-logo{font-weight:800;font-size:1.1rem;color:#fff;text-decoration:none;display:flex;align-items:center;gap:8px}
  .nav-logo svg{flex-shrink:0}
  .container{max-width:720px;margin:0 auto;padding:100px 24px 60px}
  h1{font-size:2rem;font-weight:800;margin-bottom:8px;color:#f1f5f9}
  .updated{color:#64748b;font-size:0.85rem;margin-bottom:40px}
  h2{font-size:1.2rem;font-weight:700;color:#f1f5f9;margin:32px 0 12px}
  p,li{color:#94a3b8;font-size:0.92rem;line-height:1.8;margin-bottom:12px}
  ul{padding-left:24px}
  a{color:#4ade80;text-decoration:none}
  a:hover{text-decoration:underline}
  .footer{border-top:1px solid rgba(255,255,255,0.06);padding:32px 24px;text-align:center}
  .footer a{color:#64748b;font-size:0.85rem;text-decoration:none;margin:0 12px}
  .footer a:hover{color:#4ade80}
</style>
</head>
<body>
<nav class="nav">
  <a href="/" class="nav-logo"><svg width="24" height="21" viewBox="0 0 159.83 139.7" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M121.35,34.77c-1.38-1.63-3.4-2.57-5.52-2.57h-60.88c-3.39,0-6.3,2.42-6.91,5.75l-11.16,61.01c-.38,2.1.19,4.25,1.57,5.9,1.39,1.66,3.41,2.62,5.56,2.62h61.97c3.46,0,6.37-2.47,6.93-5.87l10.07-61.01c.34-2.08-.25-4.21-1.63-5.83ZM68.74,42.53c5.65-.23,11.13.79,16.34,3.03,5.21,2.24,9.73,5.53,13.44,9.77l.95,1.08-17.51-3.83-14.66-10,1.44-.06ZM57.38,82.64l-.26-.13v-.29c-.12-7.38.16-12.31,1.12-19.57l.04-.32.32-.08c7.55-1.82,12.74-2.71,20.57-3.54l.27-.03.16.21c4.78,6.25,7.7,10.63,11.59,17.36l.16.28-.21.25c-4.6,5.68-7.98,9.29-13.42,14.28l-.21.19-.27-.1c-7.62-2.74-12.63-4.89-19.87-8.54ZM46.86,49.79c4.27-3.22,9.27-5.53,14.84-6.52l-.03.36c-2.03.59-3.97,1.37-5.83,2.3l-5.56,12.35-5.64,3.71,2.23-12.19ZM37.83,99.13l2.43-13.28,5.92,4.34,2.32,16.31h-4.5c-3.89,0-6.87-3.56-6.17-7.37ZM99.23,106.5h-23.11l5.03-4.72,13.13,2.52c1.67-1.54,3.21-3.27,4.57-5.2,1.33-1.84,2.46-3.8,3.42-5.83l-2.45-13.71,5.41-13.23.42,1.17c.22.61.38,1.23.56,1.84,4.6,12.1,1.81,26.93-6.98,37.15Z" fill="#fff"/></svg> LiveScore MCP</a>
</nav>
<div class="container">
  <h1>Terms of Service</h1>
  <p class="updated">Last updated: February 26, 2026</p>

  <h2>1. Acceptance of Terms</h2>
  <p>By accessing or using LiveScore MCP ("the Service"), you agree to be bound by these Terms of Service. If you do not agree, do not use the Service.</p>

  <h2>2. Description of Service</h2>
  <p>LiveScore MCP is a free Model Context Protocol (MCP) server that provides real-time football live scores, fixtures, team statistics, and player data. The Service is provided via an SSE (Server-Sent Events) endpoint for use with MCP-compatible AI clients.</p>

  <h2>3. Acceptable Use</h2>
  <p>You agree to use the Service only for lawful purposes. You must not:</p>
  <ul>
    <li>Attempt to circumvent rate limits or abuse the Service</li>
    <li>Scrape data aggressively or in a manner that degrades the experience for others</li>
    <li>Use the Service for any unlawful or unauthorized purpose</li>
    <li>Reverse-engineer, decompile, or attempt to extract the underlying data sources</li>
    <li>Redistribute the data commercially without prior written consent</li>
  </ul>

  <h2>4. Rate Limits</h2>
  <p>The Service enforces rate limits to ensure fair access for all users. Exceeding these limits may result in temporary or permanent suspension of access. For commercial use or higher rate limits, contact <a href="mailto:gillis.haasnoot@gmail.com">gillis.haasnoot@gmail.com</a>.</p>

  <h2>5. Commercial Use</h2>
  <p>The Service is free for personal and non-commercial use. Commercial use requires a separate licensing agreement. Please contact us for commercial inquiries.</p>

  <h2>6. Data Accuracy</h2>
  <p>While we strive to provide accurate and timely football data, we make no warranties regarding the accuracy, completeness, or reliability of the data. The data is sourced from third-party providers and may contain errors or delays.</p>

  <h2>7. Availability</h2>
  <p>The Service is provided on an "as is" and "as available" basis. We do not guarantee uninterrupted or error-free operation. We reserve the right to modify, suspend, or discontinue the Service at any time without notice.</p>

  <h2>8. Limitation of Liability</h2>
  <p>To the fullest extent permitted by law, the Service and its operators shall not be liable for any indirect, incidental, special, consequential, or punitive damages arising from your use of the Service.</p>

  <h2>9. Intellectual Property</h2>
  <p>The LiveScore MCP source code is available on <a href="https://github.com/holoduke/livescore-mcp" target="_blank" rel="noopener noreferrer">GitHub</a>. The football data provided through the Service is owned by the respective data providers and is subject to their terms.</p>

  <h2>10. Termination</h2>
  <p>We reserve the right to terminate or restrict your access to the Service at any time, for any reason, including but not limited to violation of these Terms.</p>

  <h2>11. Changes to Terms</h2>
  <p>We may update these Terms from time to time. Continued use of the Service after changes constitutes acceptance of the new Terms.</p>

  <h2>12. Contact</h2>
  <p>For questions about these Terms, contact: <a href="mailto:gillis.haasnoot@gmail.com">gillis.haasnoot@gmail.com</a></p>
</div>
<footer class="footer">
  <a href="/">Home</a>
  <a href="/privacy">Privacy Policy</a>
  <a href="https://github.com/holoduke/livescore-mcp" target="_blank" rel="noopener noreferrer">GitHub</a>
</footer>
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
