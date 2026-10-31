package presenceartworks

import (
	"net/url"
	"strconv"
	"strings"
)

// Default settings mirror PresenceArtworkConfiguration.
const (
	DefaultKeyPrefix        = "presence-artwork"
	DefaultMaxFileSizeBytes = 1024 * 1024
	DefaultRetentionDays    = 30
	DefaultRegion           = "us-east-1"
)

// Settings is PresenceArtworkConfiguration decoded from the module settings.
type Settings struct {
	KeyPrefix        string
	MaxFileSizeBytes int64
	RetentionDays    int
	S3               S3Settings
}

// S3Settings is PresenceArtworkS3Configuration. InsecureSkipVerify is an
// extension for the fleet's self-signed MinIO endpoints; the C# client relied
// on the process-wide HTTP handler instead.
type S3Settings struct {
	Endpoint           string
	ServiceURL         string
	Region             string
	AccessKey          string
	SecretKey          string
	Bucket             string
	ForcePathStyle     bool
	EnableSSL          bool
	InsecureSkipVerify bool
}

// DefaultSettings is PresenceArtworkConfiguration with the C# defaults.
func DefaultSettings() Settings {
	return Settings{
		KeyPrefix:        DefaultKeyPrefix,
		MaxFileSizeBytes: DefaultMaxFileSizeBytes,
		RetentionDays:    DefaultRetentionDays,
		S3: S3Settings{
			Region:         DefaultRegion,
			ForcePathStyle: true,
			EnableSSL:      true,
		},
	}
}

// SettingsFromMap reads the module's settings map. Keys are the C# section's
// names (camelCase); snake_case is accepted too.
func SettingsFromMap(raw map[string]any) Settings {
	settings := DefaultSettings()
	if raw == nil {
		return settings
	}
	if value, ok := stringSetting(raw, "keyPrefix", "key_prefix"); ok {
		settings.KeyPrefix = value
	}
	if value, ok := intSetting(raw, "maxFileSizeBytes", "max_file_size_bytes"); ok && value > 0 {
		settings.MaxFileSizeBytes = value
	}
	if value, ok := intSetting(raw, "retentionDays", "retention_days"); ok && value > 0 {
		settings.RetentionDays = int(value)
	}
	s3raw := nestedMap(raw, "s3")
	settings.S3.Endpoint, _ = stringSetting(s3raw, "endpoint")
	settings.S3.ServiceURL, _ = stringSetting(s3raw, "serviceUrl", "service_url")
	settings.S3.Region, _ = stringSetting(s3raw, "region")
	if strings.TrimSpace(settings.S3.Region) == "" {
		settings.S3.Region = DefaultRegion
	}
	settings.S3.AccessKey, _ = stringSetting(s3raw, "accessKey", "access_key")
	settings.S3.SecretKey, _ = stringSetting(s3raw, "secretKey", "secret_key")
	settings.S3.Bucket, _ = stringSetting(s3raw, "bucket")
	if value, ok := boolSetting(s3raw, "forcePathStyle", "force_path_style"); ok {
		settings.S3.ForcePathStyle = value
	}
	if value, ok := boolSetting(s3raw, "enableSsl", "enable_ssl"); ok {
		settings.S3.EnableSSL = value
	}
	if value, ok := boolSetting(s3raw, "insecureSkipVerify", "insecure_skip_verify"); ok {
		settings.S3.InsecureSkipVerify = value
	}
	return settings
}

// Configured reports whether the S3 section carries enough information to
// build a client. An unconfigured deployment keeps the routes mounted and
// answers 503.
func (s Settings) Configured() bool {
	return strings.TrimSpace(s.S3.Bucket) != "" &&
		strings.TrimSpace(s.S3.AccessKey) != "" &&
		strings.TrimSpace(s.S3.SecretKey) != "" &&
		(strings.TrimSpace(s.S3.Endpoint) != "" || strings.TrimSpace(s.S3.ServiceURL) != "")
}

// endpoint resolves the bucket endpoint and TLS flag the way the C# service
// did: Endpoint wins; otherwise ServiceUrl supplies both host and scheme.
func (s Settings) endpoint() (host string, secure bool, err error) {
	if endpoint := strings.TrimSpace(s.S3.Endpoint); endpoint != "" {
		return endpoint, s.S3.EnableSSL, nil
	}
	parsed, err := url.Parse(strings.TrimSpace(s.S3.ServiceURL))
	if err != nil {
		return "", false, err
	}
	if parsed.Host == "" {
		return "", false, errInvalidEndpoint
	}
	return parsed.Host, strings.EqualFold(parsed.Scheme, "https"), nil
}

func stringSetting(raw map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if typed, ok := value.(string); ok {
				return typed, true
			}
		}
	}
	return "", false
}

func intSetting(raw map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			return int64(typed), true
		case int32:
			return int64(typed), true
		case int64:
			return typed, true
		case float64:
			return int64(typed), true
		case string:
			if parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func boolSetting(raw map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed, true
		case string:
			if parsed, err := strconv.ParseBool(strings.TrimSpace(typed)); err == nil {
				return parsed, true
			}
		}
	}
	return false, false
}

func nestedMap(raw map[string]any, key string) map[string]any {
	value, ok := raw[key]
	if !ok {
		return nil
	}
	nested, _ := value.(map[string]any)
	return nested
}
