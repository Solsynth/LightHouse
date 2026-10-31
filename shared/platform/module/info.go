package module

// Info is one module's public description, used by GET /api/modules. The
// shape is shared so every service that mounts the module registry reports
// the same fields.
type Info struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Enabled      bool     `json:"enabled"`
	Required     bool     `json:"required"`
	Requires     []string `json:"requires,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Infos describes the resolved module set in registration order.
func Infos[C any](factories []Factory[C], enabled map[string]bool) []Info {
	out := make([]Info, 0, len(factories))
	for _, factory := range factories {
		out = append(out, Info{
			ID:           factory.ID,
			Kind:         string(factory.Kind),
			Enabled:      enabled[factory.ID],
			Required:     factory.Required,
			Requires:     factory.Requires,
			Capabilities: factory.Capabilities,
		})
	}
	return out
}
