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
	mux.Handle("/static/", http.FileServer(http.FS(staticFiles)))
	mux.HandleFunc("/privacy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, privacyHTML)
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
    <lastmod>2026-02-24</lastmod>
    <changefreq>weekly</changefreq>
    <priority>1.0</priority>
  </url>
  <url>
    <loc>https://livescoremcp.com/privacy</loc>
    <lastmod>2026-02-24</lastmod>
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
<meta property="og:site_name" content="LiveScore MCP">
<meta property="og:locale" content="en_US">

<!-- Twitter -->
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:url" content="https://livescoremcp.com/">
<meta name="twitter:title" content="LiveScore MCP - Football Live Scores for AI Agents">
<meta name="twitter:description" content="Free MCP server with 10 tools for real-time football scores, fixtures, team stats and player data. Works with Claude, Cursor and any MCP client.">
<meta name="twitter:image" content="https://livescoremcp.com/static/og-image.png">

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
  "codeRepository": "https://github.com/holoduke/livescore-mcp",
  "programmingLanguage": "Go",
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
      "name": "What languages does LiveScore MCP support?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "LiveScore MCP supports multiple languages including English (en), Dutch (nl), German (de), French (fr), Spanish (es), Portuguese (pt), Italian (it), and more. Use the language parameter on any tool to get results in your preferred language."
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
  "url": "https://livescoremcp.com"
}
</script>

<!-- Google Fonts: Inter -->
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800;900&display=swap" rel="stylesheet">

