package translation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	tencentEndpoint  = "https://tmt.tencentcloudapi.com"
	tencentHost      = "tmt.tencentcloudapi.com"
	tencentService   = "tmt"
	tencentAction    = "TextTranslate"
	tencentVersion   = "2018-03-21"
	tencentAlgorithm = "TC3-HMAC-SHA256"
	tencentTimeout   = 8 * time.Second
	tencentMaxBody   = 1 << 20
)

// tencentProvider calls Tencent Cloud Machine Translation (TMT) with the
// TC3-HMAC-SHA256 request signature.
type tencentProvider struct {
	client    *http.Client
	region    string
	secretID  string
	secretKey string
	projectID int64
}

func newTencentProvider(region, secretID, secretKey string, projectID int64) Provider {
	return &tencentProvider{
		client:    &http.Client{Timeout: tencentTimeout},
		region:    strings.TrimSpace(region),
		secretID:  secretID,
		secretKey: secretKey,
		projectID: projectID,
	}
}

// Translate sends one TextTranslate call. The caller guarantees non-empty
// credentials, so this method never signs a request with empty keys.
func (p *tencentProvider) Translate(ctx context.Context, text, targetLanguage, sourceLanguage string) (string, error) {
	if p.secretID == "" || p.secretKey == "" {
		return "", fmt.Errorf("tencent translation credentials are not configured")
	}

	source := strings.TrimSpace(sourceLanguage)
	if source == "" {
		source = "auto"
	}
	payload, err := json.Marshal(map[string]any{
		"SourceText": text,
		"Source":     source,
		"Target":     targetLanguage,
		"ProjectId":  p.projectID,
	})
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	timestamp := now.Unix()
	date := now.Format("2006-01-02")
	contentType := "application/json; charset=utf-8"
	signedHeaders := "content-type;host"
	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/",
		"",
		"content-type:" + contentType + "\n" + "host:" + tencentHost + "\n",
		signedHeaders,
		sha256Hex(payload),
	}, "\n")
	scope := date + "/" + tencentService + "/tc3_request"
	stringToSign := strings.Join([]string{
		tencentAlgorithm,
		strconv.FormatInt(timestamp, 10),
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	secretDate := hmacSHA256([]byte("TC3"+p.secretKey), date)
	secretService := hmacSHA256(secretDate, tencentService)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))
	authorization := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		tencentAlgorithm, p.secretID, scope, signedHeaders, signature)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tencentEndpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-TC-Action", tencentAction)
	request.Header.Set("X-TC-Version", tencentVersion)
	request.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	if p.region != "" {
		request.Header.Set("X-TC-Region", p.region)
	}
	request.Header.Set("Authorization", authorization)

	response, err := p.client.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, tencentMaxBody))
	if err != nil {
		return "", err
	}

	var decoded struct {
		Response struct {
			TargetText string `json:"TargetText"`
			Error      *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode tencent translation response: %w", err)
	}
	if decoded.Response.Error != nil {
		return "", fmt.Errorf("tencent translation failed: %s: %s",
			decoded.Response.Error.Code, decoded.Response.Error.Message)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("tencent translation returned status %d", response.StatusCode)
	}
	return decoded.Response.TargetText, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
