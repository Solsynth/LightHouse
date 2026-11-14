package analytics

import (
	"testing"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

func TestClassifyClient(t *testing.T) {
	const (
		chrome = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
		iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1"
		google = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
		gpt    = "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; GPTBot/1.2; +https://openai.com/gptbot"
	)
	tests := []struct {
		name    string
		ua      string
		class   int
		label   string
		mobile  bool
		hasPlat bool
	}{
		{name: "chrome is a human", ua: chrome, class: model.AnalyticsClientHuman, label: "Chrome", hasPlat: true},
		{name: "iphone is a mobile human", ua: iphone, class: model.AnalyticsClientHuman, label: "Safari", mobile: true, hasPlat: true},
		{name: "googlebot is a bot", ua: google, class: model.AnalyticsClientBot, label: "Googlebot"},
		{name: "gptbot is a bot", ua: gpt, class: model.AnalyticsClientBot, label: "GPTBot"},
		{name: "curl is a bot", ua: "curl/8.5.0", class: model.AnalyticsClientBot, label: "curl"},
		{name: "empty ua is a bot", ua: "", class: model.AnalyticsClientBot, label: "empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := classifyClient(test.ua)
			if info.Class != test.class {
				t.Fatalf("class = %d, want %d", info.Class, test.class)
			}
			if info.Name != test.label {
				t.Fatalf("name = %q, want %q", info.Name, test.label)
			}
			if info.Mobile != test.mobile {
				t.Fatalf("mobile = %v, want %v", info.Mobile, test.mobile)
			}
			if test.hasPlat && info.Platform == "" {
				t.Fatalf("platform is empty for %q", test.ua)
			}
		})
	}
}

// A hostile user agent must not escape the stored column limits.
func TestClassifyClientClipsUserAgent(t *testing.T) {
	long := "Mozilla/5.0 " + string(make([]byte, 5000))
	info := classifyClient(long)
	if info.Class != model.AnalyticsClientUnknown && info.Class != model.AnalyticsClientBot {
		t.Fatalf("class = %d, want unknown or bot", info.Class)
	}
}
