package nearby

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// clockSkew mirrors NearbyService.AllowedClockSkew.
const clockSkew = 2 * time.Minute

// AccountLookup hydrates the resolved peer payload (account + profile).
type AccountLookup interface {
	GetAccountWithProfile(ctx context.Context, id uuid.UUID) (*model.Account, error)
}

// RelationshipReader is the social-graph subset nearby needs. The module
// adapter reads through relationship.Service so the shared Redis caches stay
// authoritative; without the relationships module the reader is nil and every
// account is treated as a non-friend outsider.
type RelationshipReader interface {
	// IsFriendFamily reports whether the account has a Friends or CloseFriend
	// row pointing at the related account (the friends-family check).
	IsFriendFamily(ctx context.Context, accountID, relatedID uuid.UUID) (bool, error)
	// IsBlockedEitherDirection reports a non-expired block in either
	// direction.
	IsBlockedEitherDirection(ctx context.Context, accountID, relatedID uuid.UUID) (bool, error)
}

// FileResolver hydrates a bare stored file reference. It is optional: without
// the files port the reference's cached URL is served as-is.
type FileResolver interface {
	Resolve(ctx context.Context, fileID string) (*model.SnCloudFileReferenceObject, error)
}

// Service implements the presence-token issue/resolve flow.
type Service struct {
	store         *Store
	accounts      AccountLookup
	relationships RelationshipReader
	files         FileResolver
	settings      Settings
	log           *slog.Logger
}

// NewService builds the domain service. A blank settings secret leaves the
// service unavailable (ErrUnavailable) until the deployment configures one.
func NewService(st *Store, accounts AccountLookup, relationships RelationshipReader, files FileResolver, settings Settings, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(settings.ServiceUUID) == "" {
		settings.ServiceUUID = DefaultServiceUUID
	}
	settings.SlotDurationSec = normalizeSlotDuration(settings.SlotDurationSec)
	settings.PresenceTokenSecret = strings.TrimSpace(settings.PresenceTokenSecret)
	return &Service{
		store:         st,
		accounts:      accounts,
		relationships: relationships,
		files:         files,
		settings:      settings,
		log:           log,
	}
}

// ServiceUUID returns the advertised BLE service UUID.
func (s *Service) ServiceUUID() string { return s.settings.ServiceUUID }

// SlotDurationSec returns the effective slot duration.
func (s *Service) SlotDurationSec() int { return s.settings.SlotDurationSec }

// Ready reports whether tokens can be issued or verified.
func (s *Service) Ready() bool { return s.settings.PresenceTokenSecret != "" }

