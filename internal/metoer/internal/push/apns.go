package push

import (
	"fmt"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
)

// APNResult mirrors CorePush's ApnResult: status code plus the reason
// string.
type APNResult struct {
	StatusCode int
	Error      string
}

// ApnSender is the CorePush ApnSender equivalent: one APNs client per
// topic, built from the p8 key (ES256), team id and key id.
type ApnSender struct {
	client *apns2.Client
	topic  string
}

// NewApnSender builds an ApnSender for one topic.
func NewApnSender(privateKey []byte, privateKeyID, teamID, topic string, production bool) (*ApnSender, error) {
	authKey, err := token.AuthKeyFromBytes(privateKey)
	if err != nil {
		return nil, fmt.Errorf("parse apns p8 key: %w", err)
	}
	tok := &token.Token{
		AuthKey: authKey,
		KeyID:   privateKeyID,
		TeamID:  teamID,
	}
	client := apns2.NewTokenClient(tok)
	if !production {
		client.Development()
	}
	return &ApnSender{client: client, topic: topic}, nil
}

// SendAsync mirrors CorePush ApnSender.SendAsync: posts the payload dict to
// /3/device/{token} with the apns headers (topic, id, priority, push type).
func (s *ApnSender) SendAsync(deviceToken string, payload map[string]any, apnsID string, apnsPriority int, apnPushType apns2.EPushType) (*APNResult, error) {
	notification := &apns2.Notification{
		DeviceToken: deviceToken,
		Topic:       s.topic,
		Payload:     payload,
		ApnsID:      apnsID,
		Priority:    apnsPriority,
		PushType:    apnPushType,
	}
	resp, err := s.client.Push(notification)
	if err != nil {
		return nil, err
	}
	return &APNResult{StatusCode: resp.StatusCode, Error: resp.Reason}, nil
}
