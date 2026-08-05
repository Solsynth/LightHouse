package e2eectl

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Constants mirroring E2EeService.
const (
	packetType                  = "e2ee.envelope"
	kpDepletedPacketType        = "e2ee.kp.depleted"
	groupResetPacketType        = "e2ee.group.reset"
	wsNamespace                 = "dev.solsynth.solian"
	mlsKeyPackageDailyLimit     = 10
	mlsKeyPackageRetentionDays  = 30
	maxFanoutPayloadsPerRequest = 1000
	minKeyPackagesPerDevice     = 3
)

// ServiceError mirrors the InvalidOperationException/KeyNotFoundException the
// C# E2EeService throws for business-rule failures. ASP.NET surfaces those as
// unhandled 500s; the handlers map ServiceError to a generic 500 ApiError.
type ServiceError struct{ Message string }

func (e *ServiceError) Error() string { return e.Message }

// DeviceCiphertextEnvelope mirrors the DeviceCiphertextEnvelope record.
type DeviceCiphertextEnvelope struct {
	RecipientDeviceID string
	ClientMessageID   *string
	Ciphertext        []byte
	Header            []byte
	Signature         []byte
	Meta              map[string]any
}

// fanoutRequest mirrors SendE2EeFanoutRequest.
type fanoutRequest struct {
	RecipientAccountID string
	SessionID          *string
	Type               int
	GroupID            *string
	ExpiresAt          *time.Time
	IncludeSenderCopy  bool
	Payloads           []DeviceCiphertextEnvelope
}

// Service ports E2EeService's MLS delivery semantics over the store helpers.
type Service struct {
	store  *store.Store
	events auth.EventBus
	blade  gen.WebSocketServiceClient
	log    *slog.Logger
}

// NewService wires the E2EE service. events may be nil (no realtime push);
// blade is the Blade websocket client used for the connection-status check.
func NewService(st *store.Store, events auth.EventBus, clients *grpcclient.Clients, log *slog.Logger) *Service {
	s := &Service{store: st, events: events, log: log}
	if clients != nil {
		s.blade = clients.Blade
	}
	return s
}

func (s *Service) logf() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

// --- Key packages ---