// IssuePresenceTokens ports NearbyService.IssuePresenceTokensAsync.
func (s *Service) IssuePresenceTokens(ctx context.Context, userID uuid.UUID, request IssueRequest) (IssueResult, error) {
	if !s.Ready() {
		return IssueResult{}, ErrUnavailable
	}
	deviceID := strings.TrimSpace(request.DeviceID)
	if deviceID == "" || len(deviceID) > 256 {
		return IssueResult{}, fmt.Errorf("nearby: invalid device id")
	}
	if s.store == nil {
		return IssueResult{}, errors.New("nearby: store unavailable")
	}

	now := time.Now().UTC()
	prefetch := request.PrefetchSlots
	if prefetch < 1 {
		prefetch = 1
	}
	if prefetch > MaxPrefetchSlots {
		prefetch = MaxPrefetchSlots
	}
	duration := int64(s.settings.SlotDurationSec)
	currentSlot := now.Unix() / duration

	device, err := s.store.FindDevice(ctx, userID, deviceID)
	isNew := false
	if errors.Is(err, store.ErrNotFound) {
		device = &NearbyDeviceEntity{
			ID:       uuid.New(),
			UserID:   userID,
			DeviceID: deviceID,
			Status:   int(DeviceActive),
		}
		isNew = true
	} else if err != nil {
		return IssueResult{}, err
	}

	settingsChanged := device.Discoverable != request.Discoverable ||
		device.FriendOnly != request.FriendOnly ||
		device.Capabilities != request.Capabilities ||
		device.Status != int(DeviceActive)

	device.Discoverable = request.Discoverable
	device.FriendOnly = request.FriendOnly
	device.Capabilities = request.Capabilities
	device.Status = int(DeviceActive)
	device.LastHeartbeatAt = &now
	device.LastTokenIssuedAt = &now

	if settingsChanged {
		if err := s.store.DeleteTokensFromSlot(ctx, device.ID, currentSlot); err != nil {
			return IssueResult{}, err
		}
	}
	if isNew {
		if err := s.store.CreateDevice(ctx, device); err != nil {
			return IssueResult{}, err
		}
	} else if err := s.store.SaveDevice(ctx, device); err != nil {
		return IssueResult{}, err
	}

	existingSlots, err := s.store.ListTokenSlots(ctx, device.ID, currentSlot, currentSlot+int64(prefetch))
	if err != nil {
		return IssueResult{}, err
	}
	existing := make(map[int64]struct{}, len(existingSlots))
	for _, slot := range existingSlots {
		existing[slot] = struct{}{}
	}

	tokens := make([]PresenceToken, 0, prefetch)
	pending := make([]*NearbyPresenceTokenEntity, 0, prefetch)
	for i := range prefetch {
		slot := currentSlot + int64(i)
		token := s.generateToken(userID, deviceID, slot, request.FriendOnly, request.Discoverable, request.Capabilities)
		validFrom := slot * duration
		validTo := validFrom + duration
		if _, ok := existing[slot]; !ok {
			pending = append(pending, &NearbyPresenceTokenEntity{
				ID:           uuid.New(),
				DeviceID:     device.ID,
				Slot:         slot,
				TokenHash:    hashToken(token),
				ValidFrom:    time.Unix(validFrom, 0).UTC(),
				ValidTo:      time.Unix(validTo, 0).UTC(),
				Discoverable: request.Discoverable,
				FriendOnly:   request.FriendOnly,
				Capabilities: request.Capabilities,
			})
		}
		tokens = append(tokens, PresenceToken{
			Slot:      slot,
			Token:     token,
			ValidFrom: validFrom,
			ValidTo:   validTo,
		})
	}
	if err := s.store.CreateTokens(ctx, pending); err != nil {
		return IssueResult{}, err
	}
	return IssueResult{
		ServiceUUID:     s.settings.ServiceUUID,
		SlotDurationSec: s.settings.SlotDurationSec,
		Tokens:          tokens,
	}, nil
}

// Resolve ports NearbyService.ResolveAsync.
func (s *Service) Resolve(ctx context.Context, observerID uuid.UUID, observations []Observation) ([]Peer, error) {
	if !s.Ready() {
		return nil, ErrUnavailable
	}
	if len(observations) == 0 {
		return []Peer{}, nil
	}
	if s.store == nil {
		return nil, errors.New("nearby: store unavailable")
	}

	now := time.Now().UTC()
	selected := dedupeObservations(observations, now)

	hashes := make([]string, 0, len(selected))
	slots := make([]int64, 0, len(selected))
	seenHash := make(map[string]struct{}, len(selected))
	seenSlot := make(map[int64]struct{}, len(selected))
	tokens := make([]string, len(selected))
	for i, observation := range selected {
		token := strings.TrimSpace(observation.Token)
		tokens[i] = token
		hash := hashToken(token)
		if _, ok := seenHash[hash]; !ok {
			seenHash[hash] = struct{}{}
			hashes = append(hashes, hash)
		}
		if _, ok := seenSlot[observation.Slot]; !ok {
			seenSlot[observation.Slot] = struct{}{}
			slots = append(slots, observation.Slot)
		}
	}

	matches, err := s.store.FindMatches(ctx, hashes, slots, now, clockSkew)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]TokenMatch, len(matches))
	for _, match := range matches {
		byKey[match.TokenHash+":"+fmt.Sprint(match.Slot)] = match
	}

	nowUnix := now.Unix()
	friendCache := make(map[uuid.UUID]bool)
	blockedCache := make(map[uuid.UUID]bool)
	accountCache := make(map[uuid.UUID]*model.Account)
	peerIndex := make(map[uuid.UUID]int)
	peerRSSI := make(map[uuid.UUID]int)
	peers := make([]Peer, 0)

	for i, observation := range selected {
		match, ok := byKey[hashToken(tokens[i])+":"+fmt.Sprint(observation.Slot)]
		if !ok {
			continue
		}
		if match.UserID == observerID {
			continue
		}

		isFriend, ok := friendCache[match.UserID]
		if !ok {
			isFriend = false
			if s.relationships != nil {
				isFriend, err = s.relationships.IsFriendFamily(ctx, observerID, match.UserID)
				if err != nil {
					return nil, err
				}
			}
			friendCache[match.UserID] = isFriend
		}
		if match.FriendOnly && !isFriend {
			continue
		}

		blocked, ok := blockedCache[match.UserID]
		if !ok {
			blocked = false
			if s.relationships != nil {
				blocked, err = s.relationships.IsBlockedEitherDirection(ctx, observerID, match.UserID)
				if err != nil {
					return nil, err
				}
			}
			blockedCache[match.UserID] = blocked
		}
		if blocked {
			continue
		}

		account, ok := accountCache[match.UserID]
		if !ok {
			account, err = s.account(ctx, match.UserID)
			if err != nil {
				return nil, err
			}
			accountCache[match.UserID] = account
		}
		if account == nil {
			continue
		}

		displayName := account.Name
		if strings.TrimSpace(account.Nick) != "" {
			displayName = account.Nick
		}
		avatar := s.avatar(ctx, account)
		lastSeen := observationLastSeen(observation, nowUnix)
		peer := Peer{
			UserID:      account.Id,
			DisplayName: displayName,
			Avatar:      avatar,
			IsFriend:    isFriend,
			CanInvite:   true,
			Visibility:  "public",
			LastSeenAt:  lastSeen,
		}
		if match.FriendOnly {
			peer.Visibility = "friend_only"
		}
		if index, exists := peerIndex[match.UserID]; exists {
			if observation.AvgRSSI > peerRSSI[match.UserID] {
				peers[index] = peer
				peerRSSI[match.UserID] = observation.AvgRSSI
			}
			continue
		}
		peerIndex[match.UserID] = len(peers)
		peerRSSI[match.UserID] = observation.AvgRSSI
		peers = append(peers, peer)
	}

	sort.SliceStable(peers, func(a, b int) bool {
		return peers[a].LastSeenAt > peers[b].LastSeenAt
	})
	return peers, nil
}

