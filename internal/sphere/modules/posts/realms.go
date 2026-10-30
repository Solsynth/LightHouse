package posts

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
)

// errRealmUnavailable marks the absence of the realms port/client. Callers use
// it to skip a filter instead of hiding every post.
var errRealmUnavailable = errors.New("realm service unavailable")

func (h *handler) realmsPort() (ports.Realms, bool) {
	if h.ports == nil {
		return nil, false
	}
	return module.Get[ports.Realms](h.ports, ports.PortRealms)
}

func realmInfoToRef(info *ports.RealmInfo) *model.RealmRef {
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

// loadRealmRefs resolves realm snapshots for the posts being hydrated. A
// missing port leaves Post.Realm null rather than failing the read.
func (h *handler) loadRealmRefs(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]*model.RealmRef {
	out := map[uuid.UUID]*model.RealmRef{}
	realms, ok := h.realmsPort()
	if !ok {
		return out
	}
	seen := map[uuid.UUID]struct{}{}
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		info, err := realms.GetRealm(ctx, id.String())
		if err != nil {
			h.log.Debug("posts: load realm failed", "realm", id, "error", err)
			continue
		}
		if ref := realmInfoToRef(info); ref != nil {
			out[id] = ref
		}
	}
	return out
}

// realmByID resolves a realm through the port, falling back to the directly
// configured Realm client when the integrations module did not publish it.
func (h *handler) realmByID(ctx context.Context, id uuid.UUID) (*ports.RealmInfo, error) {
	if realms, ok := h.realmsPort(); ok {
		return realms.GetRealm(ctx, id.String())
	}
	if h.core != nil && h.core.Clients != nil && h.core.Clients.Realm != nil {
		realm, err := h.core.Clients.Realm.GetRealm(ctx, &gen.DyGetRealmRequest{
			Query: &gen.DyGetRealmRequest_Id{Id: id.String()},
		})
		if err != nil {
			return nil, err
		}
		return realmInfoFromProto(realm), nil
	}
	return nil, errRealmUnavailable
}

// realmIDBySlug resolves a slug for the list filter.
func (h *handler) realmIDBySlug(ctx context.Context, slug string) (uuid.UUID, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return uuid.Nil, errRealmUnavailable
	}
	var info *ports.RealmInfo
	if realms, ok := h.realmsPort(); ok {
		found, err := realms.GetRealmBySlug(ctx, slug)
		if err != nil {
			if errors.Is(err, ports.ErrUnavailable) {
				return uuid.Nil, errRealmUnavailable
			}
			return uuid.Nil, err
		}
		info = found
	} else if h.core != nil && h.core.Clients != nil && h.core.Clients.Realm != nil {
		realm, err := h.core.Clients.Realm.GetRealm(ctx, &gen.DyGetRealmRequest{
			Query: &gen.DyGetRealmRequest_Slug{Slug: slug},
		})
		if err != nil {
			return uuid.Nil, err
		}
		info = realmInfoFromProto(realm)
	} else {
		return uuid.Nil, errRealmUnavailable
	}
	if info == nil {
		return uuid.Nil, errors.New("realm not found")
	}
	id, err := uuid.Parse(info.ID)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func realmInfoFromProto(realm *gen.DyRealm) *ports.RealmInfo {
	if realm == nil {
		return nil
	}
	var description *string
	if value := realm.GetDescription(); value != "" {
		description = &value
	}
	return &ports.RealmInfo{
		ID:          realm.GetId(),
		Slug:        realm.GetSlug(),
		Name:        realm.GetName(),
		Description: description,
		IsPublic:    realm.GetIsPublic(),
	}
}

// realmMembership reports whether the account belongs to the realm. The realms
// port exposes only snapshots today, so the check goes through the direct
// Stargate client when it is configured. checked is false when no membership
// API is reachable; callers log that gap instead of failing closed.
func (h *handler) realmMembership(ctx context.Context, realmID, accountID uuid.UUID) (member, checked bool) {
	if realmID == uuid.Nil || accountID == uuid.Nil {
		return false, true
	}
	if h.core == nil || h.core.Clients == nil || h.core.Clients.Realm == nil {
		return true, false
	}
	value, err := h.core.Clients.Realm.IsMemberWithRole(ctx, &gen.DyIsMemberWithRoleRequest{
		RealmId:       realmID.String(),
		AccountId:     accountID.String(),
		RequiredRoles: []int32{0},
	})
	if err != nil {
		h.log.Warn("posts: realm membership check failed", "realm", realmID, "error", err)
		return true, false
	}
	return value.GetValue(), true
}

// validatePostRealm verifies that the realm exists and the poster belongs to
// it. It returns an HTTP status and message when the realm must be rejected.
func (h *handler) validatePostRealm(ctx context.Context, realmID, accountID uuid.UUID) (int, string) {
	if _, err := h.realmByID(ctx, realmID); err != nil {
		if errors.Is(err, errRealmUnavailable) {
			// Fail closed: a post that claims a realm must be authorised by the
			// realm service. Accepting the claim while the service is down would
			// let any account post into any realm.
			h.log.Warn("posts: realm validation failed, realm service unavailable",
				"realm", realmID)
			return 503, "REALM_SERVICE_UNAVAILABLE"
		}
		h.log.Debug("posts: realm not found", "realm", realmID, "error", err)
		return 400, "REALM_NOT_FOUND"
	}
	member, checked := h.realmMembership(ctx, realmID, accountID)
	if !checked {
		h.log.Warn("posts: realm membership not verified, membership API unavailable",
			"realm", realmID, "account", accountID)
		return 503, "REALM_MEMBERSHIP_UNVERIFIED"
	}
	if !member {
		return 403, "REALM_MEMBERSHIP_REQUIRED"
	}
	return 0, ""
}
