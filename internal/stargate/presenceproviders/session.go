package presenceproviders

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/presencecmd"
	"src.solsynth.dev/sosys/stargate/model"
)

// sessionSpec is the subset of StartSessionRequest the two scanners populate.
type sessionSpec struct {
	Type           string
	Tags           []model.PresenceTag
	Provider       string
	ReferenceID    *string
	Title          *string
	Subtitle       *string
	Caption        *string
	LargeImage     *string
	SmallImage     *string
	TitleURL       *string
	SubtitleURL    *string
	QueryableTerms []string
	Meta           map[string]any
	CatalogKey     string
	CatalogName    *string
}

// startSession sends a start command to Pulsar. The provider scanners keep
// their connection and stage logic in Stargate; the activity row itself is
// owned by Pulsar.
func (s *Service) startSession(ctx context.Context, accountID uuid.UUID, manualID string, spec sessionSpec) error {
	if strings.TrimSpace(manualID) == "" || strings.TrimSpace(spec.Type) == "" {
		return errors.New("presenceproviders: manual id and type are required")
	}
	if s == nil || s.nats == nil || s.nats.Conn == nil {
		return errors.New("presenceproviders: event bus is not configured")
	}
	lease := LeaseMinutes
	return s.nats.PublishJetStream(ctx, presencecmd.Subject, presencecmd.Stream, presencecmd.Command{
		AccountID: accountID.String(),
		ManualID:  manualID,
		Action:    presencecmd.ActionStart,
		Spec: &presencecmd.SessionSpec{
			Type:           spec.Type,
			Provider:       spec.Provider,
			ReferenceID:    spec.ReferenceID,
			Title:          spec.Title,
			Subtitle:       spec.Subtitle,
			Caption:        spec.Caption,
			LargeImage:     spec.LargeImage,
			SmallImage:     spec.SmallImage,
			TitleURL:       spec.TitleURL,
			SubtitleURL:    spec.SubtitleURL,
			QueryableTerms: spec.QueryableTerms,
			Meta:           spec.Meta,
			CatalogKey:     spec.CatalogKey,
			CatalogName:    spec.CatalogName,
			LeaseMinutes:   lease,
		},
	})
}

// endSession sends an end command to Pulsar. Ending an already ended/absent
// session is idempotent there.
func (s *Service) endSession(ctx context.Context, accountID uuid.UUID, manualID string) error {
	if s == nil || s.nats == nil || s.nats.Conn == nil {
		return errors.New("presenceproviders: event bus is not configured")
	}
	return s.nats.PublishJetStream(ctx, presencecmd.Subject, presencecmd.Stream, presencecmd.Command{
		AccountID: accountID.String(),
		ManualID:  manualID,
		Action:    presencecmd.ActionEnd,
	})
}

// ─────────────────────────── normalization ───────────────────────────

func normalizeProvider(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	lowered := strings.ToLower(trimmed)
	return &lowered
}

func normalizeTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		trimmed := strings.TrimSpace(term)
		if trimmed == "" {
			continue
		}
		lowered := strings.ToLower(trimmed)
		if _, ok := seen[lowered]; ok {
			continue
		}
		seen[lowered] = struct{}{}
		out = append(out, lowered)
	}
	return out
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func trimmedPtr(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
