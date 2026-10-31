package passkit

import (
	"fmt"
	"strconv"
	"strings"
)

// Settings mirrors Passport's AppleWalletOptions. Keys are camelCase in
// [modules.settings."app.solian.id.passkit"], with snake_case accepted as an
// alias.
type Settings struct {
	PassTypeIdentifier         string
	TeamIdentifier             string
	OrganizationName           string
	Description                string
	LogoText                   string
	ForegroundColor            string
	BackgroundColor            string
	LabelColor                 string
	TermsText                  string
	BarcodeAltText             string
	WebServiceURL              string
	SigningCertificatePath     string
	SigningCertificatePassword string
	AppleWwdrCertificatePath   string
	IconPath                   *string
	Icon2XPath                 *string
	Icon3XPath                 *string
	LogoPath                   *string
	Logo2XPath                 *string
	Logo3XPath                 *string
	ThumbnailPath              *string
	Thumbnail2XPath            *string
	Thumbnail3XPath            *string
	SiteURL                    *string
	AssociatedStoreIdentifier  *int
}

// DefaultSettings mirrors AppleWalletOptions' property defaults.
func DefaultSettings() Settings {
	return Settings{
		ForegroundColor: "rgb(255, 255, 255)",
		BackgroundColor: "rgb(91, 93, 148)",
		LabelColor:      "rgb(224, 242, 254)",
		TermsText:       "Solsynth © All rights reserved.",
		BarcodeAltText:  "MEMBER CODE",
	}
}

// SettingsFromMap decodes the module settings subtree.
func SettingsFromMap(raw map[string]any) Settings {
	settings := DefaultSettings()
	if raw == nil {
		return settings
	}
	setString := func(target *string, keys ...string) {
		if value, ok := stringSetting(raw, keys...); ok {
			*target = value
		}
	}
	setOptional := func(target **string, keys ...string) {
		if value, ok := stringSetting(raw, keys...); ok {
			v := value
			*target = &v
		}
	}

	setString(&settings.PassTypeIdentifier, "passTypeIdentifier", "pass_type_identifier")
	setString(&settings.TeamIdentifier, "teamIdentifier", "team_identifier")
	setString(&settings.OrganizationName, "organizationName", "organization_name")
	setString(&settings.Description, "description")
	setString(&settings.LogoText, "logoText", "logo_text")
	setString(&settings.ForegroundColor, "foregroundColor", "foreground_color")
	setString(&settings.BackgroundColor, "backgroundColor", "background_color")
	setString(&settings.LabelColor, "labelColor", "label_color")
	setString(&settings.TermsText, "termsText", "terms_text")
	setString(&settings.BarcodeAltText, "barcodeAltText", "barcode_alt_text")
	setString(&settings.WebServiceURL, "webServiceUrl", "webServiceURL", "web_service_url")
	setString(&settings.SigningCertificatePath, "signingCertificatePath", "signing_certificate_path")
	setString(&settings.SigningCertificatePassword, "signingCertificatePassword", "signing_certificate_password")
	setString(&settings.AppleWwdrCertificatePath, "appleWwdrCertificatePath", "apple_wwdr_certificate_path")

	setOptional(&settings.IconPath, "iconPath", "icon_path")
	setOptional(&settings.Icon2XPath, "icon2XPath", "icon_2x_path")
	setOptional(&settings.Icon3XPath, "icon3XPath", "icon_3x_path")
	setOptional(&settings.LogoPath, "logoPath", "logo_path")
	setOptional(&settings.Logo2XPath, "logo2XPath", "logo_2x_path")
	setOptional(&settings.Logo3XPath, "logo3XPath", "logo_3x_path")
	setOptional(&settings.ThumbnailPath, "thumbnailPath", "thumbnail_path")
	setOptional(&settings.Thumbnail2XPath, "thumbnail2XPath", "thumbnail_2x_path")
	setOptional(&settings.Thumbnail3XPath, "thumbnail3XPath", "thumbnail_3x_path")
	setOptional(&settings.SiteURL, "siteUrl", "siteURL", "site_url")

	if value, ok := intSetting(raw, "associatedStoreIdentifier", "associated_store_identifier"); ok {
		settings.AssociatedStoreIdentifier = &value
	}
	return settings
}

// MissingRequired lists the settings generation cannot run without.
func (s Settings) MissingRequired() []string {
	var missing []string
	check := func(value, key string) {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}
	check(s.PassTypeIdentifier, "passTypeIdentifier")
	check(s.TeamIdentifier, "teamIdentifier")
	check(s.OrganizationName, "organizationName")
	check(s.Description, "description")
	check(s.WebServiceURL, "webServiceUrl")
	check(s.SigningCertificatePath, "signingCertificatePath")
	check(s.AppleWwdrCertificatePath, "appleWwdrCertificatePath")
	return missing
}

// ConfigError reports an unusable Apple Wallet configuration.
type ConfigError struct {
	Missing []string
	Detail  string
}

func (e *ConfigError) Error() string {
	if len(e.Missing) > 0 {
		return fmt.Sprintf("Apple Wallet is not configured: missing %s.", strings.Join(e.Missing, ", "))
	}
	if e.Detail != "" {
		return "Apple Wallet is not configured: " + e.Detail
	}
	return "Apple Wallet is not configured."
}

func stringSetting(raw map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if typed, ok := value.(string); ok {
			return typed, true
		}
	}
	return "", false
}

func intSetting(raw map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			return typed, true
		case int64:
			return int(typed), true
		case float64:
			return int(typed), true
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}
