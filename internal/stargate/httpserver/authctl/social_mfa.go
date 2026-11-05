package authctl

import (
	"context"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// SocialMFADeps is what BeginSocialMFA needs; socialctl supplies it without
// depending on the rest of authctl's wiring.
type SocialMFADeps struct {
	Store *store.Store
	Cfg   *config.Config
}

// SocialMFAContext describes the client completing a provider sign-in.
type SocialMFAContext struct {
	IPAddress  string
	UserAgent  string
	DeviceID   string
	DeviceName *string
	Platform   model.ClientPlatform
}

// BeginSocialMFA applies the same risk scoring as the username/password
// challenge to a provider sign-in. A provider login proves one step; when the
// environment looks risky (new IP/user agent, long absence, recent failures) and
// the account has further factors, it must not mint a session. Instead a
// challenge with the remaining steps is stored and returned, and the caller
// hands it to the client to finish and exchange via POST /auth/token.
//
// A nil challenge with a nil error means no extra verification is required.
// Accounts without any completable factor cannot be asked for one and pass.
func BeginSocialMFA(ctx context.Context, d SocialMFADeps, account *model.Account, cc SocialMFAContext) (*model.AuthChallenge, error) {
	h := &handler{d: Deps{Store: d.Store, Cfg: d.Cfg}}

	maxSteps, err := h.allRequiredStepCount(ctx, account.Id)
	if err != nil {
		return nil, err
	}
	if maxSteps == 0 {
		return nil, nil
	}
	mode, err := d.Store.GetSecurityMode(ctx, account.Id)
	if err != nil {
		mode = model.SecurityModeDefault
	}
	steps, err := h.detectChallengeRisk(ctx, account.Id, cc.IPAddress, cc.UserAgent, mode)
	if err != nil {
		return nil, err
	}
	// The provider identity is the first step; only the rest need completing.
	remain := steps - 1
	if remain <= 0 {
		return nil, nil
	}

	now := time.Now().UTC()
	deviceName := cc.UserAgent
	if cc.DeviceName != nil {
		deviceName = *cc.DeviceName
	}
	challenge := &model.AuthChallenge{
		Id:         uuid.NewString(),
		ExpiredAt:  model.NewTime(now.Add(time.Hour)),
		StepTotal:  steps,
		StepRemain: remain,
		IpAddress:  &cc.IPAddress,
		UserAgent:  &cc.UserAgent,
		DeviceId:   cc.DeviceID,
		DeviceName: &deviceName,
		Platform:   cc.Platform,
		AccountId:  account.Id,
		CreatedAt:  model.NewTime(now),
		UpdatedAt:  model.NewTime(now),
	}
	if err := d.Store.CreateAuthChallenge(ctx, challenge); err != nil {
		return nil, err
	}
	return challenge, nil
}
