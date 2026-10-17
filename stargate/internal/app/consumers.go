package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/nats"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	redisclient "src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/relationship"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// consumeAccountActivated mirrors Padlock's AccountActivatedEvent consumer:
// it sets activated_at, grants the `verified` group and clears the
// permission cache. Runs until ctx is cancelled. Malformed events are acked
// (they can never succeed on redelivery), mirroring the C# behavior; DB
// errors leave the message unacked for redelivery.
func consumeAccountActivated(ctx context.Context, nc *nats.Client, st *store.Store, rc *redisclient.Client, log *slog.Logger) {
	if err := nc.ConsumeAccountEvents(ctx, "accounts.activated", "stargate_accountactivatedevent_consumer", func(payload []byte) error {
		var ev nats.AccountActivatedEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			log.Warn("malformed accounts.activated event", "error", err)
			return nil
		}
		id, err := uuid.Parse(ev.AccountID)
		if err != nil {
			log.Warn("accounts.activated event has invalid account id", "account_id", ev.AccountID, "payload", string(payload))
			return nil
		}
		activated, err := st.ActivateAccountAndGrantVerified(ctx, id, ev.ActivatedAt)
		if err != nil {
			return err
		}
		if !activated {
			log.Warn("accounts.activated for missing or already-activated account", "account_id", ev.AccountID)
			return nil
		}
		rc.ClearActorPermissionCache(ctx, ev.AccountID)
		log.Info("activated account from accounts.activated", "account_id", ev.AccountID)
		return nil
	}); err != nil {
		log.Warn("accounts.activated consumer stopped", "error", err)
	}
}

// consumeTestPassedGroupGrant mirrors Padlock's
// AccountTestPassedPermissionGroupEvent consumer: it grants the account the
// permission group a passed test configured and clears the permission cache.
func consumeTestPassedGroupGrant(ctx context.Context, nc *nats.Client, perm *permission.Service, rc *redisclient.Client, log *slog.Logger) {
	if err := nc.ConsumeAccountEvents(ctx, "accounts.tests.permission-group-granted", "stargate_accounttestpassedpermissiongroupevent_consumer", func(payload []byte) error {
		var ev nats.AccountTestPassedPermissionGroupEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			log.Warn("malformed accounts.tests.permission-group-granted event", "error", err)
			return nil
		}
		id, err := uuid.Parse(ev.AccountID)
		if err != nil {
			log.Warn("accounts.tests.permission-group-granted event has invalid account id", "account_id", ev.AccountID, "payload", string(payload))
			return nil
		}
		granted, err := perm.GrantPermissionGroup(ctx, id, ev.PermissionGroupKey)
		if err != nil {
			return err
		}
		if !granted {
			log.Warn("permission group not found for passed test",
				"account_id", ev.AccountID, "group_key", ev.PermissionGroupKey, "test_id", ev.TestID)
			return nil
		}
		rc.ClearActorPermissionCache(ctx, ev.AccountID)
		log.Info("granted permission group from passed test",
			"account_id", ev.AccountID, "group_key", ev.PermissionGroupKey, "test_id", ev.TestID)
		return nil
	}); err != nil {
		log.Warn("accounts.tests.permission-group-granted consumer stopped", "error", err)
	}
}

