// Proto<->model converters for the inbound gRPC servers. The model->proto
// directions for Account/Profile/Contact/Session are the shared converters in
// internal/auth/proto.go; this file holds the conversions the shared file
// does not cover (auth factors, connections, api keys, action logs) plus the
// proto->model directions used by the bot account receiver (ports of
// SnAccount.FromProtoValue / SnAccountProfile.FromProtoValue).
package grpcserver

import (
	"encoding/json"
	"errors"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

func toProtoTime(t *model.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(t.Time())
}

func tsToModel(ts *timestamppb.Timestamp) *model.Time {
	if ts == nil {
		return nil
	}
	return model.NewTime(ts.AsTime())
}

func wrapperToStr(w *wrapperspb.StringValue) *string {
	if w == nil {
		return nil
	}
	v := w.Value
	return &v
}

// authFactorToProto mirrors AccountServiceGrpc.ToProtoAuthFactor.
func authFactorToProto(f *model.AuthFactor) *gen.DyAccountAuthFactor {
	proto := &gen.DyAccountAuthFactor{
		Id:          f.Id,
		Type:        authFactorTypeToProto(f.Type),
		Trustworthy: int32(f.Trustworthy),
		EnabledAt:   toProtoTime(f.EnabledAt),
		ExpiredAt:   toProtoTime(f.ExpiredAt),
		AccountId:   f.AccountId,
		CreatedAt:   toProtoTime(f.CreatedAt),
		UpdatedAt:   toProtoTime(f.UpdatedAt),
	}
	if len(f.Config) > 0 {
		proto.Config = anyMetaToProto(f.Config)
	}
	if len(f.CreatedResponse) > 0 {
		proto.CreatedResponse = anyMetaToProto(f.CreatedResponse)
	}
	return proto
}

func authFactorTypeToProto(t model.AuthFactorType) gen.DyAccountAuthFactorType {
	switch t {
	case model.AuthFactorTypePassword:
		return gen.DyAccountAuthFactorType_DY_PASSWORD
	case model.AuthFactorTypeEmailCode:
		return gen.DyAccountAuthFactorType_DY_EMAIL_CODE
	case model.AuthFactorTypeInAppCode:
		return gen.DyAccountAuthFactorType_DY_IN_APP_CODE
	case model.AuthFactorTypeTimedCode:
		return gen.DyAccountAuthFactorType_DY_TIMED_CODE
	case model.AuthFactorTypePinCode:
		return gen.DyAccountAuthFactorType_DY_PIN_CODE
	case model.AuthFactorTypePasskey:
		return gen.DyAccountAuthFactorType_DY_PASSKEY
	default:
		return gen.DyAccountAuthFactorType_DY_AUTH_FACTOR_TYPE_UNSPECIFIED
	}
}

// connectionToProto mirrors AccountServiceGrpc.ToProtoConnection.
func connectionToProto(c *model.Connection) *gen.DyAccountConnection {
	proto := &gen.DyAccountConnection{
		Id:                 c.Id,
		Provider:           c.Provider,
		ProvidedIdentifier: c.ProvidedIdentifier,
		LastUsedAt:         toProtoTime(c.LastUsedAt),
		IsPublic:           c.IsPublic,
		AccountId:          c.AccountId,
		CreatedAt:          toProtoTime(c.CreatedAt),
		UpdatedAt:          toProtoTime(c.UpdatedAt),
	}
	if c.AccessToken != "" {
		proto.AccessToken = wrapperspb.String(c.AccessToken)
	}
	if c.RefreshToken != "" {
		proto.RefreshToken = wrapperspb.String(c.RefreshToken)
	}
	if len(c.Meta) > 0 {
		proto.Meta = anyMetaToProto(c.Meta)
	}
	return proto
}

// apiKeyToProto mirrors SnApiKey.ToProtoValue (BotAccountReceiverGrpc).
func apiKeyToProto(k *model.ApiKey) *gen.DyApiKey {
	proto := &gen.DyApiKey{
		Id:        k.Id,
		Label:     k.Label,
		AccountId: k.AccountId,
		SessionId: k.SessionId,
		CreatedAt: toProtoTime(k.CreatedAt),
		UpdatedAt: toProtoTime(k.UpdatedAt),
	}
	if k.Key != nil {
		proto.Key = wrapperspb.String(*k.Key)
	}
	return proto
}

// actionLogToProto mirrors SnActionLog.ToProtoValue (ActionLogServiceGrpc).
func actionLogToProto(l *model.ActionLog) *gen.DyActionLog {
	proto := &gen.DyActionLog{
		Id:        l.Id,
		Action:    l.Action,
		AccountId: l.AccountId,
		CreatedAt: toProtoTime(l.CreatedAt),
	}
	if len(l.Meta) > 0 {
		proto.Meta = anyMetaToProto(l.Meta)
	}
	if l.UserAgent != nil {
		proto.UserAgent = wrapperspb.String(*l.UserAgent)
	}
	if l.IpAddress != nil {
		proto.IpAddress = wrapperspb.String(*l.IpAddress)
	}
	if l.Location != nil {
		if b, err := json.Marshal(l.Location); err == nil {
			proto.Location = wrapperspb.String(string(b))
		}
	}
	if l.SessionId != nil {
		proto.SessionId = wrapperspb.String(*l.SessionId)
	}
	return proto
}

// accountFromProto mirrors SnAccount.FromProtoValue (BotAccountReceiverGrpc
// CreateBotAccount/UpdateBotAccount).
func accountFromProto(p *gen.DyAccount) *model.Account {
	account := &model.Account{
		Id:          p.Id,
		Name:        p.Name,
		Nick:        p.Nick,
		Language:    p.Language,
		Region:      p.Region,
		IsSuperuser: p.IsSuperuser,
		CreatedAt:   tsToModel(p.CreatedAt),
		UpdatedAt:   tsToModel(p.UpdatedAt),
	}
	if p.ActivatedAt != nil {
		account.ActivatedAt = tsToModel(p.ActivatedAt)
	}
	if p.AutomatedId != nil {
		v := p.AutomatedId.Value
		account.AutomatedId = &v
	}
	if p.PerkSubscription != nil && p.PerkSubscription.Id != "" {
		sub := p.PerkSubscription
		account.PerkSubscription = &model.SnSubscriptionReferenceObject{
			Id:          sub.Id,
			Identifier:  sub.Identifier,
			DisplayName: sub.DisplayName,
			PerkLevel:   int(sub.PerkLevel),
			IsActive:    sub.IsActive,
			IsAvailable: sub.IsAvailable,
			IsFreeTrial: sub.IsFreeTrial,
			Status:      int(sub.Status),
			BegunAt:     tsToModel(sub.BegunAt),
			EndedAt:     tsToModel(sub.EndedAt),
			RenewalAt:   tsToModel(sub.RenewalAt),
			BasePrice:   sub.BasePrice,
			FinalPrice:  sub.FinalPrice,
			AccountId:   sub.AccountId,
			CreatedAt:   tsToModel(sub.CreatedAt),
			UpdatedAt:   tsToModel(sub.UpdatedAt),
		}
		account.PerkLevel = int(sub.PerkLevel)
	}
	if p.Profile != nil && (p.Profile.Id != "" || p.Profile.AccountId != "") {
		account.Profile = profileFromProto(p.Profile)
	}
	return account
}

// profileFromProto mirrors SnAccountProfile.FromProtoValue.
func profileFromProto(p *gen.DyAccountProfile) *model.Profile {
	profile := &model.Profile{
		Id:               p.Id,
		FirstName:        wrapperToStr(p.FirstName),
		MiddleName:       wrapperToStr(p.MiddleName),
		LastName:         wrapperToStr(p.LastName),
		Bio:              wrapperToStr(p.Bio),
		Gender:           wrapperToStr(p.Gender),
		Pronouns:         wrapperToStr(p.Pronouns),
		TimeZone:         wrapperToStr(p.TimeZone),
		Location:         wrapperToStr(p.Location),
		Birthday:         tsToModel(p.Birthday),
		LastSeenAt:       tsToModel(p.LastSeenAt),
		Experience:       int(p.Experience),
		Level:            int(p.Level),
		LevelingProgress: p.LevelingProgress,
		SocialCredits:    p.SocialCredits,
		AccountId:        p.AccountId,
		CreatedAt:        tsToModel(p.CreatedAt),
		UpdatedAt:        tsToModel(p.UpdatedAt),
	}
	for _, l := range p.Links {
		profile.Links = append(profile.Links, model.Link{Name: l.Name, Url: l.Url})
	}
	if p.UsernameColor != nil {
		profile.UsernameColor = &model.UsernameColor{
			Type:      p.UsernameColor.Type,
			Value:     wrapperToStr(p.UsernameColor.Value),
			Direction: wrapperToStr(p.UsernameColor.Direction),
			Colors:    p.UsernameColor.Colors,
		}
	}
	if p.Verification != nil {
		profile.Verification = &model.SnVerificationMark{
			Type:        int(p.Verification.Type),
			Title:       strPtrOrNil(p.Verification.Title),
			Description: strPtrOrNil(p.Verification.Description),
			VerifiedBy:  strPtrOrNil(p.Verification.VerifiedBy),
		}
	}
	if p.Picture != nil {
		profile.Picture = cloudFileToRef(p.Picture)
	}
	if p.Background != nil {
		profile.Background = cloudFileToRef(p.Background)
	}
	return profile
}

func cloudFileToRef(f *gen.DyCloudFile) *model.SnCloudFileReferenceObject {
	ref := &model.SnCloudFileReferenceObject{
		Id:       f.Id,
		Url:      f.Url,
		MimeType: f.MimeType,
		Size:     &f.Size,
	}
	if f.Blurhash != nil {
		ref.Blurhash = *f.Blurhash
	}
	if f.Width != nil {
		w := int64(*f.Width)
		ref.Width = &w
	}
	if f.Height != nil {
		h := int64(*f.Height)
		ref.Height = &h
	}
	return ref
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// protoMetaToAny mirrors InfraObjectCoder.ConvertValueToObject (the string
// stays a string; structs become maps, lists become slices). Used for the
// CreateActionLog request meta (null entries dropped by the caller).
func protoMetaToAny(meta map[string]*structpb.Value) map[string]any {
	result := make(map[string]any, len(meta))
	for k, v := range meta {
		if v == nil {
			continue
		}
		result[k] = v.AsInterface()
	}
	return result
}

// anyMetaToProto mirrors InfraObjectCoder.ConvertToValueMap: strings, numbers,
// bools and nulls map to their Value kinds; anything else is JSON-serialized
// into a string value.
func anyMetaToProto(meta map[string]any) map[string]*structpb.Value {
	result := make(map[string]*structpb.Value, len(meta))
	for k, v := range meta {
		result[k] = anyToProtoValue(v)
	}
	return result
}

func anyToProtoValue(v any) *structpb.Value {
	switch t := v.(type) {
	case string:
		return structpb.NewStringValue(t)
	case int:
		return structpb.NewNumberValue(float64(t))
	case int32:
		return structpb.NewNumberValue(float64(t))
	case int64:
		return structpb.NewNumberValue(float64(t))
	case float32:
		return structpb.NewNumberValue(float64(t))
	case float64:
		return structpb.NewNumberValue(t)
	case bool:
		return structpb.NewBoolValue(t)
	case nil:
		return structpb.NewNullValue()
	default:
		if b, err := json.Marshal(v); err == nil {
			return structpb.NewStringValue(string(b))
		}
		return structpb.NewNullValue()
	}
}

// jsonToProtoValue parses raw JSON into a structpb.Value
// (the Go equivalent of Value.Parser.ParseJson).
func jsonToProtoValue(raw []byte) (*structpb.Value, error) {
	v := &structpb.Value{}
	if err := protojson.Unmarshal(raw, v); err != nil {
		return nil, err
	}
	return v, nil
}

// protoValueToJSON serializes a structpb.Value to its compact JSON form
// (the Go equivalent of Value.ToString()).
func protoValueToJSON(v *structpb.Value) ([]byte, error) {
	if v == nil {
		return nil, errors.New("permission value is required")
	}
	return protojson.Marshal(v)
}

func timeNow() time.Time { return time.Now().UTC() }
