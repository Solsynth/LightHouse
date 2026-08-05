package spell

import (
	"strings"
	"testing"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

func TestRenderEmailTemplate(t *testing.T) {
	body, err := renderEmailTemplate("ContactVerification", "en", map[string]string{
		"nick": "Alice",
		"link": "http://localhost:3000/spells/abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Dear Alice,") {
		t.Errorf("body missing nick substitution: %s", body)
	}
	if !strings.Contains(body, `href="http://localhost:3000/spells/abc"`) {
		t.Errorf("body missing link substitution: %s", body)
	}
}

func TestRenderEmailTemplateEscapesHTML(t *testing.T) {
	body, err := renderEmailTemplate("Welcome", "en", map[string]string{
		"nick":           `<script>alert("x")</script>`,
		"activation_url": "http://localhost:3000/accounts/me/activation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "<script>") {
		t.Errorf("value was not HTML-escaped: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected escaped value in body: %s", body)
	}
}

func TestRenderEmailTemplateLocaleFallback(t *testing.T) {
	// Unknown locale falls back to English (a zh-hans template exists, but a
	// never-seen locale must not fail).
	body, err := renderEmailTemplate("ContactVerification", "ja-jp", map[string]string{
		"nick": "Taro",
		"link": "http://x/spells/y",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Dear Taro,") {
		t.Errorf("fallback body missing substitution: %s", body)
	}
}

func TestRenderEmailTemplateUnknownName(t *testing.T) {
	if _, err := renderEmailTemplate("DoesNotExist", "en", nil); err == nil {
		t.Fatal("expected error for unknown template")
	}
}

func TestGenerateSpellWord(t *testing.T) {
	word := generateSpellWord(128)
	if len(word) != 128 {
		t.Fatalf("length = %d, want 128", len(word))
	}
	for _, r := range word {
		if !strings.ContainsRune(spellWordCharset, r) {
			t.Fatalf("unexpected character %q", r)
		}
	}
	if generateSpellWord(128) == word {
		t.Fatal("two generated words are identical")
	}
}

func TestNormalizeLocale(t *testing.T) {
	cases := map[string]string{
		"":        "en",
		"en-US":   "en",
		"en":      "en",
		"zh-hans": "zh-hans",
		"zh-CN":   "zh-hans",
		"de-DE":   "en",
		"unknown": "en",
	}
	for in, want := range cases {
		if got := normalizeLocale(in); got != want {
			t.Errorf("normalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubjectFallback(t *testing.T) {
	if got := subject("en", "contractMethodVerificationTitle"); got != "Verify your contact method" {
		t.Errorf("en subject = %q", got)
	}
	if got := subject("zh-hans", "welcomeEmailTitle"); got != "欢迎加入 Solar Network" {
		t.Errorf("zh-hans subject = %q", got)
	}
	if got := subject("de", "passwordResetTitle"); got != "Reset your password" {
		t.Errorf("fallback subject = %q", got)
	}
}

func TestSpellLink(t *testing.T) {
	s := &Service{siteURL: "http://localhost:3000/"}
	spell := &model.MagicSpell{Spell: "abcXYZ123"}
	if got := s.spellLink(spell); got != "http://localhost:3000/spells/abcXYZ123" {
		t.Errorf("spellLink = %q", got)
	}
}
