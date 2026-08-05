package auth

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Converters from the internal model to the Golaunch proto shapes used for
// gRPC responses and the shared Redis session cache (protojson, snake_case,
// EmitUnpopulated — the wire contract downstream Go services deserialize).

func toProtoTime(t *model.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(t.Time())
}

func toProtoStr(v *string) *wrapperspb.StringValue {
	if v == nil {
		return nil
	}
	return wrapperspb.String(*v)
}

// AccountToProto converts an account to DyAccount.
func AccountToProto(a *model.Account) *gen.DyAccount {
	if a == nil {
		return nil
	}
	proto := &gen.DyAccount{
		Id:          a.Id,
		Name:        a.Name,
		Nick:        a.Nick,
		Language:    a.Language,
		Region:      a.Region,
		ActivatedAt: toProtoTime(a.ActivatedAt),
		IsSuperuser: a.IsSuperuser,
		Profile:     ProfileToProto(a.Profile),
		CreatedAt:   toProtoTime(a.CreatedAt),
		UpdatedAt:   toProtoTime(a.UpdatedAt),
	}
	if a.AutomatedId != nil {
		proto.AutomatedId = wrapperspb.String(*a.AutomatedId)
	}
	if a.PerkSubscription != nil {
		sub := a.PerkSubscription
		proto.PerkSubscription = &gen.DySubscriptionReferenceObject{
			Id:          sub.Id,
			Identifier:  sub.Identifier,
			DisplayName: sub.DisplayName,
			PerkLevel:   int32(sub.PerkLevel),
			BegunAt:     toProtoTime(sub.BegunAt),
			EndedAt:     toProtoTime(sub.EndedAt),
			RenewalAt:   toProtoTime(sub.RenewalAt),
			IsActive:    sub.IsActive,
			IsAvailable: sub.IsAvailable,
			IsFreeTrial: sub.IsFreeTrial,
			Status:      gen.DySubscriptionStatus(sub.Status),
			AccountId:   sub.AccountId,
			CreatedAt:   toProtoTime(sub.CreatedAt),
		}
		perkLevel := int32(sub.PerkLevel)
		proto.PerkLevel = &perkLevel
	}
	for i := range a.Contacts {
		proto.Contacts = append(proto.Contacts, ContactToProto(&a.Contacts[i]))
	}
	return proto
}

// ProfileToProto converts a profile to DyAccountProfile.
func ProfileToProto(p *model.Profile) *gen.DyAccountProfile {
	if p == nil {
		return nil
	}
	proto := &gen.DyAccountProfile{
		Id:                 p.Id,
		FirstName:          toProtoStr(p.FirstName),
		MiddleName:         toProtoStr(p.MiddleName),
		LastName:           toProtoStr(p.LastName),
		Bio:                toProtoStr(p.Bio),
		Gender:             toProtoStr(p.Gender),
		Pronouns:           toProtoStr(p.Pronouns),
		TimeZone:           toProtoStr(p.TimeZone),
		Location:           toProtoStr(p.Location),
		Birthday:           toProtoTime(p.Birthday),
		LastSeenAt:         toProtoTime(p.LastSeenAt),
		Experience:         int32(p.Experience),
		Level:              int32(p.Level),
		LevelingProgress:   p.LevelingProgress,
		SocialCredits:      p.SocialCredits,
		SocialCreditsLevel: int32(p.SocialCreditsLevel),
		AccountId:          p.AccountId,
		CreatedAt:          toProtoTime(p.CreatedAt),
		UpdatedAt:          toProtoTime(p.UpdatedAt),
	}
	for i := range p.Links {
		proto.Links = append(proto.Links, &gen.DyProfileLink{
			Name: p.Links[i].Name,
			Url:  p.Links[i].Url,
		})
	}
	if p.UsernameColor != nil {
		proto.UsernameColor = &gen.DyUsernameColor{
			Type:      p.UsernameColor.Type,
			Value:     toProtoStr(p.UsernameColor.Value),
			Direction: toProtoStr(p.UsernameColor.Direction),
			Colors:    p.UsernameColor.Colors,
		}
	}
	if p.Verification != nil {
		proto.Verification = &gen.DyVerificationMark{
			Type:        gen.DyVerificationMarkType(p.Verification.Type),
			Title:       derefOrEmpty(p.Verification.Title),
			Description: derefOrEmpty(p.Verification.Description),
			VerifiedBy:  derefOrEmpty(p.Verification.VerifiedBy),
		}
	}
	return proto
}

// ContactToProto converts a contact to DyAccountContact.
func ContactToProto(c *model.Contact) *gen.DyAccountContact {
	if c == nil {
		return nil
	}
	return &gen.DyAccountContact{
		Id:         c.Id,
		Type:       contactTypeToProto(c.Type),
		Content:    c.Content,
		IsPrimary:  c.IsPrimary,
		IsPublic:   c.IsPublic,
		VerifiedAt: toProtoTime(c.VerifiedAt),
		AccountId:  c.AccountId,
		CreatedAt:  toProtoTime(c.CreatedAt),
		UpdatedAt:  toProtoTime(c.UpdatedAt),
	}
}

func contactTypeToProto(t int) gen.DyAccountContactType {
	switch model.ContactType(t) {
	case model.ContactTypeEmail:
		return gen.DyAccountContactType_DY_EMAIL
	case model.ContactTypePhoneNumber:
		return gen.DyAccountContactType_DY_PHONE_NUMBER
	case model.ContactTypeAddress:
		return gen.DyAccountContactType_DY_ADDRESS
	default:
		return gen.DyAccountContactType_DY_EMAIL
	}
}

// SessionToProto converts a session (with its Account populated) to DyAuthSession.
func SessionToProto(s *model.AuthSession) *gen.DyAuthSession {
	if s == nil {
		return nil
	}
	proto := &gen.DyAuthSession{
		Id:            s.Id,
		LastGrantedAt: toProtoTime(s.LastGrantedAt),
		ExpiredAt:     toProtoTime(s.ExpiredAt),
		AccountId:     s.AccountId,
		Account:       AccountToProto(s.Account),
		Audiences:     s.Audiences,
		Scopes:        s.Scopes,
		Type:          sessionTypeToProto(s.Type),
		Epoch:         int32(s.Epoch),
	}
	if s.IpAddress != nil {
		proto.IpAddress = wrapperspb.String(*s.IpAddress)
	}
	if s.UserAgent != nil {
		proto.UserAgent = wrapperspb.String(*s.UserAgent)
	}
	if s.ClientId != nil {
		clientId := *s.ClientId
		proto.ClientId = &clientId
	}
	if s.ParentSessionId != nil {
		parentId := *s.ParentSessionId
		proto.ParentSessionId = &parentId
	}
	if s.AppId != nil {
		proto.AppId = wrapperspb.String(*s.AppId)
	}
	return proto
}

func sessionTypeToProto(t model.SessionType) gen.DySessionType {
	switch t {
	case model.SessionTypeOAuth:
		return gen.DySessionType_DY_OAUTH
	case model.SessionTypeOidc:
		return gen.DySessionType_DY_OIDC
	case model.SessionTypeApiKey:
		return gen.DySessionType_DY_API_KEY
	default:
		return gen.DySessionType_DY_LOGIN
	}
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func timeNow() time.Time { return time.Now().UTC() }