// PublishMlsKeyPackage mirrors PublishMlsKeyPackageAsync (purge expired, 24h
// upload limit, device upsert, insert).
func (s *Service) PublishMlsKeyPackage(ctx context.Context, accountID, deviceID string, deviceLabel *string, keyPackage []byte, ciphersuite string, meta map[string]any) (*MlsKeyPackage, error) {
	if len(keyPackage) == 0 {
		return nil, &ServiceError{"MLS key package cannot be empty."}
	}
	now := time.Now().UTC()
	if err := s.store.PurgeExpiredMlsKeyPackages(ctx, accountID, now.AddDate(0, 0, -mlsKeyPackageRetentionDays)); err != nil {
		return nil, err
	}
	uploadedInDay, err := s.store.CountMlsKeyPackagesUploadedSince(ctx, accountID, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	if uploadedInDay >= mlsKeyPackageDailyLimit {
		return nil, &ServiceError{fmt.Sprintf("MLS key package daily upload limit exceeded. Max %d per 24h.", mlsKeyPackageDailyLimit)}
	}
	if _, err := s.store.UpsertE2eeDevice(ctx, accountID, deviceID, deviceLabel, now); err != nil {
		return nil, err
	}
	kp := &store.MlsKeyPackage{
		Id:          uuid.NewString(),
		AccountId:   accountID,
		DeviceId:    deviceID,
		DeviceLabel: deviceLabel,
		KeyPackage:  keyPackage,
		Ciphersuite: ciphersuite,
		Meta:        meta,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.store.InsertMlsKeyPackage(ctx, kp); err != nil {
		return nil, err
	}
	return mlsKeyPackageWire(kp), nil
}

// ListMlsDeviceKeyPackages mirrors ListMlsDeviceKeyPackagesAsync (consume
// runs the claim in a SERIALIZABLE transaction and fires KP-depleted
// notifications after the commit).
func (s *Service) ListMlsDeviceKeyPackages(ctx context.Context, accountID string, requesterID *string, consume bool) ([]MlsDeviceKeyPackage, error) {
	now := time.Now().UTC()
	if err := s.store.PurgeExpiredMlsKeyPackages(ctx, accountID, now.AddDate(0, 0, -mlsKeyPackageRetentionDays)); err != nil {
		return nil, err
	}
	pkgs, consumed, err := s.store.ListMlsDeviceKeyPackages(ctx, accountID, requesterID, consume)
	if err != nil {
		return nil, err
	}
	for _, c := range consumed {
		s.checkAndNotifyKpDepleted(ctx, accountID, c.DeviceID, c.DeviceLabel)
	}
	responses := make([]MlsDeviceKeyPackage, 0, len(pkgs))
	for _, p := range pkgs {
		label := p.Device.DeviceLabel
		if label == nil {
			label = p.Package.DeviceLabel
		}
		responses = append(responses, MlsDeviceKeyPackage{
			AccountId:   p.Package.AccountId,
			DeviceId:    p.Package.DeviceId,
			DeviceLabel: label,
			Ciphersuite: p.Package.Ciphersuite,
			KeyPackage:  p.Package.KeyPackage,
			Meta:        p.Package.Meta,
		})
	}
	return responses, nil
}

// MlsKeyPackageStatus mirrors GetMlsKeyPackageStatusAsync.
func (s *Service) MlsKeyPackageStatus(ctx context.Context, accountID string) (*MlsKeyPackageStatus, error) {
	now := time.Now().UTC()
	if err := s.store.PurgeExpiredMlsKeyPackages(ctx, accountID, now.AddDate(0, 0, -mlsKeyPackageRetentionDays)); err != nil {
		return nil, err
	}
	statuses, err := s.store.MlsKeyPackageStatusPerDevice(ctx, accountID)
	if err != nil {
		return nil, err
	}
	devices := make([]MlsDeviceKpStatus, 0, len(statuses))
	for _, st := range statuses {
		devices = append(devices, MlsDeviceKpStatus{
			DeviceId:       st.DeviceID,
			DeviceLabel:    st.DeviceLabel,
			AvailableCount: st.AvailableCount,
		})
	}
	return &MlsKeyPackageStatus{NeedsMoreKps: len(devices) > 0, DevicesNeedingKps: devices}, nil
}

// GetCapableDevices mirrors GetCapableDevicesAsync.
func (s *Service) GetCapableDevices(ctx context.Context, groupID string) ([]MlsDeviceKeyPackage, error) {
	packages, err := s.store.GetCapableDevices(ctx, groupID)
	if err != nil {
		return nil, err
	}
	responses := make([]MlsDeviceKeyPackage, 0, len(packages))
	for _, p := range packages {
		responses = append(responses, MlsDeviceKeyPackage{
			AccountId:   p.AccountId,
			DeviceId:    p.DeviceId,
			DeviceLabel: p.DeviceLabel,
			Ciphersuite: p.Ciphersuite,
			KeyPackage:  p.KeyPackage,
			Meta:        p.Meta,
		})
	}
	return responses, nil
}

func (s *Service) checkAndNotifyKpDepleted(ctx context.Context, accountID, deviceID string, deviceLabel *string) {
	count, err := s.store.CountUnconsumedMlsKeyPackages(ctx, accountID, deviceID)
	if err != nil {
		s.logf().Warn("count key packages for depleted check", "device", deviceID, "error", err)
		return
	}
	if count < minKeyPackagesPerDevice {
		s.notifyKpDepleted(ctx, accountID, deviceID, deviceLabel, int(count))
	}
}

// notifyKpDepleted mirrors NotifyKpDepletedAsync: an account-level
// e2ee.kp.depleted websocket push (the C# routes it through the Ring
// service, which forwards to the websocket service).
func (s *Service) notifyKpDepleted(ctx context.Context, accountID, mlsDeviceID string, deviceLabel *string, availableCount int) {
	payload := kpDepletedPayload{
		MlsDeviceId:    mlsDeviceID,
		DeviceId:       mlsDeviceID,
		DeviceLabel:    deviceLabel,
		AvailableCount: availableCount,
	}
	if err := s.events.PublishWS(ctx, accountID, kpDepletedPacketType, payload); err != nil {
		s.logf().Warn("push KP depleted notification",
			"account", accountID, "device", mlsDeviceID, "error", err)
	}
}

// --- Groups ---

// BootstrapMlsGroup mirrors BootstrapMlsGroupAsync (SERIALIZABLE,
// create-if-absent; replay returns the existing state).
func (s *Service) BootstrapMlsGroup(ctx context.Context, accountID, groupID string, epoch, stateVersion int64, meta map[string]any) (*MlsGroupState, error) {
	now := time.Now().UTC()
	stateMeta := make(map[string]any, len(meta)+1)
	for k, v := range meta {
		stateMeta[k] = v
	}
	stateMeta["bootstrap_account_id"] = accountID
	state, err := s.store.BootstrapMlsGroup(ctx, accountID, groupID, epoch, stateVersion, stateMeta, now)
	if err != nil {
		return nil, err
	}
	return mlsGroupStateWire(state), nil
}

// CommitMlsGroup mirrors CommitMlsGroupAsync: epoch must be exactly
// current+1; returns (nil, nil) when the group is absent.
func (s *Service) CommitMlsGroup(ctx context.Context, accountID, groupID string, epoch int64, reason string, meta map[string]any) (*MlsGroupState, error) {
	state, err := s.store.GetMlsGroupStateByGroupID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, nil
	}
	if epoch <= state.Epoch {
		return mlsGroupStateWire(state), nil
	}
	if epoch != state.Epoch+1 {
		return nil, &ServiceError{fmt.Sprintf("MLS epoch mismatch. Current epoch is %d; requested epoch is %d.", state.Epoch, epoch)}
	}
	now := time.Now().UTC()
	state.Epoch = epoch
	state.StateVersion += 1
	state.LastCommitAt = &now
	if meta != nil {
		stateMeta := make(map[string]any, len(meta)+1)
		for k, v := range meta {
			stateMeta[k] = v
		}
		stateMeta["reason"] = reason
		state.Meta = stateMeta
	}
	state.UpdatedAt = now
	if err := s.store.UpdateMlsGroupState(ctx, state); err != nil {
		return nil, err
	}
	return mlsGroupStateWire(state), nil
}

// GetGroupState loads the group state, (nil, nil) when absent.
func (s *Service) GetGroupState(ctx context.Context, groupID string) (*MlsGroupState, error) {
	state, err := s.store.GetMlsGroupStateByGroupID(ctx, groupID)
	if err != nil || state == nil {
		return nil, err
	}
	return mlsGroupStateWire(state), nil
}

// IsMlsGroupMember mirrors IsMlsGroupMemberAsync.
func (s *Service) IsMlsGroupMember(ctx context.Context, accountID, deviceID, groupID string) (bool, error) {
	return s.store.IsMlsGroupMember(ctx, accountID, deviceID, groupID)
}

// UploadGroupInfo mirrors UploadGroupInfoAsync (SERIALIZABLE; epoch must
// match the current state when the group exists).
func (s *Service) UploadGroupInfo(ctx context.Context, groupID string, groupInfo, ratchetTree []byte, expectedEpoch int64) (*UploadGroupInfoResult, error) {
	result, err := s.store.UploadGroupInfo(ctx, groupID, groupInfo, ratchetTree, &expectedEpoch, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return &UploadGroupInfoResult{Success: result.Success, GroupId: result.GroupID, Epoch: result.Epoch}, nil
}

// ResetMlsGroup mirrors the controller's reset flow: flag all devices for
// reshare, notify, soft-delete the group, then create the fresh state.
// Returns (nil, nil) when the group does not exist.
func (s *Service) ResetMlsGroup(ctx context.Context, groupID string, newEpoch, stateVersion int64, reason *string) (*MlsGroupState, error) {
	group, err := s.store.GetMlsGroupStateByGroupID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	if _, err := s.store.MarkAllDevicesReshareRequired(ctx, groupID, now); err != nil {
		return nil, err
	}
	s.notifyGroupReset(ctx, groupID, reason)
	if _, err := s.store.DeleteMlsGroup(ctx, groupID, now); err != nil {
		return nil, err
	}
	newState, err := s.store.CreateMlsGroup(ctx, groupID, newEpoch, stateVersion+1, now)
	if err != nil {
		return nil, err
	}
	return mlsGroupStateWire(newState), nil
}

// notifyGroupReset mirrors NotifyGroupResetAsync: an e2ee.group.reset push to
// every distinct member account (the payload reason is the raw request
// reason, which may be null).
func (s *Service) notifyGroupReset(ctx context.Context, groupID string, reason *string) {
	userIds, err := s.store.ListMlsGroupMemberAccountIDs(ctx, groupID)
	if err != nil {
		s.logf().Warn("list group members for reset notify", "group", groupID, "error", err)
		return
	}
	if len(userIds) == 0 {
		return
	}
	payload := groupResetPayload{
		Type:      "mls.group.reset",
		GroupId:   groupID,
		Reason:    reason,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	for _, userID := range userIds {
		if err := s.events.PublishWS(ctx, userID, groupResetPacketType, payload); err != nil {
			s.logf().Warn("push group reset notify", "group", groupID, "user", userID, "error", err)
		}
	}
}

// --- Memberships / reshare ---

// MarkMlsReshareRequired mirrors MarkMlsReshareRequiredAsync (the C# request
// carries the reason but the service only stores the marker and epoch).
func (s *Service) MarkMlsReshareRequired(ctx context.Context, groupID, targetAccountID, targetDeviceID string, epoch int64, reason string) (*MlsDeviceMembership, error) {
	membership, err := s.store.MarkMlsReshareRequired(ctx, groupID, targetAccountID, targetDeviceID, epoch, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return mlsDeviceMembershipWire(membership), nil
}

// AddMlsDeviceMembership mirrors AddMlsDeviceMembershipAsync (revives
// soft-deleted rows, clears reshare markers).
func (s *Service) AddMlsDeviceMembership(ctx context.Context, accountID, deviceID, groupID string, epoch int64) (*MlsDeviceMembership, error) {
	membership, err := s.store.UpsertMlsDeviceMembership(ctx, groupID, accountID, deviceID, epoch, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return mlsDeviceMembershipWire(membership), nil
}

// GetDeviceReshareStatus mirrors GetDeviceMlsReshareStatusAsync.
func (s *Service) GetDeviceReshareStatus(ctx context.Context, accountID, deviceID string) ([]MlsDeviceMembership, error) {
	memberships, err := s.store.ListDeviceReshareStatus(ctx, accountID, deviceID)
	if err != nil {
		return nil, err
	}
	responses := make([]MlsDeviceMembership, 0, len(memberships))
	for i := range memberships {
		responses = append(responses, *mlsDeviceMembershipWire(&memberships[i]))
	}
	return responses, nil
}

// CompleteMlsReshare mirrors CompleteMlsReshareAsync.
func (s *Service) CompleteMlsReshare(ctx context.Context, accountID, deviceID, groupID string) (bool, error) {
	return s.store.CompleteMlsReshare(ctx, accountID, deviceID, groupID, time.Now().UTC())
}

// --- Envelope fanout ---

// FanoutMlsWelcome mirrors FanoutMlsWelcomeAsync.
func (s *Service) FanoutMlsWelcome(ctx context.Context, senderID, senderDeviceID, groupID string, recipientAccountID *string, expiresAt *time.Time, payloads []DeviceCiphertextEnvelope) ([]E2eeEnvelope, error) {
	if recipientAccountID != nil {
		items := make([]DeviceCiphertextEnvelope, 0, len(payloads))
		for _, p := range payloads {
			items = append(items, DeviceCiphertextEnvelope{
				RecipientDeviceID: p.RecipientDeviceID,
				ClientMessageID:   p.ClientMessageID,
				Ciphertext:        p.Ciphertext,
				Header:            p.Header,
				Signature:         p.Signature,
				Meta:              copyWithKey(p.Meta, "mls_group_id", groupID),
			})
		}
		return s.SendFanoutEnvelopes(ctx, senderID, senderDeviceID, fanoutRequest{
			RecipientAccountID: *recipientAccountID,
			Type:               envelopeTypeMlsWelcome,
			GroupID:            &groupID,
			ExpiresAt:          expiresAt,
			Payloads:           items,
		})
	}
	if len(payloads) == 0 {
		return nil, &ServiceError{"No payloads provided for all-fanout welcome."}
	}
	first := payloads[0]
	return s.FanoutMlsMessageToGroup(ctx, senderID, senderDeviceID, groupID,
		first.Ciphertext, first.Header, first.Signature, first.ClientMessageID,
		copyWithKey(first.Meta, "mls_group_id", groupID), envelopeTypeMlsWelcome)
}

// FanoutMlsCommit mirrors FanoutMlsCommitAsync (per-account payload fanout of
// the shared commit blob).
func (s *Service) FanoutMlsCommit(ctx context.Context, senderID, senderDeviceID, groupID string, ciphertext, header, signature []byte, clientMessageID *string, meta map[string]any) ([]E2eeEnvelope, error) {
	return s.fanoutMlsGroupMessage(ctx, senderID, senderDeviceID, groupID, ciphertext, header, signature, clientMessageID, meta, envelopeTypeMlsCommit)
}

// FanoutMlsMessageToGroup mirrors FanoutMlsMessageToGroupAsync.
func (s *Service) FanoutMlsMessageToGroup(ctx context.Context, senderID, senderDeviceID, groupID string, ciphertext, header, signature []byte, clientMessageID *string, meta map[string]any, envelopeType int) ([]E2eeEnvelope, error) {
	return s.fanoutMlsGroupMessage(ctx, senderID, senderDeviceID, groupID, ciphertext, header, signature, clientMessageID, meta, envelopeType)
}

func (s *Service) fanoutMlsGroupMessage(ctx context.Context, senderID, senderDeviceID, groupID string, ciphertext, header, signature []byte, clientMessageID *string, meta map[string]any, envelopeType int) ([]E2eeEnvelope, error) {
	memberships, err := s.store.ListMlsMembershipsByGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if len(memberships) == 0 {
		return nil, &ServiceError{"No devices found in group."}
	}
	grouped := map[string][]string{}
	var order []string
	for _, m := range memberships {
		if m.DeviceId == senderDeviceID {
			continue
		}
		if _, ok := grouped[m.AccountId]; !ok {
			order = append(order, m.AccountId)
		}
		grouped[m.AccountId] = append(grouped[m.AccountId], m.DeviceId)
	}

	var envelopes []E2eeEnvelope
	for _, accountID := range order {
		payloads := make([]DeviceCiphertextEnvelope, 0, len(grouped[accountID]))
		for _, deviceID := range grouped[accountID] {
			payloads = append(payloads, DeviceCiphertextEnvelope{
				RecipientDeviceID: deviceID,
				ClientMessageID:   clientMessageID,
				Ciphertext:        ciphertext,
				Header:            header,
				Signature:         signature,
				Meta:              copyWithKey(meta, "mls_group_id", groupID),
			})
		}
		result, err := s.SendFanoutEnvelopes(ctx, senderID, senderDeviceID, fanoutRequest{
			RecipientAccountID: accountID,
			Type:               envelopeType,
			GroupID:            &groupID,
			Payloads:           payloads,
		})
		if err != nil {
			return nil, err
		}
		envelopes = append(envelopes, result...)
	}
	return envelopes, nil
}

// SendFanoutEnvelopes mirrors SendFanoutEnvelopesAsync: validates device
// completeness for the recipient, stores one envelope per payload with a
// monotonic per-device sequence, then pushes the realtime notifications.
func (s *Service) SendFanoutEnvelopes(ctx context.Context, senderID, senderDeviceID string, req fanoutRequest) ([]E2eeEnvelope, error) {
	if senderDeviceID == "" {
		return nil, &ServiceError{"senderDeviceId cannot be empty."}
	}
	if len(req.Payloads) == 0 {
		return nil, &ServiceError{"payloads cannot be empty."}
	}
	if len(req.Payloads) > maxFanoutPayloadsPerRequest {
		return nil, &ServiceError{fmt.Sprintf("Too many payloads in one fanout request. Max allowed: %d.", maxFanoutPayloadsPerRequest)}
	}
	recipientExists, err := s.store.AccountExists(ctx, req.RecipientAccountID)
	if err != nil {
		return nil, err
	}
	if !recipientExists {
		return nil, &ServiceError{"Recipient not found."}
	}
	activeDevices, err := s.store.ListActiveE2eeDeviceIDs(ctx, req.RecipientAccountID)
	if err != nil {
		return nil, err
	}
	if len(activeDevices) == 0 {
		return nil, &ServiceError{"Recipient has no active E2EE devices."}
	}

	isMlsType := req.Type == envelopeTypeMlsWelcome || req.Type == envelopeTypeMlsCommit ||
		req.Type == envelopeTypeMlsApplication || req.Type == envelopeTypeMlsProposal ||
		req.Type == envelopeTypeControl

	if !isMlsType {
		payloadByDevice := make(map[string]struct{}, len(req.Payloads))
		for _, p := range req.Payloads {
			payloadByDevice[p.RecipientDeviceID] = struct{}{}
		}
		var missing []string
		for _, d := range activeDevices {
			if _, ok := payloadByDevice[d]; !ok {
				missing = append(missing, d)
			}
		}
		if len(missing) > 0 {
			return nil, &ServiceError{fmt.Sprintf("Missing ciphertext for recipient devices: %s", strings.Join(missing, ", "))}
		}
		var extra []string
		for _, p := range req.Payloads {
			if !contains(activeDevices, p.RecipientDeviceID) {
				extra = append(extra, p.RecipientDeviceID)
			}
		}
		extra = distinct(extra)
		if len(extra) > 0 {
			return nil, &ServiceError{fmt.Sprintf("Payload includes unknown/revoked devices: %s", strings.Join(extra, ", "))}
		}
	} else {
		var unknown []string
		for _, p := range req.Payloads {
			if !contains(activeDevices, p.RecipientDeviceID) {
				unknown = append(unknown, p.RecipientDeviceID)
			}
		}
		unknown = distinct(unknown)
		if len(unknown) > 0 {
			return nil, &ServiceError{fmt.Sprintf("Payload includes unknown/revoked devices: %s", strings.Join(unknown, ", "))}
		}
	}

	now := time.Now().UTC()
	var envelopes []store.E2eeEnvelope
	for _, p := range req.Payloads {
		env, err := s.createEnvelopeForTarget(ctx, senderID, senderDeviceID, req.RecipientAccountID,
			p.RecipientDeviceID, req.SessionID, req.Type, req.GroupID, p.ClientMessageID, p.Ciphertext,
			p.Header, p.Signature, req.ExpiresAt, p.Meta, false, now)
		if err != nil {
			return nil, err
		}
		envelopes = append(envelopes, *env)
	}

	if req.IncludeSenderCopy && req.RecipientAccountID != senderID {
		for _, p := range req.Payloads {
			if p.RecipientDeviceID == senderDeviceID {
				senderCopyMeta := copyWithKey(p.Meta, "sender_copy", true)
				var copyMessageID *string
				if p.ClientMessageID != nil {
					v := *p.ClientMessageID + ":self"
					copyMessageID = &v
				}
				env, err := s.createEnvelopeForTarget(ctx, senderID, senderDeviceID, senderID, senderDeviceID,
					req.SessionID, req.Type, req.GroupID, copyMessageID, p.Ciphertext, p.Header, p.Signature,
					req.ExpiresAt, senderCopyMeta, false, now)
				if err != nil {
					return nil, err
				}
				envelopes = append(envelopes, *env)
				break
			}
		}
	}

	if req.SessionID != nil {
		if err := s.store.TouchE2eeSession(ctx, *req.SessionID, now); err != nil {
			return nil, err
		}
	}

	for i := range envelopes {
		s.deliverEnvelope(ctx, &envelopes[i])
	}

	responses := make([]E2eeEnvelope, 0, len(envelopes))
	for i := range envelopes {
		responses = append(responses, *envelopeWire(&envelopes[i]))
	}
	return responses, nil
}

// createEnvelopeForTarget mirrors CreateEnvelopeForTargetAsync (dedupe on
// client_message_id + next monotonic sequence per recipient device).
func (s *Service) createEnvelopeForTarget(ctx context.Context, senderID, senderDeviceID, recipientAccountID, recipientDeviceID string, sessionID *string, envType int, groupID *string, clientMessageID *string, ciphertext, header, signature []byte, expiresAt *time.Time, meta map[string]any, legacyAccountScoped bool, createdAt time.Time) (*store.E2eeEnvelope, error) {
	if len(ciphertext) == 0 {
		return nil, &ServiceError{"Ciphertext cannot be empty."}
	}
	env := &store.E2eeEnvelope{
		Id:                  uuid.NewString(),
		SenderId:            senderID,
		SenderDeviceId:      &senderDeviceID,
		RecipientId:         recipientAccountID,
		RecipientAccountId:  recipientAccountID,
		RecipientDeviceId:   &recipientDeviceID,
		SessionId:           sessionID,
		Type:                envType,
		GroupId:             groupID,
		ClientMessageId:     clientMessageID,
		Ciphertext:          ciphertext,
		Header:              header,
		Signature:           signature,
		ExpiresAt:           expiresAt,
		LegacyAccountScoped: legacyAccountScoped,
		Meta:                meta,
		CreatedAt:           createdAt,
		UpdatedAt:           createdAt,
	}
	return s.store.InsertEnvelope(ctx, env)
}

// GetPendingEnvelopesByDevice mirrors GetPendingEnvelopesByDeviceAsync.
func (s *Service) GetPendingEnvelopesByDevice(ctx context.Context, accountID, deviceID string, take int) ([]E2eeEnvelope, error) {
	envelopes, err := s.store.GetPendingEnvelopesByDevice(ctx, accountID, deviceID, take, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	responses := make([]E2eeEnvelope, 0, len(envelopes))
	for i := range envelopes {
		responses = append(responses, *envelopeWire(&envelopes[i]))
	}
	return responses, nil
}

// AckEnvelopeByDevice mirrors AcknowledgeEnvelopeByDeviceAsync (nil when the
// device is inactive or the envelope is missing).
func (s *Service) AckEnvelopeByDevice(ctx context.Context, accountID, deviceID, envelopeID string) (*E2eeEnvelope, error) {
	env, err := s.store.AckEnvelopeByDevice(ctx, accountID, deviceID, envelopeID, time.Now().UTC())
	if err != nil || env == nil {
		return nil, err
	}
	return envelopeWire(env), nil
}

// RevokeDevice mirrors RevokeDeviceAsync (false when the device is missing).
func (s *Service) RevokeDevice(ctx context.Context, accountID, deviceID string) (bool, error) {
	now := time.Now().UTC()
	result, err := s.store.RevokeDevice(ctx, accountID, deviceID, now)
	if err != nil {
		return false, err
	}
	if !result.Found {
		return false, nil
	}
	if result.AlreadyRevoked {
		return true, nil
	}
	for i := range result.ControlEnvelopes {
		s.deliverEnvelope(ctx, &result.ControlEnvelopes[i])
	}
	s.logf().Info("revoked e2ee device",
		"account", accountID, "device", deviceID, "purged", result.PurgedCount)
	return true, nil
}

// --- Realtime push ---

// deliverEnvelope mirrors TryDeliverEnvelopeAsync: skip when the recipient
// websocket is disconnected (or the status check is unavailable), else push
// e2ee.envelope to the websocket_push stream and mark the envelope Delivered.
func (s *Service) deliverEnvelope(ctx context.Context, env *store.E2eeEnvelope) {
	if s.blade != nil {
		connected, err := s.checkWebsocketConnected(ctx, env)
		if err != nil {
			s.logf().Warn("websocket status check failed",
				"envelope", env.Id, "recipient", env.RecipientAccountId, "error", err)
			return
		}
		if !connected {
			return
		}
	}
	payload := envelopePushPayload{
		Id:                  env.Id,
		SenderId:            env.SenderId,
		SenderDeviceId:      env.SenderDeviceId,
		RecipientId:         env.RecipientId,
		RecipientAccountId:  env.RecipientAccountId,
		RecipientDeviceId:   env.RecipientDeviceId,
		SessionId:           env.SessionId,
		Type:                env.Type,
		GroupId:             env.GroupId,
		ClientMessageId:     env.ClientMessageId,
		Sequence:            env.Sequence,
		Ciphertext:          env.Ciphertext,
		Header:              env.Header,
		Signature:           env.Signature,
		Meta:                env.Meta,
		LegacyAccountScoped: env.LegacyAccountScoped,
		CreatedAt:           env.CreatedAt,
	}
	var err error
	if env.RecipientDeviceId != nil {
		err = s.events.PublishWS(ctx, *env.RecipientDeviceId, packetType, payload)
	} else {
		err = s.events.PublishWS(ctx, env.RecipientAccountId, packetType, payload)
	}
	if err != nil {
		s.logf().Warn("push realtime e2ee envelope",
			"envelope", env.Id, "recipient", env.RecipientAccountId, "error", err)
		return
	}
	if err := s.store.MarkEnvelopeDelivered(ctx, env.Id, time.Now().UTC()); err != nil {
		s.logf().Warn("mark envelope delivered", "envelope", env.Id, "error", err)
	}
}

// checkWebsocketConnected mirrors
// RemoteWebSocketService.GetWebsocketConnectionStatus (device-scoped for
// device envelopes, account-scoped otherwise).
func (s *Service) checkWebsocketConnected(ctx context.Context, env *store.E2eeEnvelope) (bool, error) {
	req := &gen.DyGetWebsocketConnectionStatusRequest{Namespace: wsNamespace}
	if env.RecipientDeviceId != nil {
		req.Id = &gen.DyGetWebsocketConnectionStatusRequest_DeviceId{DeviceId: *env.RecipientDeviceId}
	} else {
		req.Id = &gen.DyGetWebsocketConnectionStatusRequest_UserId{UserId: env.RecipientAccountId}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	resp, err := s.blade.GetWebsocketConnectionStatus(checkCtx, req)
	if err != nil {
		return false, err
	}
	return resp != nil && resp.IsConnected, nil
}

// --- Wire conversion helpers ---

func mlsKeyPackageWire(k *store.MlsKeyPackage) *MlsKeyPackage {
	return &MlsKeyPackage{
		Id:                  k.Id,
		AccountId:           k.AccountId,
		DeviceId:            k.DeviceId,
		DeviceLabel:         k.DeviceLabel,
		KeyPackage:          k.KeyPackage,
		Ciphersuite:         k.Ciphersuite,
		IsConsumed:          k.IsConsumed,
		ConsumedAt:          timePtr(k.ConsumedAt),
		ConsumedByAccountId: k.ConsumedByAccountId,
		Meta:                k.Meta,
		CreatedAt:           model.NewTime(k.CreatedAt),
		UpdatedAt:           model.NewTime(k.UpdatedAt),
		DeletedAt:           timePtr(k.DeletedAt),
	}
}

func mlsGroupStateWire(st *store.MlsGroupState) *MlsGroupState {
	return &MlsGroupState{
		Id:           st.Id,
		MlsGroupId:   st.MlsGroupId,
		Epoch:        st.Epoch,
		StateVersion: st.StateVersion,
		LastCommitAt: timePtr(st.LastCommitAt),
		GroupInfo:    st.GroupInfo,
		RatchetTree:  st.RatchetTree,
		Meta:         st.Meta,
		CreatedAt:    model.NewTime(st.CreatedAt),
		UpdatedAt:    model.NewTime(st.UpdatedAt),
		DeletedAt:    timePtr(st.DeletedAt),
	}
}

func mlsDeviceMembershipWire(m *store.MlsDeviceMembership) *MlsDeviceMembership {
	return &MlsDeviceMembership{
		Id:                     m.Id,
		MlsGroupId:             m.MlsGroupId,
		AccountId:              m.AccountId,
		DeviceId:               m.DeviceId,
		JoinedEpoch:            m.JoinedEpoch,
		LastSeenEpoch:          m.LastSeenEpoch,
		LastReshareRequiredAt:  timePtr(m.LastReshareRequiredAt),
		LastReshareCompletedAt: timePtr(m.LastReshareCompletedAt),
		CreatedAt:              model.NewTime(m.CreatedAt),
		UpdatedAt:              model.NewTime(m.UpdatedAt),
		DeletedAt:              timePtr(m.DeletedAt),
	}
}

func envelopeWire(e *store.E2eeEnvelope) *E2eeEnvelope {
	return &E2eeEnvelope{
		Id:                  e.Id,
		SenderId:            e.SenderId,
		SenderDeviceId:      e.SenderDeviceId,
		RecipientId:         e.RecipientId,
		RecipientAccountId:  e.RecipientAccountId,
		RecipientDeviceId:   e.RecipientDeviceId,
		SessionId:           e.SessionId,
		Type:                e.Type,
		GroupId:             e.GroupId,
		ClientMessageId:     e.ClientMessageId,
		Sequence:            e.Sequence,
		Ciphertext:          e.Ciphertext,
		Header:              e.Header,
		Signature:           e.Signature,
		DeliveryStatus:      e.DeliveryStatus,
		DeliveredAt:         timePtr(e.DeliveredAt),
		AckedAt:             timePtr(e.AckedAt),
		ExpiresAt:           timePtr(e.ExpiresAt),
		LegacyAccountScoped: e.LegacyAccountScoped,
		Meta:                e.Meta,
		CreatedAt:           model.NewTime(e.CreatedAt),
		UpdatedAt:           model.NewTime(e.UpdatedAt),
		DeletedAt:           timePtr(e.DeletedAt),
	}
}

func timePtr(t *time.Time) *model.Time {
	if t == nil {
		return nil
	}
	return model.NewTime(*t)
}

func copyWithKey(meta map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(meta)+1)
	for k, v := range meta {
		out[k] = v
	}
	out[key] = value
	return out
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func distinct(list []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, v := range list {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// --- Push payloads (the C# anonymous objects; snake_case with nulls included
// per InfraObjectCoder's JsonIgnoreCondition.Never) ---

type envelopePushPayload struct {
	Id                  string         `json:"id"`
	SenderId            string         `json:"sender_id"`
	SenderDeviceId      *string        `json:"sender_device_id"`
	RecipientId         string         `json:"recipient_id"`
	RecipientAccountId  string         `json:"recipient_account_id"`
	RecipientDeviceId   *string        `json:"recipient_device_id"`
	SessionId           *string        `json:"session_id"`
	Type                int            `json:"type"`
	GroupId             *string        `json:"group_id"`
	ClientMessageId     *string        `json:"client_message_id"`
	Sequence            int64          `json:"sequence"`
	Ciphertext          []byte         `json:"ciphertext"`
	Header              []byte         `json:"header"`
	Signature           []byte         `json:"signature"`
	Meta                map[string]any `json:"meta"`
	LegacyAccountScoped bool           `json:"legacy_account_scoped"`
	CreatedAt           time.Time      `json:"created_at"`
}

type kpDepletedPayload struct {
	MlsDeviceId    string  `json:"mls_device_id"`
	DeviceId       string  `json:"device_id"`
	DeviceLabel    *string `json:"device_label"`
	AvailableCount int     `json:"available_count"`
}

type groupResetPayload struct {
	Type      string  `json:"type"`
	GroupId   string  `json:"group_id"`
	Reason    *string `json:"reason"`
	Timestamp string  `json:"timestamp"`
}
