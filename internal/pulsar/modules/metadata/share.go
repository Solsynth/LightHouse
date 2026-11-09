package metadata

import (
	"fmt"
	"html"
	"net/http"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/metadata"
)

// renderHTMLShare renders an OpenGraph / Twitter Cards HTML preview page.
func renderHTMLShare(c *gin.Context, m *metadata.UnifiedMedia) {
	titleEsc := html.EscapeString(m.Title)
	subtitleEsc := html.EscapeString(m.Subtitle)
	descEsc := html.EscapeString(m.Description)
	if descEsc == "" {
		descEsc = fmt.Sprintf("%s by %s on Pulsar", m.Title, m.Subtitle)
	}
	artEsc := html.EscapeString(m.ArtworkURL)
	shareURLEsc := html.EscapeString(m.ShareURL)

	ogType := "website"
	switch m.Domain {
	case "music":
		ogType = "music.song"
	case "video", "anime":
		ogType = "video.movie"
	}

	linksHTML := ""
	for _, l := range m.ExternalLinks {
		linksHTML += fmt.Sprintf(`<a class="ext-link" href="%s" target="_blank" rel="noopener">%s</a>`,
			html.EscapeString(l.URL), html.EscapeString(l.Label))
	}

	genresHTML := ""
	for _, g := range m.Genres {
		genresHTML += fmt.Sprintf(`<span class="tag">%s</span>`, html.EscapeString(g))
	}

	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s · Pulsar</title>
  <meta name="description" content="%s">

  <!-- OpenGraph -->
  <meta property="og:site_name" content="Pulsar · Solar Network">
  <meta property="og:title" content="%s%s">
  <meta property="og:description" content="%s">
  <meta property="og:image" content="%s">
  <meta property="og:url" content="%s">
  <meta property="og:type" content="%s">

  <!-- Twitter Card -->
  <meta name="twitter:card" content="summary_large_image">
  <meta name="twitter:title" content="%s%s">
  <meta name="twitter:description" content="%s">
  <meta name="twitter:image" content="%s">

  <style>
    :root {
      --bg: #0b0f19;
      --card-bg: rgba(22, 27, 46, 0.85);
      --text: #e2e8f0;
      --text-muted: #94a3b8;
      --accent: #38bdf8;
      --accent-grad: linear-gradient(135deg, #38bdf8 0%%, #818cf8 100%%);
      --border: rgba(255, 255, 255, 0.08);
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      background-color: var(--bg);
      color: var(--text);
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      min-height: 100vh;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      padding: 24px;
    }
    .card {
      background: var(--card-bg);
      backdrop-filter: blur(20px);
      border: 1px solid var(--border);
      border-radius: 20px;
      max-width: 580px;
      width: 100%%;
      overflow: hidden;
      box-shadow: 0 20px 40px rgba(0,0,0,0.4);
    }
    .banner {
      width: 100%%;
      height: 180px;
      object-fit: cover;
      background: var(--accent-grad);
      display: block;
    }
    .content {
      padding: 28px;
      position: relative;
    }
    .art-wrap {
      display: flex;
      gap: 20px;
      margin-top: -60px;
      margin-bottom: 20px;
      align-items: flex-end;
    }
    .cover-art {
      width: 120px;
      height: 120px;
      border-radius: 14px;
      object-fit: cover;
      box-shadow: 0 10px 25px rgba(0,0,0,0.5);
      border: 2px solid rgba(255, 255, 255, 0.15);
      background: #1e293b;
    }
    .domain-badge {
      display: inline-block;
      text-transform: uppercase;
      font-size: 11px;
      font-weight: 700;
      letter-spacing: 0.08em;
      background: var(--accent-grad);
      color: #0b0f19;
      padding: 4px 10px;
      border-radius: 20px;
      margin-bottom: 8px;
    }
    h1 {
      font-size: 24px;
      font-weight: 700;
      line-height: 1.25;
      margin-bottom: 6px;
    }
    .subtitle {
      font-size: 16px;
      color: var(--accent);
      margin-bottom: 4px;
    }
    .caption {
      font-size: 14px;
      color: var(--text-muted);
      margin-bottom: 16px;
    }
    .desc {
      font-size: 14px;
      line-height: 1.6;
      color: var(--text-muted);
      margin-bottom: 20px;
      max-height: 120px;
      overflow-y: auto;
    }
    .tags {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      margin-bottom: 20px;
    }
    .tag {
      font-size: 12px;
      background: rgba(255, 255, 255, 0.06);
      padding: 4px 10px;
      border-radius: 6px;
      color: var(--text-muted);
    }
    .metrics {
      display: flex;
      gap: 24px;
      padding: 14px 18px;
      background: rgba(0, 0, 0, 0.2);
      border-radius: 12px;
      margin-bottom: 24px;
    }
    .metric-val {
      font-size: 18px;
      font-weight: 700;
      color: var(--text);
    }
    .metric-label {
      font-size: 12px;
      color: var(--text-muted);
      text-transform: uppercase;
    }
    .links {
      display: flex;
      flex-wrap: wrap;
      gap: 10px;
    }
    .ext-link {
      display: inline-flex;
      align-items: center;
      padding: 8px 16px;
      border-radius: 8px;
      background: rgba(56, 189, 248, 0.1);
      color: var(--accent);
      text-decoration: none;
      font-size: 13px;
      font-weight: 600;
      border: 1px solid rgba(56, 189, 248, 0.2);
      transition: all 0.15s ease;
    }
    .ext-link:hover {
      background: var(--accent);
      color: #0b0f19;
    }
  </style>
</head>
<body>
  <div class="card">
    %s
    <div class="content">
      <div class="art-wrap">
        %s
        <div>
          <span class="domain-badge">%s</span>
          <h1>%s</h1>
          %s
          %s
        </div>
      </div>

      %s
      %s

      <div class="metrics">
        <div>
          <div class="metric-val">%d</div>
          <div class="metric-label">Scrobbles</div>
        </div>
        %s
      </div>

      <div class="links">
        %s
      </div>
    </div>
  </div>
</body>
</html>`,
		titleEsc,
		descEsc,
		titleEsc, formatSub(m.Subtitle),
		descEsc,
		artEsc,
		shareURLEsc,
		ogType,
		titleEsc, formatSub(m.Subtitle),
		descEsc,
		artEsc,
		renderBanner(m.BannerURL),
		renderCover(m.ArtworkURL),
		html.EscapeString(m.Domain),
		titleEsc,
		renderSub(subtitleEsc),
		renderCap(html.EscapeString(m.Caption)),
		renderDesc(descEsc),
		renderTags(genresHTML),
		m.ScrobbleCount,
		renderActive(m.ActiveListenerCount),
		linksHTML,
	)

	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(htmlContent))
}

func formatSub(s string) string {
	if s == "" {
		return ""
	}
	return " · " + s
}

func renderBanner(url string) string {
	if url == "" {
		return `<div class="banner"></div>`
	}
	return fmt.Sprintf(`<img class="banner" src="%s" alt="Banner" />`, html.EscapeString(url))
}

func renderCover(url string) string {
	if url == "" {
		return `<div class="cover-art"></div>`
	}
	return fmt.Sprintf(`<img class="cover-art" src="%s" alt="Artwork" />`, html.EscapeString(url))
}

func renderSub(s string) string {
	if s == "" {
		return ""
	}
	return fmt.Sprintf(`<div class="subtitle">%s</div>`, s)
}

func renderCap(s string) string {
	if s == "" {
		return ""
	}
	return fmt.Sprintf(`<div class="caption">%s</div>`, s)
}

func renderDesc(s string) string {
	if s == "" {
		return ""
	}
	return fmt.Sprintf(`<div class="desc">%s</div>`, s)
}

func renderTags(tagsHTML string) string {
	if tagsHTML == "" {
		return ""
	}
	return fmt.Sprintf(`<div class="tags">%s</div>`, tagsHTML)
}

func renderActive(count int64) string {
	if count <= 0 {
		return ""
	}
	return fmt.Sprintf(`<div><div class="metric-val">%d</div><div class="metric-label">Active Now</div></div>`, count)
}
