// Package push ports DysonNetwork.Ring.Notification.PushService: per-app
// sender resolution, FCM/APNs/UnifiedPush delivery, SOP streams + replay,
// the websocket fan-out and the invalid-token flush buffer.
package push

import (
	"net/http"
	"os"
	"strings"

	"src.solsynth.dev/sosys/metoer/internal/config"
)

// AppSenders is the C# AppSenders record: per-app push capability.
type AppSenders struct {
	FCM              *FCMSender
	ApnsByTopic      map[string]*ApnSender
	DefaultApnsTopic string
	Topics           map[string]string
}

// buildAppSenders mirrors PushService.BuildAppSenders.
func buildAppSenders(cfg *config.PushAppConfig, httpClient *http.Client) *AppSenders {
	var fcm *FCMSender
	if cfg.FcmKeyPath != "" {
		if _, err := os.Stat(cfg.FcmKeyPath); err == nil {
			data, err := os.ReadFile(cfg.FcmKeyPath)
			if err == nil {
				fcm, _ = NewFCMSender(data)
			}
		}
	}

	apnsTopic := ""
	topics := make(map[string]string, len(cfg.Topics))
	for k, v := range cfg.Topics {
		topics[k] = v
	}
	apnsByTopic := map[string]*ApnSender{}
	if cfg.Apns != nil {
		keyPath := cfg.Apns.PrivateKeyPath
		if _, err := os.Stat(keyPath); err == nil {
			privateKey, err := os.ReadFile(keyPath)
			if err == nil && cfg.Apns.BundleIdentifier != "" {
				apnsTopic = cfg.Apns.BundleIdentifier
				if _, ok := topics["Alert"]; !ok {
					topics["Alert"] = apnsTopic
				}
				seen := map[string]struct{}{}
				for _, topic := range topics {
					if _, ok := seen[topic]; ok {
						continue
					}
					seen[topic] = struct{}{}
					sender, err := NewApnSender(privateKey, cfg.Apns.PrivateKeyId, cfg.Apns.TeamId, topic, cfg.Production)
					if err == nil {
						apnsByTopic[topic] = sender
					}
				}
			}
		}
	}

	return &AppSenders{FCM: fcm, ApnsByTopic: apnsByTopic, DefaultApnsTopic: apnsTopic, Topics: topics}
}

// PushService is the push delivery engine (PushService).
type PushService struct {
	appSenders  map[string]*AppSenders
	defaultAppId string
}

// NewApps builds the app-sender registry, mirroring the PushService
// constructor: per-app configs under Notifications:Push:Apps, or the legacy
// flat layout (Google/Apple keys) under a "_default" app id.
func NewApps(cfg *config.Config, httpClient *http.Client) *PushService {
	s := &PushService{appSenders: map[string]*AppSenders{}}
	pushCfg := &cfg.Notifications.Push

	if len(pushCfg.Apps) > 0 {
		for appID, appConfig := range pushCfg.Apps {
			s.appSenders[appID] = buildAppSenders(&appConfig, httpClient)
		}
		if pushCfg.DefaultApp != "" {
			s.defaultAppId = pushCfg.DefaultApp
		}
	} else {
		legacy := &config.PushAppConfig{
			Production: pushCfg.Production,
			FcmKeyPath: pushCfg.Google,
		}
		if pushCfg.Apple.PrivateKey != "" || pushCfg.Apple.PrivateKeyId != "" || pushCfg.Apple.TeamId != "" || pushCfg.Apple.BundleIdentifier != "" {
			legacy.Apns = &config.ApnsAppConfig{
				PrivateKeyPath:   pushCfg.Apple.PrivateKey,
				PrivateKeyId:     pushCfg.Apple.PrivateKeyId,
				TeamId:           pushCfg.Apple.TeamId,
				BundleIdentifier: pushCfg.Apple.BundleIdentifier,
			}
		}
		s.appSenders["_default"] = buildAppSenders(legacy, httpClient)
		s.defaultAppId = "_default"
	}

	if s.defaultAppId == "" {
		for appID := range s.appSenders {
			s.defaultAppId = appID
			break
		}
	}
	return s
}

// GetDefaultAppId mirrors PushService.GetDefaultAppId.
func (s *PushService) GetDefaultAppId() string { return s.defaultAppId }

// ResolveAppId mirrors PushService.ResolveAppId.
func (s *PushService) ResolveAppId(appId string, useDefaultIfMissing bool) string {
	if strings.TrimSpace(appId) != "" {
		return appId
	}
	if useDefaultIfMissing {
		return s.defaultAppId
	}
	return ""
}

// ResolveAppSenders mirrors PushService.ResolveAppSenders.
func (s *PushService) ResolveAppSenders(appId string) *AppSenders {
	if appId != "" {
		if senders, ok := s.appSenders[appId]; ok {
			return senders
		}
	}
	if s.defaultAppId != "" {
		if senders, ok := s.appSenders[s.defaultAppId]; ok {
			return senders
		}
	}
	return nil
}

// HasApps reports whether any app configuration was loaded.
func (s *PushService) HasApps() bool { return len(s.appSenders) > 0 }
