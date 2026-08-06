// DyMlsService port of Padlock E2EE/MlsServiceGrpc.cs — the last
// Padlock-hosted gRPC surface after the Stargate takeover of the
// auth/account/actionlog/permission/bot/authorized-app services. The C#
// fleet (Messager's RemoteMlsService) dials this via _grpc.stargate now
// that Padlock is retired.
package grpcserver

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/httpserver/e2eectl"
)

// SnE2eeEnvelopeType int values (DysonNetwork.Shared/Models/E2EE.cs). The
// proto Type field carries the C# enum NAME, so the wire mapping below is
// part of the contract.
const (
	envelopeTypePairwiseMessage       = 0
	envelopeTypeSenderKeyDistribution = 1
	envelopeTypeSenderKeyMessage      = 2
	envelopeTypeControl               = 3
	envelopeTypeMlsCommit             = 4
	envelopeTypeMlsWelcome            = 5
	envelopeTypeMlsApplication        = 6
	envelopeTypeMlsProposal           = 7
)

// envelopeTypeName maps the wire type int to the C# enum name
// (e.Type.ToString() in MlsServiceGrpc).
func envelopeTypeName(t int) string {
	switch t {
	case envelopeTypePairwiseMessage:
		return "PairwiseMessage"
	case envelopeTypeSenderKeyDistribution:
		return "SenderKeyDistribution"
	case envelopeTypeSenderKeyMessage:
		return "SenderKeyMessage"
	case envelopeTypeControl:
		return "Control"
	case envelopeTypeMlsCommit:
		return "MlsCommit"
	case envelopeTypeMlsWelcome:
		return "MlsWelcome"
	case envelopeTypeMlsApplication:
		return "MlsApplication"
	case envelopeTypeMlsProposal:
		return "MlsProposal"
	default:
		return "Unknown"
	}
}

type dyMlsService struct {
	gen.UnimplementedDyMlsServiceServer
	d Deps
}

func (s *dyMlsService) e2ee() (*e2eectl.Service, error) {
	if s.d.E2ee == nil {
		return nil, status.Error(codes.Unavailable, "MLS service is not configured")
	}
	return s.d.E2ee, nil
}

func mlsEnvelopeWire(e *e2eectl.E2eeEnvelope) *gen.MlsEnvelope {
	createdAt := int64(0)
	if e.CreatedAt != nil {
		createdAt = time.Time(*e.CreatedAt).UnixMilli()
	}
	out := &gen.MlsEnvelope{
		Id:                e.Id,
		SenderId:          e.SenderId,
		SenderDeviceId:    derefStr(e.SenderDeviceId),
		RecipientId:       e.RecipientId,
		RecipientDeviceId: derefStr(e.RecipientDeviceId),
		Type:              envelopeTypeName(e.Type),
		GroupId:           derefStr(e.GroupId),
		ClientMessageId:   derefStr(e.ClientMessageId),
		Sequence:          e.Sequence,
		Ciphertext:        e.Ciphertext,
		Header:            e.Header,
		Signature:         e.Signature,
		CreatedAtUnixMs:   createdAt,
	}
	return out
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// SendMlsMessage fans an MLS application message out to the whole group.
// The C# source builds a SendE2EeFanoutRequest with Guid.Empty as the
// recipient, which its SendFanoutEnvelopesAsync rejects ("Recipient not
// found") — the intended behavior (and the one the HTTP send-fanout route
// implements) is a group fanout, so that is what this mirrors.
func (s *dyMlsService) SendMlsMessage(ctx context.Context, req *gen.SendMlsMessageRequest) (*gen.SendMlsMessageResponse, error) {
	e2ee, err := s.e2ee()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GroupId) == "" {
		return nil, status.Error(codes.InvalidArgument, "GroupId is required")
	}
	meta := make(map[string]any, len(req.Meta))
	for k, v := range req.Meta {
		meta[k] = v
	}
	var clientMessageID *string
	if req.ClientMessageId != "" {
		clientMessageID = &req.ClientMessageId
	}
	envelopes, err := e2ee.FanoutMlsMessageToGroup(ctx, "", "", req.GroupId,
		req.Ciphertext, req.Header, req.Signature, clientMessageID, meta, envelopeTypeMlsApplication)
	if err != nil {
		return nil, err
	}
	resp := &gen.SendMlsMessageResponse{Envelopes: make([]*gen.MlsEnvelope, 0, len(envelopes))}
	for i := range envelopes {
		resp.Envelopes = append(resp.Envelopes, mlsEnvelopeWire(&envelopes[i]))
	}
	return resp, nil
}

