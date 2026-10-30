package domaintrust

import (
	"net"
	"strconv"
	"strings"
)

// Settings mirrors DomainBlockSettings. Zero values do not mean "off": the
// resolver starts from DefaultSettings and only overrides the keys present in
// the module settings, so an omitted option keeps the C# default.
type Settings struct {
	BlockHTTPByDefault            bool
	BlockIPAddressesByDefault     bool
	BlockPrivateNetworksByDefault bool
	// AdditionalBlockedPorts replaces DefaultBlockedPorts when non-nil, like
	// DomainBlockSettings.AdditionalBlockedPorts.
	AdditionalBlockedPorts []int
}

// DefaultSettings is DomainBlockSettings with the C# appsettings values.
func DefaultSettings() Settings {
	return Settings{
		BlockHTTPByDefault:            true,
		BlockIPAddressesByDefault:     true,
		BlockPrivateNetworksByDefault: true,
	}
}

// DefaultBlockedPorts is DomainBlockSettings.DefaultBlockedPorts.
var DefaultBlockedPorts = []int{21, 22, 23, 25, 53, 110, 143, 993, 995, 3306, 5432, 6379, 27017}

// SettingsFromMap reads the module's settings map. Keys are the C# section's
// names (camelCase); snake_case is accepted too so a config copied from the
// JSON appsettings keeps working.
func SettingsFromMap(raw map[string]any) Settings {
	settings := DefaultSettings()
	if raw == nil {
		return settings
	}
	if value, ok := boolSetting(raw, "blockHttpByDefault", "block_http_by_default"); ok {
		settings.BlockHTTPByDefault = value
	}
	if value, ok := boolSetting(raw, "blockIpAddressesByDefault", "block_ip_addresses_by_default"); ok {
		settings.BlockIPAddressesByDefault = value
	}
	if value, ok := boolSetting(raw, "blockPrivateNetworksByDefault", "block_private_networks_by_default"); ok {
		settings.BlockPrivateNetworksByDefault = value
	}
	if value, ok := portSetting(raw, "additionalBlockedPorts", "additional_blocked_ports"); ok {
		settings.AdditionalBlockedPorts = value
	}
	return settings
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
			parsed, err := strconv.ParseBool(typed)
			if err == nil {
				return parsed, true
			}
		}
	}
	return false, false
}

func portSetting(raw map[string]any, keys ...string) ([]int, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		list, ok := value.([]any)
		if !ok {
			if direct, ok := value.([]int); ok {
				return append([]int(nil), direct...), true
			}
			continue
		}
		ports := make([]int, 0, len(list))
		for _, item := range list {
			switch typed := item.(type) {
			case int:
				ports = append(ports, typed)
			case int64:
				ports = append(ports, int(typed))
			case float64:
				ports = append(ports, int(typed))
			case string:
				if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
					ports = append(ports, parsed)
				}
			}
		}
		return ports, true
	}
	return nil, false
}

// blockedPorts returns the effective port blocklist.
func (s Settings) blockedPorts() []int {
	if s.AdditionalBlockedPorts != nil {
		return s.AdditionalBlockedPorts
	}
	return DefaultBlockedPorts
}

func containsPort(ports []int, port int) bool {
	for _, candidate := range ports {
		if candidate == port {
			return true
		}
	}
	return false
}

// IsIPAddress mirrors DomainBlockSettings.IsIpAddress.
func IsIPAddress(host string) bool {
	return net.ParseIP(strings.TrimSpace(host)) != nil
}

// IsPrivateNetwork mirrors DomainBlockSettings.IsPrivateNetwork. The C# checks
// IPv4 RFC1918 + loopback, IPv6 link-local/site-local, and fc00:: (not the
// whole fc00::/7 ULA range); reproducing that keeps the block decisions
// identical to Passport.
func IsPrivateNetwork(host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 10 ||
			(v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31) ||
			(v4[0] == 192 && v4[1] == 168) ||
			v4[0] == 127
	}
	if ip.IsLinkLocalUnicast() {
		return true
	}
	// Site-local (fec0::/10) is deprecated but still what .NET's
	// IPAddress.IsIPv6SiteLocal reports.
	if len(ip) >= 2 && ip[0] == 0xfe && ip[1]&0xc0 == 0xc0 {
		return true
	}
	return len(ip) >= 2 && ip[0] == 0xfc && ip[1] == 0x00
}
