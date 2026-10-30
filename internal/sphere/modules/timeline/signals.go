package timeline

import (
	"context"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
)

// realmRefFromInfo maps a ports.RealmInfo snapshot onto the realm reference
// embedded in timeline payloads.
func realmRefFromInfo(info *ports.RealmInfo) *model.RealmRef {
	if info == nil {
		return nil
	}
	return &model.RealmRef{
		ID:           info.ID,
		Slug:         info.Slug,
		Name:         info.Name,
		Description:  info.Description,
		IsCommunity:  info.IsCommunity,
		IsPublic:     info.IsPublic,
		Picture:      info.Picture,
		Background:   info.Background,
		Verification: info.Verification,
		AccountID:    info.AccountID,
		BoostPoints:  model.RealmBoostPoints(info.BoostPoints),
		BoostLevel:   info.BoostLevel,
	}
}

// realmRefs resolves realm snapshots for a batch of realm ids. The realms port
// wins; the realm gRPC client fills in whatever the port could not answer, so a
// disabled integration degrades to the legacy path instead of losing the
// snapshot.
func (s *service) realmRefs(ctx context.Context, ids []string) map[string]*model.RealmRef {
	out := make(map[string]*model.RealmRef, len(ids))
	if len(ids) == 0 {
		return out
	}
	missing := ids
	if s.realms != nil {
		missing = make([]string, 0, len(ids))
		for _, id := range ids {
			info, err := s.realms.GetRealm(ctx, id)
			if err != nil {
				s.log.Warn("timeline: realms port", "id", id, "error", err)
				missing = append(missing, id)
				continue
			}
			if info == nil {
				continue
			}
			out[id] = realmRefFromInfo(info)
		}
	}
	for id, realm := range s.realmBatch(ctx, missing) {
		out[id] = realmRefFromProto(realm)
	}
	return out
}

// attachEventRealms populates the realm snapshot on every timeline event that
// carries a post and does not have one yet, mirroring the C# service's
// LoadPostsRealmsAsync over the finished page.
func (s *service) attachEventRealms(ctx context.Context, page model.TimelinePage) {
	if s.realms == nil {
		return
	}
	missing := make([]string, 0, len(page.Items))
	seen := map[string]struct{}{}
	for _, item := range page.Items {
		post, ok := eventPost(item)
		if !ok || post.Realm != nil || post.RealmID == nil {
			continue
		}
		if _, dup := seen[*post.RealmID]; dup {
			continue
		}
		seen[*post.RealmID] = struct{}{}
		missing = append(missing, *post.RealmID)
	}
	if len(missing) == 0 {
		return
	}
	refs := s.realmRefs(ctx, missing)
	for i := range page.Items {
		post, ok := eventPost(page.Items[i])
		if !ok || post.Realm != nil || post.RealmID == nil {
			continue
		}
		if ref, ok := refs[*post.RealmID]; ok {
			post.Realm = ref
			page.Items[i].Data = post
		}
	}
}

// insertSponsored mirrors TimelineService.MaybeInsertAdAsync: at most one
// sponsored post per page, placed at the fixed ad slot index. Pages that
// already carry the sponsored post are left untouched.
func (s *service) insertSponsored(ctx context.Context, posts []model.Post) []model.Post {
	if s.signals == nil || len(posts) == 0 {
		return posts
	}
	sponsored, err := s.signals.SponsoredPost(ctx)
	if err != nil {
		s.log.Warn("timeline: sponsored post", "error", err)
		return posts
	}
	if sponsored == nil {
		return posts
	}
	for i := range posts {
		if posts[i].ID == sponsored.ID {
			return posts
		}
	}
	post := *sponsored
	post.Sponsored = true
	s.loadPostRealms(ctx, []model.Post{post})

	if err := s.signals.MarkSponsoredShown(ctx, post.ID); err != nil {
		s.log.Warn("timeline: mark sponsored shown", "post", post.ID, "error", err)
	}

	index := adInsertionIndex
	if index > len(posts) {
		index = len(posts)
	}
	out := make([]model.Post, 0, len(posts)+1)
	out = append(out, posts[:index]...)
	out = append(out, post)
	out = append(out, posts[index:]...)
	s.log.Debug("timeline: ad inserted", "index", index, "post", post.ID)
	return out
}

// recordPageViews reports the posts on a finished page as views for the
// current viewer; accountID is empty for anonymous readers.
func (s *service) recordPageViews(ctx context.Context, page model.TimelinePage, accountID string) {
	if s.signals == nil {
		return
	}
	seen := map[string]struct{}{}
	for _, item := range page.Items {
		post, ok := eventPost(item)
		if !ok || post.ID == "" {
			continue
		}
		if _, dup := seen[post.ID]; dup {
			continue
		}
		seen[post.ID] = struct{}{}
		if err := s.signals.IncrementViews(ctx, post.ID, accountID); err != nil {
			s.log.Warn("timeline: increment views", "post", post.ID, "error", err)
		}
	}
}

// eventPost extracts the post a timeline event carries.
func eventPost(event model.TimelineEvent) (model.Post, bool) {
	switch data := event.Data.(type) {
	case model.Post:
		return data, true
	case *model.Post:
		if data == nil {
			return model.Post{}, false
		}
		return *data, true
	}
	return model.Post{}, false
}