// GetGroupInfo mirrors MlsServiceGrpc.GetGroupInfo.
func (s *dyMlsService) GetGroupInfo(ctx context.Context, req *gen.GetMlsGroupInfoRequest) (*gen.GetMlsGroupInfoResponse, error) {
	e2ee, err := s.e2ee()
	if err != nil {
		return nil, err
	}
	state, err := e2ee.GetGroupState(ctx, req.GroupId)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, status.Errorf(codes.NotFound, "Group %s not found", req.GroupId)
	}
	return &gen.GetMlsGroupInfoResponse{
		GroupId:     state.MlsGroupId,
		Epoch:       state.Epoch,
		GroupInfo:   state.GroupInfo,
		RatchetTree: state.RatchetTree,
	}, nil
}

// UploadGroupInfo mirrors MlsServiceGrpc.UploadGroupInfo. The C# gRPC call
// passes no expected epoch, so the epoch guard is disabled (create with
// epoch 0 when the group is absent, else overwrite).
func (s *dyMlsService) UploadGroupInfo(ctx context.Context, req *gen.UploadGroupInfoRequest) (*gen.UploadGroupInfoResponse, error) {
	if s.d.Store == nil {
		return nil, status.Error(codes.Unavailable, "store is not configured")
	}
	result, err := s.d.Store.UploadGroupInfo(ctx, req.GroupId, req.GroupInfo, req.RatchetTree, nil, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return &gen.UploadGroupInfoResponse{
		Success: result.Success,
		GroupId: result.GroupID,
		Epoch:   result.Epoch,
	}, nil
}

// JoinGroupExternal mirrors MlsServiceGrpc.JoinGroupExternal: mark the
// sender device reshare-required (external join without invitation).
func (s *dyMlsService) JoinGroupExternal(ctx context.Context, req *gen.JoinMlsGroupExternalRequest) (*gen.JoinMlsGroupExternalResponse, error) {
	e2ee, err := s.e2ee()
	if err != nil {
		return nil, err
	}
	membership, err := e2ee.MarkMlsReshareRequired(ctx, req.GroupId, "", req.SenderDeviceId, req.ExpectedEpoch, "external_join")
	if err != nil {
		return &gen.JoinMlsGroupExternalResponse{Success: false, GroupId: req.GroupId, Error: err.Error()}, nil
	}
	newEpoch := req.ExpectedEpoch
	if membership != nil && membership.LastSeenEpoch != nil {
		newEpoch = *membership.LastSeenEpoch
	}
	return &gen.JoinMlsGroupExternalResponse{Success: true, GroupId: req.GroupId, NewEpoch: newEpoch}, nil
}

// CommitGroupChanges mirrors MlsServiceGrpc.CommitGroupChanges.
func (s *dyMlsService) CommitGroupChanges(ctx context.Context, req *gen.CommitGroupChangesRequest) (*gen.CommitGroupChangesResponse, error) {
	e2ee, err := s.e2ee()
	if err != nil {
		return nil, err
	}
	state, err := e2ee.CommitMlsGroup(ctx, "", req.GroupId, req.ExpectedEpoch, req.Reason, nil)
	if err != nil {
		return &gen.CommitGroupChangesResponse{Success: false, GroupId: req.GroupId, Error: err.Error()}, nil
	}
	if state == nil {
		return &gen.CommitGroupChangesResponse{Success: false, GroupId: req.GroupId, Error: "Failed to commit MLS group: state is null"}, nil
	}
	return &gen.CommitGroupChangesResponse{Success: true, GroupId: req.GroupId, NewEpoch: state.Epoch}, nil
}

// PublishWelcome mirrors MlsServiceGrpc.PublishWelcome: fan out the welcome
// envelope(s) and echo the submitted epoch.
func (s *dyMlsService) PublishWelcome(ctx context.Context, req *gen.PublishWelcomeRequest) (*gen.PublishWelcomeResponse, error) {
	e2ee, err := s.e2ee()
	if err != nil {
		return nil, err
	}
	payloads := make([]e2eectl.DeviceCiphertextEnvelope, 0, len(req.Recipients))
	for _, r := range req.Recipients {
		payloads = append(payloads, e2eectl.DeviceCiphertextEnvelope{
			RecipientDeviceID: r.DeviceId,
			Ciphertext:        r.EncryptedWelcome,
		})
	}
	if _, err := e2ee.FanoutMlsWelcome(ctx, "", req.SenderDeviceId, req.GroupId, nil, nil, payloads); err != nil {
		return nil, err
	}
	return &gen.PublishWelcomeResponse{Epoch: req.Epoch}, nil
}

// GetKeyPackages mirrors MlsServiceGrpc.GetKeyPackages (non-consuming).
func (s *dyMlsService) GetKeyPackages(ctx context.Context, req *gen.GetMlsKeyPackagesRequest) (*gen.GetMlsKeyPackagesResponse, error) {
	e2ee, err := s.e2ee()
	if err != nil {
		return nil, err
	}
	resp := &gen.GetMlsKeyPackagesResponse{}
	for _, device := range req.Devices {
		if device == nil || strings.TrimSpace(device.AccountId) == "" {
			continue
		}
		packages, err := e2ee.ListMlsDeviceKeyPackages(ctx, device.AccountId, nil, false)
		if err != nil {
			return nil, err
		}
		for _, pkg := range packages {
			resp.Results = append(resp.Results, &gen.KeyPackageResult{
				AccountId:   pkg.AccountId,
				DeviceId:    pkg.DeviceId,
				KeyPackage:  pkg.KeyPackage,
				Ciphersuite: pkg.Ciphersuite,
			})
		}
	}
	return resp, nil
}

// MarkReshareRequired mirrors MlsServiceGrpc.MarkReshareRequired.
func (s *dyMlsService) MarkReshareRequired(ctx context.Context, req *gen.MarkReshareRequiredRequest) (*gen.MarkReshareRequiredResponse, error) {
	e2ee, err := s.e2ee()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.TargetAccountId) == "" {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID")
	}
	if _, err := e2ee.MarkMlsReshareRequired(ctx, req.GroupId, req.TargetAccountId, req.TargetDeviceId, req.Epoch, req.Reason); err != nil {
		return nil, err
	}
	return &gen.MarkReshareRequiredResponse{Success: true}, nil
}

