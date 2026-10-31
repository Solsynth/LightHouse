package rewind

import "strings"

// Settings selects the outbound rewind event services. Passport hardcoded
// `https://_grpc.sphere` / `https://_grpc.messager`; Stargate makes the
// targets explicit so a deployment without those services simply omits their
// data maps.
type Settings struct {
	SphereGRPC   string
	MessagerGRPC string
}

// SettingsFromMap reads the module settings (camelCase or snake_case keys).
func SettingsFromMap(raw map[string]any) Settings {
	var settings Settings
	if raw == nil {
		return settings
	}
	settings.SphereGRPC, _ = stringSetting(raw, "sphereGrpc", "sphere_grpc")
	settings.MessagerGRPC, _ = stringSetting(raw, "messagerGrpc", "messager_grpc")
	settings.SphereGRPC = strings.TrimSpace(settings.SphereGRPC)
	settings.MessagerGRPC = strings.TrimSpace(settings.MessagerGRPC)
	return settings
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
