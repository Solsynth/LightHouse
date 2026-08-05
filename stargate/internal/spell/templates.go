package spell

import (
	"context"
	"embed"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

//go:embed templates
var templatesFS embed.FS

// emailSubjects mirrors the Passport Resources/Locales keys used by the
// spell + welcome emails (locale -> key -> subject).
var emailSubjects = map[string]map[string]string{
	"en": {
		"contractMethodVerificationTitle": "Verify your contact method",
		"welcomeEmailTitle":               "Welcome to Solar Network",
		"emailAccountDeletionTitle":       "Confirm your account deletion",
		"passwordResetTitle":              "Reset your password",
	},
	"zh-hans": {
		"contractMethodVerificationTitle": "验证您的联系方式",
		"welcomeEmailTitle":               "欢迎加入 Solar Network",
		"emailAccountDeletionTitle":       "确认删除您的账户",
		"passwordResetTitle":              "重置您的密码",
	},
}

// sendTemplatedEmail renders the Liquid email template for the account's
// language and pushes it through Ring (mirroring EmailService.
// SendTemplatedEmailAsync). Failures surface as errors; the 5-minute notify
// dedupe is only set after a successful send, like the C# cache write.
func (s *Service) sendTemplatedEmail(ctx context.Context, account *model.Account, recipient, templateName, subjectKey string, modelData map[string]string) error {
	if s.ring == nil {
		return fmt.Errorf("ring service not configured (email %q not sent)", templateName)
	}
	locale := normalizeLocale(account.Language)
	body, err := renderEmailTemplate(templateName, locale, modelData)
	if err != nil {
		return err
	}
	recipientName := strings.TrimSpace(account.Nick)
	if recipientName == "" {
		recipientName = account.Name
	}
	_, err = s.ring.SendEmail(ctx, &gen.DySendEmailRequest{
		Email: &gen.DyEmailMessage{
			ToName:    recipientName,
			ToAddress: recipient,
			Subject:   subject(locale, subjectKey),
			Body:      body,
		},
	})
	return err
}

var liquidVar = regexp.MustCompile(`{{\s*([A-Za-z_][A-Za-z0-9_]*)\s*}}`)

// renderEmailTemplate renders a Liquid template with simple {{ var }}
// substitution (the ported templates contain no tags/filters), HTML-escaping
// every value like DotLiquid's default escaping.
func renderEmailTemplate(templateName, locale string, data map[string]string) (string, error) {
	raw, err := templatesFS.ReadFile("templates/" + locale + "/" + templateName + ".liquid")
	if err != nil {
		// Fall back to English when the locale is missing.
		raw, err = templatesFS.ReadFile("templates/en/" + templateName + ".liquid")
		if err != nil {
			return "", fmt.Errorf("email template %q not found", templateName)
		}
	}
	return liquidVar.ReplaceAllStringFunc(string(raw), func(match string) string {
		key := liquidVar.FindStringSubmatch(match)[1]
		value, ok := data[key]
		if !ok {
			return ""
		}
		return html.EscapeString(value)
	}), nil
}

// normalizeLocale maps an account language ("en-US", "zh-hans", ...) to the
// template/locale directory name; unknown locales fall back to "en".
func normalizeLocale(language string) string {
	switch {
	case language == "":
		return "en"
	case strings.HasPrefix(strings.ToLower(language), "zh"):
		return "zh-hans"
	case strings.HasPrefix(strings.ToLower(language), "en"):
		return "en"
	default:
		return "en"
	}
}

func subject(locale, key string) string {
	if subjects, ok := emailSubjects[locale]; ok {
		if subject, ok := subjects[key]; ok {
			return subject
		}
	}
	return emailSubjects["en"][key]
}

// escapePathSegment mirrors the C# Uri.EscapeDataString for the spell word in
// the emailed link (the word charset is URL-safe anyway).
func escapePathSegment(word string) string {
	return url.PathEscape(word)
}

// hashPassword bcrypt-hashes a new password for a reset factor.
func hashPassword(password string) (string, error) {
	return auth.HashPassword(password)
}