// GetGroupState mirrors MlsServiceGrpc.GetGroupState.
func (s *dyMlsService) GetGroupState(ctx context.Context, req *gen.GetMlsGroupStateRequest) (*gen.GetMlsGroupStateResponse, error) {
	e2ee, err := s.e2ee()
	if err != nil {
		return nil, err
	}
	state, err := e2ee.GetGroupState(ctx, req.GroupId)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, status.Errorf(codes.NotFound, "Group %s not found", req.GroupId)
	}
	lastCommitAt := int64(0)
	if state.LastCommitAt != nil {
		lastCommitAt = time.Time(*state.LastCommitAt).UnixMilli()
	}
	return &gen.GetMlsGroupStateResponse{
		GroupId:           state.MlsGroupId,
		Epoch:             state.Epoch,
		StateVersion:      state.StateVersion,
		LastCommitAtUnixMs: lastCommitAt,
	}, nil
}

// DeleteGroup mirrors MlsServiceGrpc.DeleteGroup (soft delete).
func (s *dyMlsService) DeleteGroup(ctx context.Context, req *gen.DeleteMlsGroupRequest) (*gen.DeleteMlsGroupResponse, error) {
	if s.d.Store == nil {
		return nil, status.Error(codes.Unavailable, "store is not configured")
	}
	deleted, err := s.d.Store.DeleteMlsGroup(ctx, req.GroupId, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return &gen.DeleteMlsGroupResponse{Success: true, DeletedStateCount: int32(deleted)}, nil
}

// AddMlsDeviceMembership mirrors MlsServiceGrpc.AddMlsDeviceMembership.
func (s *dyMlsService) AddMlsDeviceMembership(ctx context.Context, req *gen.AddMlsDeviceMembershipRequest) (*gen.AddMlsDeviceMembershipResponse, error) {
	e2ee, err := s.e2ee()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.AccountId) == "" {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID")
	}
	membership, err := e2ee.AddMlsDeviceMembership(ctx, req.AccountId, req.DeviceId, req.GroupId, req.Epoch)
	if err != nil {
		return nil, err
	}
	epoch := membership.JoinedEpoch
	if membership.LastSeenEpoch != nil {
		epoch = *membership.LastSeenEpoch
	}
	return &gen.AddMlsDeviceMembershipResponse{
		Success:  true,
		GroupId:  membership.MlsGroupId,
		DeviceId: membership.DeviceId,
		Epoch:    epoch,
	}, nil
}