// consumeProfileFieldUpdated applies Passport-published profile field patches
// (last_seen touches, XP deltas, social-credit recomputes, active badge and
// verification changes) to Stargate's account_profiles after the profile
// table moved here. Malformed events are acked; DB errors redeliver.
func consumeProfileFieldUpdated(ctx context.Context, nc *nats.Client, st *store.Store, log *slog.Logger) {
	if err := nc.ConsumeAccountEvents(ctx, "accounts.profile_updated", "stargate_profilefieldupdatedevent_consumer", func(payload []byte) error {
		var ev nats.ProfileFieldUpdatedEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			log.Warn("malformed accounts.profile_updated event", "error", err)
			return nil
		}
		id, err := uuid.Parse(ev.AccountID)
		if err != nil {
			log.Warn("accounts.profile_updated event has invalid account id", "account_id", ev.AccountID, "payload", string(payload))
			return nil
		}
		patch := &store.ProfileFieldPatch{
			LastSeenAt:      ev.LastSeenAt,
			Experience:      ev.Experience,
			ExperienceDelta: ev.ExperienceDelta,
			SocialCredits:   ev.SocialCredits,
		}
		if len(ev.ActiveBadge) > 0 {
			patch.HasActiveBadge = true
			if string(ev.ActiveBadge) != "null" {
				var value any
				if err := json.Unmarshal(ev.ActiveBadge, &value); err != nil {
					log.Warn("accounts.profile_updated event has malformed active_badge", "account_id", ev.AccountID, "error", err)
					return nil
				}
				patch.ActiveBadge = value
			}
		}
		if len(ev.Verification) > 0 {
			patch.HasVerification = true
			if string(ev.Verification) != "null" {
				var mark model.SnVerificationMark
				if err := json.Unmarshal(ev.Verification, &mark); err != nil {
					log.Warn("accounts.profile_updated event has malformed verification", "account_id", ev.AccountID, "error", err)
					return nil
				}
				patch.Verification = &mark
			}
		}
		if err := st.ApplyProfileFieldPatch(ctx, id, patch); err != nil {
			return err
		}
		log.Debug("applied accounts.profile_updated patch", "account_id", ev.AccountID)
		return nil
	}); err != nil {
		log.Warn("accounts.profile_updated consumer stopped", "error", err)
	}
}

// consumeLastActive applies the fleet's last-active signals (published by the
// shared DysonTokenAuthHandler on accounts.last_active): profile last_seen_at
// plus the session last_granted_at/keep-alive. Malformed events are acked;
// DB errors redeliver.
func consumeLastActive(ctx context.Context, nc *nats.Client, st *store.Store, log *slog.Logger) {
	if err := nc.ConsumeAccountEvents(ctx, "accounts.last_active", "stargate_lastactiveevent_consumer", func(payload []byte) error {
		var ev nats.LastActiveEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			log.Warn("malformed accounts.last_active event", "error", err)
			return nil
		}
		if _, err := uuid.Parse(ev.AccountID); err != nil {
			log.Warn("accounts.last_active event has invalid account id", "account_id", ev.AccountID, "payload", string(payload))
			return nil
		}
		sessionID := ""
		if ev.SessionID != "" {
			if _, err := uuid.Parse(ev.SessionID); err == nil {
				sessionID = ev.SessionID
			}
		}
		if err := st.TouchLastActive(ctx, ev.AccountID, sessionID, ev.SeenAt); err != nil {
			return err
		}
		log.Debug("applied accounts.last_active", "account_id", ev.AccountID)
		return nil
	}); err != nil {
		log.Warn("accounts.last_active consumer stopped", "error", err)
	}
}

// consumeAccountRemoval drops the removed account's relationship rows.
//
// The table moved to Stargate, so Passport's removal flow no longer deletes
// the rows itself (MagicSpellService.ExecuteSpellAccountRemoval used to) and
// publishes accounts.removal.confirmed instead. Without this consumer the
// account's id keeps appearing in its friends' and followers' lists, and each
// row keeps satisfying HasExistingRelationship, which is what a re-registered
// account would collide with. Malformed events are acked; DB errors redeliver.
func consumeAccountRemoval(ctx context.Context, nc *nats.Client, rel *relationship.Service, log *slog.Logger) {
	if err := nc.ConsumeAccountEvents(ctx, "accounts.removal.confirmed", "stargate_accountremovalconfirmedevent_consumer", func(payload []byte) error {
		var ev nats.AccountRemovalConfirmedEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			log.Warn("malformed accounts.removal.confirmed event", "error", err)
			return nil
		}
		id, err := uuid.Parse(ev.AccountID)
		if err != nil {
			log.Warn("accounts.removal.confirmed event has invalid account id",
				"account_id", ev.AccountID, "payload", string(payload))
			return nil
		}
		removed, err := rel.PurgeAccount(ctx, id)
		if err != nil {
			return err
		}
		log.Info("purged relationships for removed account", "account_id", ev.AccountID, "rows", removed)
		return nil
	}); err != nil {
		log.Warn("accounts.removal.confirmed consumer stopped", "error", err)
	}
}
