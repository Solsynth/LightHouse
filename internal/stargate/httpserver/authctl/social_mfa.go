package authctl

import (
	"context"
	"fmt"
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
	// Provider is the social provider's name; ProviderUserID the identity it
	// asserted. Both go on the trail.
	Provider       string
	ProviderUserID string
}

// BeginSocialMFA applies the same risk scoring as the username/password
// challenge to a provider sign-in. A provider login proves one step; when the
// environment looks risky (new IP/user agent, long absence, recent failures) and
// the account has further factors, it must not mint a session. The returned
// challenge then has StepRemain > 0: the caller hands it to the client to
// finish and exchange via POST /auth/token.
//
// A challenge is recorded for every provider sign-in, risky or not, so each
// session it leads to can be traced back (provider, ip, user agent, risk
// evidence). When no further step is owed the challenge is stored already
// settled and expired — the caller links the session to it with
// store.SetSessionChallenge, and it can never be exchanged.
//
// Accounts without any completable factor cannot be asked for one and pass.
func BeginSocialMFA(ctx context.Context, d SocialMFADeps, account *model.Account, cc SocialMFAContext) (*model.AuthChallenge, error) {
	h := &handler{d: Deps{Store: d.Store, Cfg: d.Cfg}}

	steps := 1
	var assessment riskAssessment
	maxSteps, err := h.allRequiredStepCount(ctx, account.Id)
	if err != nil {
		return nil, err
	}
	if maxSteps > 0 {
		mode, err := d.Store.GetSecurityMode(ctx, account.Id)
		if err != nil {
			mode = model.SecurityModeDefault
		}
		assessment, err = h.assessChallengeRisk(ctx, account.Id, cc.IPAddress, cc.UserAgent, mode)
		if err != nil {
			return nil, err
		}
		steps = assessment.Steps
	} else {
		assessment.Signals = []string{"factors:none_completable"}
	}
	// The provider identity is the first step; only the rest need completing.
	remain := steps - 1
	if remain < 0 {
		remain = 0
	}

	now := time.Now().UTC()
	deviceName := cc.UserAgent
	if cc.DeviceName != nil {
		deviceName = *cc.DeviceName
	}
	origin := model.ChallengeOriginSocialMFA + ":" + cc.Provider
	expiresAt := now.Add(time.Hour)
	if remain == 0 {
		expiresAt = now // settled: a trace record, never exchangeable
	}
	challenge := &model.AuthChallenge{
		Id:          uuid.NewString(),
		ExpiredAt:   model.NewTime(expiresAt),
		StepTotal:   steps,
		StepRemain:  remain,
		IpAddress:   &cc.IPAddress,
		UserAgent:   &cc.UserAgent,
		DeviceId:    cc.DeviceID,
		DeviceName:  &deviceName,
		Platform:    cc.Platform,
		AccountId:   account.Id,
		CreatedAt:   model.NewTime(now),
		UpdatedAt:   model.NewTime(now),
		Origin:      origin,
		RiskScore:   assessment.Score,
		RiskSignals: assessment.Signals,
		Trail: []model.ChallengeEvent{{
			At:        now,
			Type:      model.ChallengeEventCreated,
			IpAddress: cc.IPAddress,
			UserAgent: cc.UserAgent,
			DeviceId:  cc.DeviceID,
			Detail:    fmt.Sprintf("origin=%s steps=%d remain=%d", origin, steps, remain),
		}, {
			At:        now,
			Type:      model.ChallengeEventFactorOK,
			IpAddress: cc.IPAddress,
			UserAgent: cc.UserAgent,
			DeviceId:  cc.DeviceID,
			Detail:    "provider:" + cc.Provider + " provider_user=" + cc.ProviderUserID,
		}},
	}
	if err := d.Store.CreateAuthChallenge(ctx, challenge); err != nil {
		return nil, err
	}
	return challenge, nil
}
