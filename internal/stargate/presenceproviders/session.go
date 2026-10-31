package presenceproviders

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
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

// startSession reproduces POST /api/activities/sessions/start for a scanner:
//
//   - a non-blank catalog key resolves or creates the catalog item (an
//     existing row is reused untouched, mirroring UpsertCatalogItem);
//   - an open session with the same manual id is refreshed in place and its
//     lease renewed;
//   - otherwise a new activity starts now.
//
// The session start handler cannot be called directly because it is an HTTP
// handler; this helper writes the same rows through the store primitives.
func (s *Service) startSession(ctx context.Context, accountID uuid.UUID, manualID string, spec sessionSpec) error {
	if s == nil || s.store == nil {
		return errors.New("presenceproviders: store is not configured")
	}
	if strings.TrimSpace(manualID) == "" || strings.TrimSpace(spec.Type) == "" {
		return errors.New("presenceproviders: manual id and type are required")
	}
	now := s.now()
	lease := LeaseMinutes
	provider := normalizeProvider(spec.Provider)
	terms := normalizeTerms(spec.QueryableTerms)
	tags := normalizeTags(spec.Tags)
	visibility := model.PresenceVisibilityPublic

	var catalog *model.PresenceCatalogItem
	if key := strings.TrimSpace(spec.CatalogKey); key != "" {
		existing, err := s.store.GetCatalogItemByKey(ctx, accountID, provider, key)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if existing != nil {
			catalog = existing
		} else {
			name := spec.CatalogName
			if strings.TrimSpace(derefString(name)) == "" {
				name = spec.Title
			}
			catalogKey := key
			candidate := &model.PresenceCatalogItem{
				AccountId:      accountID.String(),
				Provider:       provider,
				CatalogKey:     &catalogKey,
				ReferenceId:    spec.ReferenceID,
				Name:           name,
				Subtitle:       spec.Subtitle,
				Caption:        spec.Caption,
				Visibility:     visibility,
				Tags:           tags,
				LargeImage:     spec.LargeImage,
				SmallImage:     spec.SmallImage,
				TitleUrl:       spec.TitleURL,
				SubtitleUrl:    spec.SubtitleURL,
				QueryableTerms: terms,
				Meta:           spec.Meta,
			}
			if candidate.Meta == nil {
				candidate.Meta = map[string]any{}
			}
			saved, err := s.store.UpsertCatalogItem(ctx, candidate)
			if err != nil {
				return err
			}
			catalog = saved
		}
	}

	activity, err := s.store.GetActiveSessionByManualID(ctx, accountID, manualID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if activity != nil {
		activity.Type = new(spec.Type)
		activity.Provider = provider
		activity.ReferenceId = spec.ReferenceID
		activity.Title = spec.Title
		activity.Subtitle = spec.Subtitle
		activity.Caption = spec.Caption
		activity.LargeImage = spec.LargeImage
		activity.SmallImage = spec.SmallImage
		activity.TitleUrl = spec.TitleURL
		activity.SubtitleUrl = spec.SubtitleURL
		activity.QueryableTerms = terms
		activity.Meta = spec.Meta
		activity.Visibility = visibility
		activity.Tags = tags
		if catalog != nil {
			catalogID := catalog.Id
			activity.CatalogId = &catalogID
			activity.Catalog = catalog
		}
		activity.LeaseMinutes = lease
		activity.LeaseExpiresAt = model.NewTime(now.Add(time.Duration(lease) * time.Minute))
		if err := s.store.UpdateActivity(ctx, activity); err != nil {
			return err
		}
		s.broadcastActivities(ctx, accountID)
		return nil
	}

	created := &model.PresenceActivity{
		AccountId:      accountID.String(),
		ManualId:       new(manualID),
		Type:           new(spec.Type),
		Tags:           tags,
		Visibility:     visibility,
		Provider:       provider,
		ReferenceId:    spec.ReferenceID,
		Title:          spec.Title,
		Subtitle:       spec.Subtitle,
		Caption:        spec.Caption,
		LargeImage:     spec.LargeImage,
		SmallImage:     spec.SmallImage,
		TitleUrl:       spec.TitleURL,
		SubtitleUrl:    spec.SubtitleURL,
		QueryableTerms: terms,
		Meta:           spec.Meta,
		StartedAt:      model.NewTime(now),
		LeaseMinutes:   lease,
		LeaseExpiresAt: model.NewTime(now.Add(time.Duration(lease) * time.Minute)),
	}
	if catalog != nil {
		catalogID := catalog.Id
		created.CatalogId = &catalogID
		created.Catalog = catalog
	}
	if err := s.store.InsertActivity(ctx, created); err != nil {
		return err
	}
	s.broadcastActivities(ctx, accountID)
	return nil
}

// endSession mirrors POST /api/activities/sessions/end: ending an already
// ended/absent session is not an error, exactly like the C# RemotePresence
// client treating Stargate's 404 as "nothing was running".
func (s *Service) endSession(ctx context.Context, accountID uuid.UUID, manualID string) error {
	if s == nil || s.store == nil {
		return errors.New("presenceproviders: store is not configured")
	}
	_, err := s.store.EndActivitySession(ctx, accountID, manualID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	s.broadcastActivities(ctx, accountID)
	return nil
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

func normalizeTags(tags []model.PresenceTag) []model.PresenceTag {
	out := make([]model.PresenceTag, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		slug := strings.TrimSpace(tag.Slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, model.PresenceTag{Slug: slug, Name: tag.Name})
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