func (s *Service) account(ctx context.Context, id uuid.UUID) (*model.Account, error) {
	if s.accounts == nil {
		return nil, nil
	}
	account, err := s.accounts.GetAccountWithProfile(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return account, nil
}

// avatar mirrors NearbyService's Avatar = account.Profile?.Picture and then
// fills a bare reference through the optional files port.
func (s *Service) avatar(ctx context.Context, account *model.Account) *model.SnCloudFileReferenceObject {
	if account == nil || account.Profile == nil || account.Profile.Picture == nil {
		return nil
	}
	picture := account.Profile.Picture
	if s.files != nil && picture.Id != "" && picture.Url == "" {
		if resolved, err := s.files.Resolve(ctx, picture.Id); err == nil && resolved != nil {
			return resolved
		} else if err != nil {
			s.log.Warn("resolve nearby peer avatar", "file", picture.Id, "error", err)
		}
	}
	return picture
}

// dedupeObservations keeps, per (slot, token), the highest seen count and
// then the latest last-seen time, preserving the first-seen order like LINQ's
// GroupBy.
func dedupeObservations(observations []Observation, now time.Time) []Observation {
	selected := make(map[string]Observation, len(observations))
	order := make([]string, 0, len(observations))
	for _, observation := range observations {
		if strings.TrimSpace(observation.Token) == "" {
			continue
		}
		key := fmt.Sprintf("%d:%s", observation.Slot, strings.TrimSpace(observation.Token))
		previous, ok := selected[key]
		if !ok {
			selected[key] = observation
			order = append(order, key)
			continue
		}
		if observation.SeenCount > previous.SeenCount ||
			(observation.SeenCount == previous.SeenCount && observationLastSeen(observation, now.Unix()) > observationLastSeen(previous, now.Unix())) {
			selected[key] = observation
		}
	}
	result := make([]Observation, 0, len(order))
	for _, key := range order {
		result = append(result, selected[key])
	}
	return result
}

func observationLastSeen(observation Observation, fallback int64) int64 {
	if observation.LastSeenAt != nil {
		return *observation.LastSeenAt
	}
	return fallback
}

func (s *Service) generateToken(userID uuid.UUID, deviceID string, slot int64, friendOnly, discoverable bool, capabilities int) string {
	material := fmt.Sprintf("%s:%s:%d:%d:%d:%d",
		hex.EncodeToString(userID[:]), deviceID, slot, boolInt(friendOnly), boolInt(discoverable), capabilities)
	mac := hmac.New(sha256.New, []byte(s.settings.PresenceTokenSecret))
	_, _ = mac.Write([]byte(material))
	sum := mac.Sum(nil)
	return strings.ToUpper(hex.EncodeToString(sum[:TokenBytes]))
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