<style>
  *{margin:0;padding:0;box-sizing:border-box}
  html{scroll-behavior:smooth}
  body{font-family:'Inter',system-ui,-apple-system,sans-serif;background:#06080f;color:#e0e6ed;min-height:100vh;overflow-x:hidden}

  /* --- Animations --- */
  @keyframes fadeInUp{from{opacity:0;transform:translateY(30px)}to{opacity:1;transform:translateY(0)}}
  @keyframes gradientShift{0%{background-position:0% 50%}50%{background-position:100% 50%}100%{background-position:0% 50%}}
  @keyframes float{0%,100%{transform:translate(0,0)}50%{transform:translate(30px,-20px)}}
  @keyframes float2{0%,100%{transform:translate(0,0)}50%{transform:translate(-20px,30px)}}
  @keyframes pulse{0%,100%{opacity:0.4}50%{opacity:0.8}}
  @keyframes shimmer{0%{background-position:-200% 0}100%{background-position:200% 0}}
  @keyframes livePulse{0%,100%{opacity:1;transform:scale(1)}50%{opacity:0.4;transform:scale(0.8)}}
  @keyframes gradientDivider{0%{background-position:0% 50%}50%{background-position:100% 50%}100%{background-position:0% 50%}}
  .fade-in{opacity:0;animation:fadeInUp 0.7s ease forwards}
  .fade-in-1{animation-delay:0.1s}
  .fade-in-2{animation-delay:0.2s}
  .fade-in-3{animation-delay:0.3s}
  .fade-in-4{animation-delay:0.4s}
  .fade-in-5{animation-delay:0.5s}

  /* --- Sticky Nav --- */
  .nav{position:fixed;top:0;left:0;right:0;z-index:100;padding:0 24px;height:56px;display:flex;align-items:center;justify-content:space-between;background:rgba(6,8,15,0.6);backdrop-filter:blur(20px);-webkit-backdrop-filter:blur(20px);border-bottom:1px solid rgba(255,255,255,0.06);transition:background 0.3s}
  .nav-logo{font-weight:800;font-size:1.1rem;color:#fff;text-decoration:none;display:flex;align-items:center;gap:8px}
  .nav-logo svg{flex-shrink:0}
  .nav-links{display:flex;align-items:center;gap:24px}
  .nav-links a{color:#94a3b8;text-decoration:none;font-size:0.85rem;font-weight:500;transition:color 0.2s}
  .nav-links a:hover{color:#fff}
  .nav-gh{display:inline-flex;align-items:center;gap:6px;background:rgba(255,255,255,0.08);padding:6px 14px;border-radius:8px;color:#e0e6ed;text-decoration:none;font-size:0.8rem;font-weight:600;transition:background 0.2s}
  .nav-gh:hover{background:rgba(255,255,255,0.14)}

  /* --- Hero --- */
  .hero{position:relative;text-align:center;padding:140px 24px 80px;overflow:hidden;min-height:520px;display:flex;flex-direction:column;align-items:center;justify-content:center}
  .hero-bg{position:absolute;inset:0;background:url('/static/hero-bg.png') center center/cover no-repeat;z-index:0;opacity:0.25}
  .hero-bg::after{content:'';position:absolute;inset:0;background:linear-gradient(180deg,rgba(6,8,15,0.5) 0%,rgba(6,8,15,0.2) 40%,rgba(6,8,15,0.7) 100%)}
  .hero-orb{position:absolute;border-radius:50%;filter:blur(80px);z-index:0}
  .hero-orb-1{width:400px;height:400px;background:rgba(74,222,128,0.12);top:-100px;left:-100px;animation:float 8s ease-in-out infinite}
  .hero-orb-2{width:350px;height:350px;background:rgba(34,211,238,0.10);bottom:-80px;right:-80px;animation:float2 10s ease-in-out infinite}
  .hero-orb-3{width:200px;height:200px;background:rgba(168,85,247,0.08);top:50%;left:60%;animation:float 12s ease-in-out infinite,pulse 4s ease-in-out infinite}
  .hero *:not(.hero-bg):not(.hero-orb){position:relative;z-index:1}
  .hero h1{font-size:clamp(2.5rem,6vw,4rem);font-weight:900;letter-spacing:-0.03em;line-height:1.1;margin-bottom:20px;background:linear-gradient(135deg,#4ade80 0%,#22d3ee 50%,#a78bfa 100%);background-size:200% 200%;animation:gradientShift 6s ease infinite;-webkit-background-clip:text;-webkit-text-fill-color:transparent;background-clip:text;filter:drop-shadow(0 2px 8px rgba(74,222,128,0.3))}
  .hero-sub{font-size:clamp(1rem,2.5vw,1.3rem);color:#94a3b8;max-width:560px;margin:0 auto 32px;line-height:1.6;font-weight:400}
  .hero-stats{display:flex;flex-wrap:wrap;justify-content:center;gap:12px;margin-bottom:36px}
  .hero-stat{display:inline-flex;align-items:center;gap:6px;background:rgba(255,255,255,0.05);border:1px solid rgba(255,255,255,0.08);padding:8px 18px;border-radius:100px;font-size:0.85rem;font-weight:600;color:#cbd5e1}
  .hero-stat em{font-style:normal;color:#4ade80}
  .hero-btns{display:flex;flex-wrap:wrap;justify-content:center;gap:12px}
  .btn{display:inline-flex;align-items:center;gap:8px;padding:12px 28px;border-radius:12px;font-size:0.9rem;font-weight:600;text-decoration:none;transition:all 0.2s}
  .btn-primary{background:linear-gradient(135deg,#4ade80,#22d3ee);color:#06080f}
  .btn-primary:hover{transform:translateY(-2px);box-shadow:0 8px 30px rgba(74,222,128,0.3)}
  .btn-secondary{background:rgba(255,255,255,0.06);border:1px solid rgba(255,255,255,0.1);color:#e0e6ed}
  .btn-secondary:hover{background:rgba(255,255,255,0.1);transform:translateY(-2px)}

  /* --- Container --- */
  .container{max-width:960px;margin:0 auto;padding:0 24px}

  /* --- Section --- */
  .section{padding:64px 0}
  .section-alt{background:rgba(255,255,255,0.02);margin:0 -24px;padding:64px 24px;border-top:1px solid rgba(255,255,255,0.04);border-bottom:1px solid rgba(255,255,255,0.04)}
  .section-label{display:inline-block;font-size:0.75rem;font-weight:700;text-transform:uppercase;letter-spacing:0.1em;color:#4ade80;background:rgba(74,222,128,0.1);padding:6px 14px;border-radius:100px;margin-bottom:16px}
  .section-title{font-size:clamp(1.5rem,3vw,2rem);font-weight:800;color:#f1f5f9;margin-bottom:12px;letter-spacing:-0.02em}
  .section-desc{color:#94a3b8;font-size:1rem;line-height:1.7;max-width:600px}

  /* --- How It Works --- */
  .steps{display:grid;grid-template-columns:repeat(3,1fr);gap:32px;margin-top:40px;position:relative}
  .steps::before{content:'';position:absolute;top:40px;left:calc(16.66% + 20px);right:calc(16.66% + 20px);height:2px;background:linear-gradient(90deg,rgba(74,222,128,0.3),rgba(34,211,238,0.3));z-index:0}
  .step{text-align:center;position:relative;z-index:1}
  .step-num{width:56px;height:56px;border-radius:16px;background:linear-gradient(135deg,rgba(74,222,128,0.15),rgba(34,211,238,0.15));border:1px solid rgba(74,222,128,0.2);display:inline-flex;align-items:center;justify-content:center;font-size:1.2rem;font-weight:800;color:#4ade80;margin-bottom:16px}
  .step h3{font-size:1rem;font-weight:700;color:#f1f5f9;margin-bottom:8px}
  .step p{font-size:0.85rem;color:#94a3b8;line-height:1.6}

  /* --- Connect --- */
  .connect-grid{display:grid;gap:16px;margin-top:32px}
  .connect-box{background:rgba(255,255,255,0.03);border:1px solid rgba(255,255,255,0.06);border-radius:16px;padding:24px;backdrop-filter:blur(10px);-webkit-backdrop-filter:blur(10px);transition:border-color 0.3s}
  .connect-box:hover{border-color:rgba(74,222,128,0.2)}
  .connect-box h3{color:#22d3ee;margin-bottom:12px;font-size:0.9rem;font-weight:700;text-transform:uppercase;letter-spacing:0.05em}
  .connect-box p{color:#94a3b8;font-size:0.9rem;margin-bottom:12px;line-height:1.6}
  .code-wrap{position:relative}
  pre{background:rgba(0,0,0,0.4);border:1px solid rgba(255,255,255,0.06);border-radius:12px;padding:20px;overflow-x:auto;font-family:'SF Mono',Consolas,monospace;font-size:0.82rem;line-height:1.7;color:#c9d1d9}
  .code-key{color:#79c0ff}
  .code-str{color:#a5d6ff}
  .code-val{color:#7ee787}
  .copy-btn{position:absolute;top:10px;right:10px;background:rgba(255,255,255,0.08);border:1px solid rgba(255,255,255,0.1);color:#94a3b8;padding:6px 12px;border-radius:8px;font-size:0.72rem;font-weight:600;cursor:pointer;transition:all 0.2s;font-family:'Inter',sans-serif}
  .copy-btn:hover{background:rgba(255,255,255,0.14);color:#fff}
  .endpoint-url{font-family:'SF Mono',Consolas,monospace;background:rgba(74,222,128,0.1);color:#4ade80;padding:3px 10px;border-radius:6px;font-size:0.85rem;font-weight:600}

  /* --- Tools --- */
  .tools-section{position:relative}
  .tools-section::before{content:'';position:absolute;inset:0;background:url('/static/tools-bg.png') center center/cover no-repeat;opacity:0.06;z-index:0;pointer-events:none;border-radius:20px}
  .tools-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:16px;margin-top:32px;position:relative;z-index:1}
  .tool-card{background:rgba(255,255,255,0.03);border:1px solid rgba(255,255,255,0.06);border-left:3px solid;border-image:linear-gradient(180deg,#4ade80,#22d3ee) 1;border-radius:14px;padding:24px;transition:all 0.3s ease;cursor:default}
  .tool-card:hover{transform:translateY(-4px);box-shadow:0 12px 40px rgba(74,222,128,0.08);border-color:rgba(74,222,128,0.15)}
  .tool-icon{font-size:1.5rem;margin-bottom:12px;display:block}
  .tool-card h3{font-family:'SF Mono',Consolas,monospace;color:#4ade80;font-size:0.9rem;margin-bottom:8px;font-weight:700}
  .tool-card p{color:#94a3b8;font-size:0.82rem;line-height:1.6}

  /* --- Languages --- */
  .lang-pills{display:flex;flex-wrap:wrap;gap:10px;margin-top:24px}
  .lang-pill{display:inline-flex;align-items:center;gap:8px;background:rgba(255,255,255,0.04);border:1px solid rgba(255,255,255,0.08);padding:10px 18px;border-radius:12px;font-size:0.85rem;font-weight:600;color:#cbd5e1;transition:all 0.2s}
  .lang-pill:hover{border-color:rgba(74,222,128,0.3);background:rgba(74,222,128,0.05)}
  .lang-flag{font-size:1.1rem}
  .lang-code{color:#4ade80;font-family:'SF Mono',Consolas,monospace;font-size:0.8rem}

  /* --- FAQ --- */
  .faq-list{margin-top:32px}
  .faq-item{background:rgba(255,255,255,0.03);border:1px solid rgba(255,255,255,0.06);border-radius:14px;margin-bottom:12px;overflow:hidden;transition:border-color 0.3s}
  .faq-item:hover{border-color:rgba(255,255,255,0.1)}
  .faq-item summary{padding:20px 24px;cursor:pointer;font-weight:600;font-size:0.95rem;color:#f1f5f9;list-style:none;display:flex;align-items:center;gap:12px;transition:color 0.2s}
  .faq-item summary::-webkit-details-marker{display:none}
  .faq-item summary::before{content:'+';display:inline-flex;align-items:center;justify-content:center;width:28px;height:28px;border-radius:8px;background:rgba(74,222,128,0.1);color:#4ade80;font-weight:700;font-size:1.1rem;flex-shrink:0;transition:all 0.2s}
  .faq-item[open] summary::before{content:'-';background:rgba(74,222,128,0.2)}
  .faq-answer{padding:0 24px 20px 64px;color:#94a3b8;line-height:1.7;font-size:0.9rem}
  .faq-answer a{color:#4ade80;text-decoration:none;font-weight:500}
  .faq-answer a:hover{text-decoration:underline}

  /* --- Powered By --- */
  .powered-card{display:flex;align-items:center;gap:24px;background:rgba(255,255,255,0.03);border:1px solid rgba(255,255,255,0.06);border-radius:16px;padding:32px;margin-top:32px;transition:border-color 0.3s}
  .powered-card:hover{border-color:rgba(74,222,128,0.2)}
  .powered-icon{font-size:2.5rem;flex-shrink:0}
  .powered-card h3{font-size:1rem;font-weight:700;color:#f1f5f9;margin-bottom:6px}
  .powered-card h3 a{color:#4ade80;text-decoration:none;transition:color 0.2s}
  .powered-card h3 a:hover{color:#22d3ee;text-decoration:underline}
  .powered-card p{color:#94a3b8;font-size:0.85rem;line-height:1.6}
  @media(max-width:480px){.powered-card{flex-direction:column;text-align:center}}

  /* --- Footer --- */
  .footer{border-top:1px solid rgba(255,255,255,0.06);padding:48px 24px;margin-top:32px}
  .footer-inner{max-width:960px;margin:0 auto;display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:16px}
  .footer-links{display:flex;gap:24px}
  .footer-links a{color:#64748b;text-decoration:none;font-size:0.85rem;font-weight:500;transition:color 0.2s}
  .footer-links a:hover{color:#4ade80}
  .footer-built{color:#475569;font-size:0.82rem}
  .footer-built a{color:#64748b;text-decoration:none;font-weight:500}
  .footer-built a:hover{color:#4ade80}

  /* --- Shimmer on Hero Stats --- */
  .hero-stat{position:relative;overflow:hidden}
  .hero-stat::after{content:'';position:absolute;top:0;left:-200%;width:200%;height:100%;background:linear-gradient(90deg,transparent,rgba(255,255,255,0.06),transparent);animation:shimmer 4s ease-in-out infinite}

  /* --- Gradient Section Titles --- */
  .section-title{background:linear-gradient(135deg,#f1f5f9 0%,#4ade80 50%,#22d3ee 100%);background-size:200% 200%;animation:gradientShift 6s ease infinite;-webkit-background-clip:text;-webkit-text-fill-color:transparent;background-clip:text}

  /* --- Animated Gradient Dividers --- */
  .gradient-divider{height:2px;border:none;margin:0 auto;max-width:200px;background:linear-gradient(90deg,#4ade80,#22d3ee,#a78bfa,#4ade80);background-size:300% 100%;animation:gradientDivider 4s ease infinite;border-radius:2px;opacity:0.5}

  /* --- Tool Card Ring Glow --- */
  .tool-card:hover{transform:translateY(-4px);box-shadow:0 0 0 2px rgba(74,222,128,0.15),0 12px 40px rgba(74,222,128,0.12);border-color:rgba(74,222,128,0.25)}

  /* --- Live Pulse Dot --- */
  .live-dot{display:inline-block;width:8px;height:8px;background:#4ade80;border-radius:50%;margin-right:6px;animation:livePulse 1.5s ease-in-out infinite;vertical-align:middle;box-shadow:0 0 8px rgba(74,222,128,0.6)}

  /* --- Get the App Section --- */
  .app-badges{display:flex;flex-wrap:wrap;justify-content:center;gap:16px;margin-top:32px}
  .app-badge{display:inline-flex;align-items:center;gap:12px;padding:14px 28px;border-radius:14px;background:rgba(255,255,255,0.05);border:1px solid rgba(255,255,255,0.1);text-decoration:none;color:#e0e6ed;font-weight:600;font-size:0.9rem;transition:all 0.3s ease}
  .app-badge:hover{transform:translateY(-3px);box-shadow:0 0 0 2px rgba(74,222,128,0.2),0 12px 32px rgba(74,222,128,0.15);border-color:rgba(74,222,128,0.3);background:rgba(255,255,255,0.08)}
  .app-badge svg{flex-shrink:0}
  .app-badge-text{display:flex;flex-direction:column;line-height:1.2}
  .app-badge-small{font-size:0.65rem;font-weight:400;color:#94a3b8;text-transform:uppercase;letter-spacing:0.05em}
  .app-badge-store{font-size:1rem;font-weight:700;color:#fff}
  .app-tagline{text-align:center;margin-top:20px;color:#94a3b8;font-size:0.9rem;font-style:italic}

  /* --- Examples Chat Bubbles --- */
  .examples-grid{display:grid;gap:40px;margin-top:32px}
  .example-item{display:grid;grid-template-columns:1fr 1.4fr;gap:16px;align-items:start}
  .chat-q,.chat-a{padding:16px 20px;border-radius:16px;font-size:0.88rem;line-height:1.6;position:relative;margin-top:24px}
  .chat-q{background:linear-gradient(135deg,rgba(74,222,128,0.12),rgba(34,211,238,0.12));border:1px solid rgba(74,222,128,0.2);color:#e0e6ed;border-top-left-radius:4px}
  .chat-q::before{content:'You';position:absolute;top:-20px;left:0;font-size:0.7rem;font-weight:600;color:#4ade80;text-transform:uppercase;letter-spacing:0.05em}
  .chat-a{background:rgba(255,255,255,0.04);border:1px solid rgba(255,255,255,0.08);color:#cbd5e1;border-top-right-radius:4px}
  .chat-a::before{content:'LiveScore MCP';position:absolute;top:-20px;left:0;font-size:0.7rem;font-weight:600;color:#22d3ee;text-transform:uppercase;letter-spacing:0.05em}
  .chat-a strong{color:#4ade80;font-weight:700}
  .chat-a .score{font-family:'SF Mono',Consolas,monospace;color:#22d3ee;font-weight:600}
  @media(max-width:640px){
    .examples-grid{gap:24px}
    .example-item{display:flex;flex-direction:column;gap:4px}
    .chat-q,.chat-a{font-size:0.82rem;padding:12px 16px;max-width:85%;margin-top:20px}
    .chat-q{align-self:flex-end;border-top-left-radius:16px;border-top-right-radius:4px;border-bottom-right-radius:4px;text-align:right}
    .chat-q::before{right:0;left:auto}
    .chat-a{align-self:flex-start;border-top-left-radius:4px;border-top-right-radius:16px;border-bottom-left-radius:4px}
    .chat-a::before{content:'MCP';font-size:0.65rem}
  }

  /* --- Usage Policy --- */
  .policy-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(260px,1fr));gap:16px;margin-top:32px}
  .policy-card{background:rgba(255,255,255,0.03);border:1px solid rgba(255,255,255,0.06);border-radius:14px;padding:24px;transition:border-color 0.3s}
  .policy-card:hover{border-color:rgba(255,255,255,0.12)}
  .policy-icon{font-size:1.5rem;margin-bottom:12px;display:block}
  .policy-card h3{font-size:0.95rem;font-weight:700;color:#f1f5f9;margin-bottom:8px}
  .policy-card p{color:#94a3b8;font-size:0.85rem;line-height:1.7}
  .policy-card a{color:#4ade80;text-decoration:none;font-weight:600}
  .policy-card a:hover{text-decoration:underline}
  .policy-note{margin-top:24px;padding:20px 24px;background:rgba(234,179,8,0.06);border:1px solid rgba(234,179,8,0.15);border-radius:12px;color:#94a3b8;font-size:0.85rem;line-height:1.7}
  .policy-note strong{color:#eab308}

  /* --- Responsive --- */
  @media(max-width:768px){
    .nav-links a:not(.nav-gh){display:none}
    .steps{grid-template-columns:1fr;gap:24px}
    .steps::before{display:none}
    .hero{padding:110px 20px 50px;min-height:auto}
    .hero-stats{gap:8px}
    .hero-stat{padding:6px 14px;font-size:0.8rem}
    .tools-grid{grid-template-columns:1fr}
    .footer-inner{flex-direction:column;text-align:center}
    .footer-links{justify-content:center;flex-wrap:wrap}
    .section{padding:48px 0}
    .section-alt{margin:0 -16px;padding:48px 16px}
    .connect-box{padding:20px}
    pre{font-size:0.72rem;padding:16px}
    .policy-grid{grid-template-columns:1fr}
    .policy-note{padding:16px}
    .footer-built{text-align:center;font-size:0.75rem}
  }
  @media(max-width:480px){
    .hero h1{font-size:2rem}
    .hero-sub{font-size:0.95rem;margin-bottom:24px}
    .hero-btns{flex-direction:column;align-items:center}
    .btn{width:100%;justify-content:center}
    .lang-pills{gap:8px}
    .app-badges{flex-direction:column;align-items:center}
    .app-badge{width:100%;justify-content:center}
    .hero-stats{flex-direction:column;align-items:center;gap:6px}
    .hero-stat{width:auto}
    .container{padding:0 16px}
    .nav{padding:0 16px}
    .footer{padding:32px 16px}
  }
</style>
</head>
<body>

<!-- Nav -->
<nav class="nav">
  <a href="#" class="nav-logo"><svg width="24" height="21" viewBox="0 0 159.83 139.7" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M121.35,34.77c-1.38-1.63-3.4-2.57-5.52-2.57h-60.88c-3.39,0-6.3,2.42-6.91,5.75l-11.16,61.01c-.38,2.1.19,4.25,1.57,5.9,1.39,1.66,3.41,2.62,5.56,2.62h61.97c3.46,0,6.37-2.47,6.93-5.87l10.07-61.01c.34-2.08-.25-4.21-1.63-5.83ZM68.74,42.53c5.65-.23,11.13.79,16.34,3.03,5.21,2.24,9.73,5.53,13.44,9.77l.95,1.08-17.51-3.83-14.66-10,1.44-.06ZM57.38,82.64l-.26-.13v-.29c-.12-7.38.16-12.31,1.12-19.57l.04-.32.32-.08c7.55-1.82,12.74-2.71,20.57-3.54l.27-.03.16.21c4.78,6.25,7.7,10.63,11.59,17.36l.16.28-.21.25c-4.6,5.68-7.98,9.29-13.42,14.28l-.21.19-.27-.1c-7.62-2.74-12.63-4.89-19.87-8.54ZM46.86,49.79c4.27-3.22,9.27-5.53,14.84-6.52l-.03.36c-2.03.59-3.97,1.37-5.83,2.3l-5.56,12.35-5.64,3.71,2.23-12.19ZM37.83,99.13l2.43-13.28,5.92,4.34,2.32,16.31h-4.5c-3.89,0-6.87-3.56-6.17-7.37ZM99.23,106.5h-23.11l5.03-4.72,13.13,2.52c1.67-1.54,3.21-3.27,4.57-5.2,1.33-1.84,2.46-3.8,3.42-5.83l-2.45-13.71,5.41-13.23.42,1.17c.22.61.38,1.23.56,1.84,4.6,12.1,1.81,26.93-6.98,37.15Z" fill="#fff"/></svg> LiveScore MCP</a>
  <div class="nav-links">
    <a href="#how-it-works">How It Works</a>
    <a href="#examples">Examples</a>
    <a href="#connect">Connect</a>
    <a href="#tools">Tools</a>
    <a href="#faq">FAQ</a>
    <a href="#get-app">App</a>
    <a href="https://github.com/holoduke/livescore-mcp" class="nav-gh" target="_blank" rel="noopener">
      <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>
      GitHub
    </a>
  </div>
</nav>

<!-- Hero -->
<header class="hero">
  <div class="hero-bg"></div>
  <div class="hero-orb hero-orb-1"></div>
  <div class="hero-orb hero-orb-2"></div>
  <div class="hero-orb hero-orb-3"></div>
  <h1 class="fade-in fade-in-1">LiveScore MCP</h1>
  <p class="hero-sub fade-in fade-in-2">Real-time football scores, fixtures, team &amp; player data for AI agents via the Model Context Protocol</p>
  <div class="hero-stats fade-in fade-in-3">
    <span class="hero-stat"><em>1000+</em> Leagues</span>
    <span class="hero-stat"><em>10</em> Tools</span>
    <span class="hero-stat"><span class="live-dot"></span><em>SSE</em> Transport</span>
    <span class="hero-stat"><em>8+</em> Languages</span>
  </div>
  <div class="hero-btns fade-in fade-in-4">
    <a href="#connect" class="btn btn-primary">Get Started</a>
    <a href="https://github.com/holoduke/livescore-mcp" class="btn btn-secondary" target="_blank" rel="noopener">
      <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>
      View on GitHub
    </a>
  </div>
</header>

<main class="container">

  <!-- How It Works -->
  <section class="section fade-in fade-in-2" id="how-it-works">
    <span class="section-label">Quick Start</span>
    <h2 class="section-title">How It Works</h2>
    <p class="section-desc">Connect any MCP-compatible AI client to live football data in three simple steps.</p>
    <div class="steps">
      <div class="step">
        <div class="step-num">1</div>
        <h3>Copy the Endpoint</h3>
        <p>Use the SSE endpoint URL to connect your AI client to the LiveScore MCP server</p>
      </div>
      <div class="step">
        <div class="step-num">2</div>
        <h3>Configure Your Client</h3>
        <p>Add the endpoint to Claude Desktop, Cursor, Claude Code, or any MCP-compatible client</p>
      </div>
      <div class="step">
        <div class="step-num">3</div>
        <h3>Query Live Data</h3>
        <p>Ask your AI for live scores, fixtures, team stats, player profiles, and more</p>
      </div>
    </div>
  </section>


  <!-- Examples -->
  <section class="section section-alt fade-in fade-in-3" id="examples">
    <span class="section-label">In Action</span>
    <h2 class="section-title">Example Queries</h2>
    <p class="section-desc">See what you can ask your AI agent when connected to LiveScore MCP.</p>
    <div class="examples-grid">
      <div class="example-item">
        <div class="chat-q">What are the live scores right now?</div>
        <div class="chat-a"><strong>Premier League</strong><br>Arsenal <span class="score">2 - 1</span> Chelsea &bull; 67'<br>Liverpool <span class="score">0 - 0</span> Man City &bull; 34'<br><br><strong>La Liga</strong><br>Real Madrid <span class="score">3 - 0</span> Getafe &bull; FT<br><em style="color:#64748b;font-size:0.8rem">Showing 3 of 12 live matches</em></div>
      </div>
      <div class="example-item">
        <div class="chat-q">Search for Feyenoord and show me their squad</div>
        <div class="chat-a"><strong>Feyenoord Rotterdam</strong> &bull; Eredivisie<br>Stadium: De Kuip (47,000)<br>Coach: Brian Priske<br><br><strong>Key Players:</strong><br>Santiago Gimenez &bull; Forward &bull; #29<br>Igor Paixao &bull; Winger &bull; #11<br>Justin Bijlow &bull; Goalkeeper &bull; #1<br><em style="color:#64748b;font-size:0.8rem">+ 22 more players</em></div>
      </div>
      <div class="example-item">
        <div class="chat-q">Get me all Champions League fixtures</div>
        <div class="chat-a"><strong>UEFA Champions League 2025/26</strong><br><br>Round of 16 &bull; 1st Leg:<br>Barcelona <span class="score">vs</span> PSG &bull; Mar 4<br>Bayern Munich <span class="score">vs</span> Inter Milan &bull; Mar 4<br>Real Madrid <span class="score">vs</span> Man City &bull; Mar 5<br>Liverpool <span class="score">vs</span> Juventus &bull; Mar 5<br><em style="color:#64748b;font-size:0.8rem">+ 4 more fixtures</em></div>
      </div>
    </div>
  </section>


  <!-- Connect -->
  <section class="section fade-in fade-in-3" id="connect">
    <span class="section-label">Setup</span>
    <h2 class="section-title">Connect to LiveScore MCP</h2>
    <p class="section-desc">Get started in seconds. Just point your MCP client to our SSE endpoint.</p>
    <div class="connect-grid">
      <div class="connect-box">
        <h3>SSE Endpoint</h3>
        <p>Connect any MCP client to: <span class="endpoint-url">https://livescoremcp.com/sse</span></p>
      </div>
      <div class="connect-box">
        <h3>Claude Desktop / claude_desktop_config.json</h3>
        <div class="code-wrap">
          <button class="copy-btn" onclick="navigator.clipboard.writeText(this.parentElement.querySelector('pre').textContent).then(function(){event.target.textContent='Copied!'});setTimeout(function(){document.querySelectorAll('.copy-btn').forEach(function(b){b.textContent='Copy'})},2000)">Copy</button>
          <pre>{
  <span class="code-key">"mcpServers"</span>: {
    <span class="code-key">"livescore"</span>: {
      <span class="code-key">"url"</span>: <span class="code-val">"https://livescoremcp.com/sse"</span>
    }
  }
}</pre>
        </div>
      </div>
      <div class="connect-box">
        <h3>Health Check</h3>
        <div class="code-wrap">
          <button class="copy-btn" onclick="navigator.clipboard.writeText(this.parentElement.querySelector('pre').textContent).then(function(){event.target.textContent='Copied!'});setTimeout(function(){document.querySelectorAll('.copy-btn').forEach(function(b){b.textContent='Copy'})},2000)">Copy</button>
          <pre>curl https://livescoremcp.com/health</pre>
        </div>
      </div>
    </div>
  </section>


  <!-- Tools -->
  <section class="section section-alt fade-in fade-in-3 tools-section" id="tools">
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


  <!-- Languages -->
  <section class="section fade-in fade-in-4" id="languages">
    <span class="section-label">Global</span>
    <h2 class="section-title">Multi-Language Support</h2>
    <p class="section-desc">All tools accept a <code style="color:#22d3ee;background:rgba(34,211,238,0.1);padding:2px 8px;border-radius:4px;font-size:0.85rem">language</code> parameter. Get results in your preferred language. All timestamps are in GMT/UTC.</p>
    <div class="lang-pills">
      <span class="lang-pill"><span class="lang-flag">&#127468;&#127463;</span> English <span class="lang-code">en</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127475;&#127473;</span> Dutch <span class="lang-code">nl</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127465;&#127466;</span> German <span class="lang-code">de</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127467;&#127479;</span> French <span class="lang-code">fr</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127466;&#127480;</span> Spanish <span class="lang-code">es</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127477;&#127481;</span> Portuguese <span class="lang-code">pt</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127470;&#127481;</span> Italian <span class="lang-code">it</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127479;&#127482;</span> Russian <span class="lang-code">ru</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127480;&#127462;</span> Arabic <span class="lang-code">ar</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127465;&#127472;</span> Danish <span class="lang-code">da</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127482;&#127462;</span> Ukrainian <span class="lang-code">uk</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127483;&#127475;</span> Vietnamese <span class="lang-code">vi</span></span>
      <span class="lang-pill"><span class="lang-flag">&#127472;&#127479;</span> Korean <span class="lang-code">ko</span></span>
      <span class="lang-pill" style="color:#64748b;border-style:dashed">+ more</span>
    </div>
  </section>


  <!-- Powered By -->
  <section class="section section-alt fade-in fade-in-4" id="powered-by">
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
  <section class="section fade-in fade-in-4" id="get-app" style="text-align:center">
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
  <section class="section section-alt fade-in fade-in-4" id="usage-policy">
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


  <!-- FAQ -->
  <section class="section fade-in fade-in-5" id="faq">
    <span class="section-label">Support</span>
    <h2 class="section-title">Frequently Asked Questions</h2>
    <div class="faq-list">
      <details class="faq-item">
        <summary>What is LiveScore MCP?</summary>
        <div class="faq-answer">LiveScore MCP is a free Model Context Protocol (MCP) server that provides real-time football live scores, fixtures, team statistics, player data, and match details. It connects AI agents like Claude, Cursor, and other MCP-compatible clients to comprehensive football data from 1000+ leagues worldwide.</div>
      </details>
      <details class="faq-item">
        <summary>How do I connect to LiveScore MCP?</summary>
        <div class="faq-answer">Connect any MCP client to the SSE endpoint at <strong>https://livescoremcp.com/sse</strong>. For Claude Desktop, add the URL to your claude_desktop_config.json under mcpServers. For Cursor and other IDE-based clients, configure the SSE URL in your MCP settings.</div>
      </details>
      <details class="faq-item">
        <summary>Is LiveScore MCP free to use?</summary>
        <div class="faq-answer">Yes, LiveScore MCP is free for personal and non-commercial use. The source code is available on <a href="https://github.com/holoduke/livescore-mcp">GitHub</a>. Rate limits apply to ensure fair access for all users. For commercial use or higher rate limits, please contact <a href="mailto:gillis.haasnoot@gmail.com">gillis.haasnoot@gmail.com</a>.</div>
      </details>
      <details class="faq-item">
        <summary>What leagues and competitions are supported?</summary>
        <div class="faq-answer">LiveScore MCP covers 1000+ football leagues and competitions worldwide, including the Premier League, La Liga, Serie A, Bundesliga, Eredivisie, Ligue 1, Champions League, Europa League, World Cup, and many more domestic and international tournaments.</div>
      </details>
      <details class="faq-item">
        <summary>What MCP clients work with LiveScore MCP?</summary>
        <div class="faq-answer">LiveScore MCP uses the SSE (Server-Sent Events) transport and works with any MCP-compatible client, including Claude Desktop, Claude Code, Cursor, Windsurf, Cline, and any other tool that supports the Model Context Protocol over SSE.</div>
      </details>
    </div>
  </section>

</main>

<!-- Footer -->
<footer class="footer">
  <div class="footer-inner">
    <div class="footer-links">
      <a href="https://github.com/holoduke/livescore-mcp">GitHub</a>
      <a href="#connect">Get Started</a>
      <a href="#tools">Tools</a>
      <a href="#faq">FAQ</a>
      <a href="/privacy">Privacy Policy</a>
    </div>
    <div class="footer-built">Powered by <a href="https://football-mania.com">football-mania.com</a> &bull; Built with <a href="https://github.com/mark3labs/mcp-go">mcp-go</a> &bull; <a href="https://github.com/holoduke/livescore-mcp">Source on GitHub</a></div>
  </div>
</footer>
</body>
</html>`

const privacyHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Privacy Policy - LiveScore MCP</title>
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
