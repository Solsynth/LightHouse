package analytics

import (
	"strings"

	"github.com/mssola/useragent"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

// clientInfo is the classification stored with an event.
type clientInfo struct {
	Class    int
	Name     string
	Platform string
	Mobile   bool
}

// botMarkers match crawlers and link-preview agents. The first match wins, so
// specific markers come before the generic "bot/", "crawler" and "spider".
var botMarkers = []struct{ marker, name string }{
	{"googlebot", "Googlebot"},
	{"google-inspectiontool", "Google Inspection Tool"},
	{"adsbot-google", "AdsBot"},
	{"mediapartners-google", "AdSense"},
	{"bingbot", "Bingbot"},
	{"msnbot", "MSNBot"},
	{"duckduckbot", "DuckDuckBot"},
	{"baiduspider", "Baiduspider"},
	{"yandexbot", "YandexBot"},
	{"yandex.com/bots", "YandexBot"},
	{"sogou", "Sogou Spider"},
	{"exabot", "Exabot"},
	{"ia_archiver", "Alexa"},
	{"semrushbot", "SemrushBot"},
	{"ahrefsbot", "AhrefsBot"},
	{"mj12bot", "Majestic"},
	{"dotbot", "DotBot"},
	{"petalbot", "PetalBot"},
	{"bytespider", "Bytespider"},
	{"amazonbot", "Amazonbot"},
	{"applebot", "Applebot"},
	{"gptbot", "GPTBot"},
	{"chatgpt-user", "ChatGPT"},
	{"oai-searchbot", "OpenAI Search"},
	{"claudebot", "ClaudeBot"},
	{"anthropic-ai", "Anthropic"},
	{"perplexitybot", "PerplexityBot"},
	{"ccbot", "CCBot"},
	{"commoncrawl", "Common Crawl"},
	{"facebookexternalhit", "Facebook Preview"},
	{"facebot", "Facebook"},
	{"twitterbot", "Twitterbot"},
	{"linkedinbot", "LinkedInBot"},
	{"pinterestbot", "Pinterestbot"},
	{"telegrambot", "Telegram Preview"},
	{"discordbot", "Discord Preview"},
	{"slackbot", "Slack Preview"},
	{"whatsapp", "WhatsApp Preview"},
	{"embedly", "Embedly"},
	{"bitlybot", "Bitlybot"},
	{"bot/", "Generic Bot"},
	{"crawler", "Crawler"},
	{"spider", "Spider"},
}

// automationMarkers are HTTP clients and headless browsers: automation rather
// than a human reading a page.
var automationMarkers = []struct{ marker, name string }{
	{"curl/", "curl"},
	{"wget/", "wget"},
	{"python-requests", "python-requests"},
	{"python-urllib", "python-urllib"},
	{"aiohttp", "aiohttp"},
	{"httpx/", "httpx"},
	{"go-http-client", "Go HTTP client"},
	{"okhttp", "OkHttp"},
	{"axios/", "axios"},
	{"node-fetch", "node-fetch"},
	{"java/", "Java HTTP client"},
	{"libwww-perl", "libwww-perl"},
	{"apache-httpclient", "Apache HttpClient"},
	{"headlesschrome", "Headless Chrome"},
	{"phantomjs", "PhantomJS"},
	{"selenium", "Selenium"},
	{"puppeteer", "Puppeteer"},
	{"playwright", "Playwright"},
}

// classifyClient maps a user agent to a class plus display metadata. Empty
// user agents and known HTTP clients count as bots; parsers that cannot name a
// browser leave the class unknown instead of guessing at "human".
func classifyClient(ua string) clientInfo {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return clientInfo{Class: model.AnalyticsClientBot, Name: "empty"}
	}
	lower := strings.ToLower(ua)
	for _, candidate := range botMarkers {
		if strings.Contains(lower, candidate.marker) {
			return clientInfo{Class: model.AnalyticsClientBot, Name: candidate.name}
		}
	}
	for _, candidate := range automationMarkers {
		if strings.Contains(lower, candidate.marker) {
			return clientInfo{Class: model.AnalyticsClientBot, Name: candidate.name}
		}
	}
	parsed := useragent.New(ua)
	if parsed.Bot() {
		name, _ := parsed.Browser()
		if name == "" {
			name = "bot"
		}
		return clientInfo{Class: model.AnalyticsClientBot, Name: name}
	}
	name, _ := parsed.Browser()
	if name == "" {
		return clientInfo{Class: model.AnalyticsClientUnknown, Platform: parsed.Platform()}
	}
	return clientInfo{
		Class:    model.AnalyticsClientHuman,
		Name:     name,
		Platform: parsed.Platform(),
		Mobile:   parsed.Mobile(),
	}
}
