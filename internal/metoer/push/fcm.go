package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// FCMResult mirrors CorePush's FirebaseSender result: status code plus the
// error string (FCM v1 error status/message).
type FCMResult struct {
	StatusCode int
	Error      string
}

// FCMSender is the CorePush FirebaseSender equivalent: OAuth2 JWT from the
// service-account key, POST to the FCM v1 messages:send endpoint.
type FCMSender struct {
	projectID   string
	tokenSource oauth2.TokenSource
	httpClient  *http.Client
}

// serviceAccountJSON is the subset of the service-account key Metoer reads.
type serviceAccountJSON struct {
	ProjectID string `json:"project_id"`
}

// NewFCMSender builds a sender from the service-account JSON bytes.
func NewFCMSender(data []byte) (*FCMSender, error) {
	var sa serviceAccountJSON
	if err := json.Unmarshal(data, &sa); err != nil {
		return nil, fmt.Errorf("parse fcm service account: %w", err)
	}
	if sa.ProjectID == "" {
		return nil, fmt.Errorf("fcm service account has no project_id")
	}
	conf, err := google.JWTConfigFromJSON(data, "https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		return nil, fmt.Errorf("build fcm jwt config: %w", err)
	}
	return &FCMSender{
		projectID:   sa.ProjectID,
		tokenSource: conf.TokenSource(context.Background()),
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// SendAsync mirrors FirebaseSender.SendAsync: posts the message dictionary
// to the FCM v1 endpoint with a Bearer token.
func (s *FCMSender) SendAsync(ctx context.Context, payload map[string]any) (*FCMResult, error) {
	if s == nil {
		return nil, fmt.Errorf("fcm sender not initialized")
	}
	tok, err := s.tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("fcm token: %w", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := "https://fcm.googleapis.com/v1/projects/" + s.projectID + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	result := &FCMResult{StatusCode: resp.StatusCode}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return result, nil
	}
	result.Error = extractFCMError(raw)
	return result, nil
}

// extractFCMError mirrors CorePush's error extraction: the FCM v1 error body
// {"error":{"code":404,"message":"...","status":"UNREGISTERED"}} surfaces the
// status (falling back to the message, then the raw body).
func extractFCMError(raw []byte) string {
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error != nil {
		if envelope.Error.Status != "" {
			return envelope.Error.Status
		}
		if envelope.Error.Message != "" {
			return envelope.Error.Message
		}
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}
