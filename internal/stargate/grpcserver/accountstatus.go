// Account-status RPCs of DyProfileService, ported from Passport's
// AccountServiceGrpc.GetAccountStatus/GetAccountStatusBatch. They are the read
// half of the presence surface the C# fleet depends on: Sphere profiles,
// Messager contact rows and Realm member lists all read a member's status
// through these two calls.
//
// This file deliberately does not live in profile.go: profile.go's
// dyProfileService delegates here. Without a configured presence resolver the
// two methods answer Unimplemented.
package grpcserver

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/httpserver/statusctl"
	"src.solsynth.dev/sosys/stargate/model"
)

// accountStatusRPCs serves the two DyProfileService account-status methods.
type accountStatusRPCs struct {
	d Deps
}

func newAccountStatusRPCs(d Deps) *accountStatusRPCs {
	return &accountStatusRPCs{d: d}
}

// available reports whether the status read path is configured.
func (r *accountStatusRPCs) available() bool {
	return r != nil && r.d.Store != nil && r.d.PresenceResolver != nil
}

// unimplemented mirrors the embedded UnimplementedDyProfileServiceServer
// message so a disabled module is indistinguishable from an unported method.
func (r *accountStatusRPCs) unimplemented(method string) error {
	return status.Errorf(codes.Unimplemented, "method %s not implemented", method)
}

// loader assembles statuses through the same code path the HTTP surface uses,
// so the gRPC and REST reads cannot drift. No viewer is attached, exactly like
// the C# calls: GetStatus(accountId) passes viewerId = null, which hides the
// device list while still reporting IsOnline.
func (r *accountStatusRPCs) loader() statusctl.Loader {
	return statusctl.Loader{Store: r.d.Store, Presence: r.d.PresenceResolver}
}

// GetAccountStatus mirrors AccountServiceGrpc.GetAccountStatus. The C# parses
// the id with Guid.Parse; Stargate answers InvalidArgument instead of a 500.
func (r *accountStatusRPCs) GetAccountStatus(ctx context.Context, req *gen.DyGetAccountRequest) (*gen.DyAccountStatus, error) {
	if !r.available() {
		return nil, r.unimplemented("GetAccountStatus")
	}
	accountID, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	st, err := r.loader().Status(ctx, accountID, nil)
	if err != nil {
		return nil, err
	}
	return accountStatusToProto(st), nil
}

// GetAccountStatusBatch mirrors AccountServiceGrpc.GetAccountStatusBatch: an
// unparsable id is skipped rather than failing the batch (the C# filters with
// Guid.TryParse) and every resolvable account contributes one status.
func (r *accountStatusRPCs) GetAccountStatusBatch(ctx context.Context, req *gen.DyGetAccountBatchRequest) (*gen.DyGetAccountStatusBatchResponse, error) {
	if !r.available() {
		return nil, r.unimplemented("GetAccountStatusBatch")
	}
	accountIDs := make([]uuid.UUID, 0, len(req.GetId()))
	for _, raw := range req.GetId() {
		if id, err := uuid.Parse(raw); err == nil {
			accountIDs = append(accountIDs, id)
		}
	}
	statuses, err := r.loader().Statuses(ctx, accountIDs, nil)
	if err != nil {
		return nil, err
	}
	resp := &gen.DyGetAccountStatusBatchResponse{Statuses: make([]*gen.DyAccountStatus, 0, len(statuses))}
	for _, id := range accountIDs {
		if st, ok := statuses[id]; ok {
			resp.Statuses = append(resp.Statuses, accountStatusToProto(st))
		}
	}
	return resp, nil
}

// accountStatusToProto mirrors SnAccountStatus.ToProtoValue field for field.
func accountStatusToProto(st *model.AccountStatus) *gen.DyAccountStatus {
	if st == nil {
		return nil
	}
	devices := make([]*gen.DyOnlineDevice, 0, len(st.OnlineDevices))
	for _, device := range st.OnlineDevices {
		devices = append(devices, onlineDeviceFromStatus(device))
	}
	return &gen.DyAccountStatus{
		Id:            st.Id,
		Attitude:      statusAttitudeToProto(st.Attitude),
		IsOnline:      st.IsOnline,
		OnlineDevices: devices,
		IsIdle:        st.IsIdle,
		IsCustomized:  st.IsCustomized,
		IsInvisible:   st.Type == model.StatusTypeInvisible,
		IsNotDisturb:  st.Type == model.StatusTypeDoNotDisturb,
		Label:         st.Label,
		Type:          gen.DyAccountStatusType(st.Type),
		Symbol:        st.Symbol,
		Meta:          statusMetaBytes(st.Meta),
		ClearedAt:     toProtoTime(st.ClearedAt),
		AccountId:     st.AccountId,
		AppIdentifier: st.AppIdentifier,
		IsAutomated:   st.IsAutomated,
		CreatedAt:     toProtoTime(st.CreatedAt),
		UpdatedAt:     toProtoTime(st.UpdatedAt),
		DeletedAt:     toProtoTime(st.DeletedAt),
		IdleSince:     toProtoTime(st.IdleSince),
	}
}

// statusAttitudeToProto maps StatusAttitude onto the proto enum. The proto
// enum carries an UNSPECIFIED prefix, so a plain cast would shift every value.
func statusAttitudeToProto(attitude model.StatusAttitude) gen.DyStatusAttitude {
	switch attitude {
	case model.StatusAttitudePositive:
		return gen.DyStatusAttitude_DY_POSITIVE
	case model.StatusAttitudeNegative:
		return gen.DyStatusAttitude_DY_NEGATIVE
	case model.StatusAttitudeNeutral:
		return gen.DyStatusAttitude_DY_NEUTRAL
	default:
		return gen.DyStatusAttitude_DY_STATUS_ATTITUDE_UNSPECIFIED
	}
}

// onlineDeviceFromStatus converts one device of a status read. The device list
// only exists as a wire shape (the C# SnOnlineDevice is [NotMapped]).
func onlineDeviceFromStatus(device model.OnlineDevice) *gen.DyOnlineDevice {
	proto := &gen.DyOnlineDevice{
		Id:         device.Id,
		DeviceId:   device.DeviceId,
		DeviceName: device.DeviceName,
		Platform:   clientPlatformToProto(model.ClientPlatform(device.Platform)),
	}
	if device.DeviceLabel != nil {
		proto.DeviceLabel = device.DeviceLabel
	}
	if device.LastGrantedAt != nil {
		proto.LastGrantedAt = toProtoTime(device.LastGrantedAt)
	}
	return proto
}

// statusMetaBytes mirrors InfraObjectCoder.ConvertObjectToByteString: the JSON
// encoding of the status metadata dictionary (nil encodes as "null").
func statusMetaBytes(meta map[string]any) []byte {
	raw, err := json.Marshal(meta)
	if err != nil {
		return []byte("null")
	}
	return raw
}
