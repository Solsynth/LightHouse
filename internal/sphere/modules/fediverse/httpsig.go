package fediverse

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Signature timing tolerances mirror HttpSignature.MaxClockSkew.
const (
	maxClockSkew      = 5 * time.Minute
	maxSignatureAge   = 5 * time.Minute
	maxSignatureAhead = 5 * time.Minute
)

type signatureHeader struct {
	KeyID     string
	Algorithm string
	Signature []byte
	Headers   []string
	Created   string
	Expires   string
	Opaque    string
}

func parseSignatureHeader(raw string) (*signatureHeader, error) {
	parts := map[string]string{}
	for _, chunk := range strings.Split(raw, ",") {
		pair := strings.SplitN(chunk, "=", 2)
		key := strings.ToLower(strings.TrimSpace(pair[0]))
		value := ""
		if len(pair) == 2 {
			value = strings.Trim(strings.TrimSpace(pair[1]), `"`)
		}
		parts[key] = value
	}
	encoded := parts["signature"]
	if encoded == "" {
		return nil, errors.New("signature header is missing the signature field")
	}
	headersRaw := parts["headers"]
	if headersRaw == "" {
		return nil, errors.New("signature header is missing the headers field")
	}
	keyID := parts["keyid"]
	if keyID == "" {
		return nil, errors.New("signature header is missing the keyId field")
	}
	signature, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("signature is not valid base64: %w", err)
	}
	algorithm := parts["algorithm"]
	if algorithm == "" {
		algorithm = "rsa-sha256"
	}
	headers := []string{}
	for _, header := range strings.Fields(headersRaw) {
		headers = append(headers, strings.ToLower(header))
	}
	return &signatureHeader{
		KeyID:     keyID,
		Algorithm: algorithm,
		Signature: signature,
		Headers:   headers,
		Created:   parts["created"],
		Expires:   parts["expires"],
		Opaque:    parts["opaque"],
	}, nil
}

// verifyIncoming validates an inbox request and returns the actor URI the
// signature belongs to.
func (s *server) verifyIncoming(r *http.Request, body []byte) (bool, string, error) {
	raw := r.Header.Get("Signature")
	if raw == "" {
		return false, "", errors.New("request is missing the Signature header")
	}
	signature, err := parseSignatureHeader(raw)
	if err != nil {
		return false, "", err
	}
	actorURI := strings.Split(signature.KeyID, "#")[0]
	if actorURI == "" {
		return false, "", errors.New("signature keyId has no actor URI")
	}

	required := []string{"(request-target)", "host", "date"}
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		required = append(required, "digest")
	}
	present := make(map[string]bool, len(signature.Headers))
	for _, header := range signature.Headers {
		present[header] = true
	}
	for _, header := range required {
		if !present[header] {
			return false, "", fmt.Errorf("request is missing required signed header %q", header)
		}
	}

	if err := verifySignatureClock(r, signature); err != nil {
		return false, "", err
	}
	if len(body) > 0 {
		if err := verifyDigest(r.Header.Get("Digest"), body); err != nil {
			return false, "", err
		}
	}

	publicKey, err := s.keys.resolvePublicKey(context.Background(), signature.KeyID, actorURI)
	if err != nil {
		return false, "", err
	}
	signingString := signingString(signature.Headers, requestTarget(r), r.Host,
		r.Header.Get("Date"), r.Header.Get("Digest"), r.Header.Get("Content-Type"),
		signature.Created, signature.Expires)
	if err := verifyRSA(publicKey, signingString, signature.Signature); err != nil {
		return false, "", err
	}
	return true, actorURI, nil
}

func verifySignatureClock(r *http.Request, signature *signatureHeader) error {
	now := time.Now()
	if signature.Expires != "" {
		seconds, err := strconv.ParseInt(signature.Expires, 10, 64)
		if err == nil && now.After(time.Unix(seconds, 0).Add(maxClockSkew)) {
			return errors.New("request signature is expired")
		}
	}
	if signature.Created != "" {
		seconds, err := strconv.ParseInt(signature.Created, 10, 64)
		if err == nil {
			created := time.Unix(seconds, 0)
			if now.Sub(created) > maxSignatureAge {
				return errors.New("request signature is too old")
			}
			if created.Sub(now) > maxSignatureAhead {
				return errors.New("request signature is from the future")
			}
			return nil
		}
	}
	dateHeader := r.Header.Get("Date")
	if dateHeader == "" {
		return errors.New("neither date nor (created) are present")
	}
	parsed, err := http.ParseTime(dateHeader)
	if err != nil {
		return errors.New("request date header is malformed")
	}
	if now.Sub(parsed) > maxSignatureAge {
		return errors.New("request date is too old")
	}
	if parsed.Sub(now) > maxSignatureAhead {
		return errors.New("request date is from the future")
	}
	return nil
}

func verifyDigest(header string, body []byte) error {
	if header == "" {
		return errors.New("digest header is required for a request with a body")
	}
	element := strings.TrimSpace(strings.Split(header, ",")[0])
	pair := strings.SplitN(element, "=", 2)
	if len(pair) != 2 {
		return errors.New("digest header is malformed")
	}
	algorithm := strings.ToLower(strings.TrimSpace(pair[0]))
	if algorithm != "sha-256" && algorithm != "sha256" {
		return fmt.Errorf("unsupported digest algorithm %q", pair[0])
	}
	sum := sha256.Sum256(body)
	expected := strings.TrimSpace(pair[1])
	if expected != base64.StdEncoding.EncodeToString(sum[:]) {
		return errors.New("digest mismatch")
	}
	return nil
}

func requestTarget(r *http.Request) string {
	target := r.URL.EscapedPath()
	if target == "" {
		target = "/"
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	return target
}

func signingString(headers []string, target, host, date, digest, contentType, created, expires string) string {
	lines := make([]string, 0, len(headers))
	for _, header := range headers {
		var value string
		switch strings.ToLower(header) {
		case "(request-target)":
			value = target
		case "host":
			value = host
		case "date":
			value = date
		case "digest":
			value = digest
		case "content-type":
			value = contentType
		case "(created)":
			value = created
		case "(expires)":
			value = expires
		}
		lines = append(lines, strings.ToLower(header)+": "+value)
	}
	return strings.Join(lines, "\n")
}

// signRequest signs an outbound request the way HttpSignature.SignRequestAsync
// does. The body must be the exact bytes that will be sent.
func (s *server) signRequest(req *http.Request, body []byte, keyID, privatePEM string, includeDigest bool) error {
	now := time.Now().UTC()
	req.Header.Set("Date", now.Format(http.TimeFormat))
	req.Header.Set("Host", req.URL.Host)
	if req.Host == "" {
		req.Host = req.URL.Host
	}

	headers := []string{"(request-target)", "host", "date"}
	if includeDigest && len(body) > 0 {
		sum := sha256.Sum256(body)
		req.Header.Set("Digest", "SHA-256="+base64.StdEncoding.EncodeToString(sum[:]))
		headers = append(headers, "digest")
	}
	if contentType := req.Header.Get("Content-Type"); contentType != "" {
		headers = append(headers, "content-type")
	}

	base := signingString(headers, requestTarget(req), req.Host,
		req.Header.Get("Date"), req.Header.Get("Digest"), req.Header.Get("Content-Type"), "", "")

	privateKey, err := parsePrivateKeyPEM(privatePEM)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(base))
	signature, err := rsa.SignPKCS1v15(nil, privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return err
	}
	req.Header.Set("Signature", fmt.Sprintf(
		`keyId="%s",algorithm="rsa-sha256",headers="%s",signature="%s"`,
		keyID, strings.Join(headers, " "), base64.StdEncoding.EncodeToString(signature)))
	return nil
}

// signRequestAs signs with the actor's own key, falling back to the server key.
func (s *server) signRequestAs(ctx context.Context, req *http.Request, body []byte, actor *store.PublisherEntity) error {
	if actor != nil {
		if key, err := s.keys.getOrCreateActorKey(ctx, actor); err == nil && key != nil && key.PrivateKeyPEM != nil {
			return s.signRequest(req, body, key.KeyID, *key.PrivateKeyPEM, req.Method != http.MethodGet)
		}
	}
	serverKey, err := s.keys.getServerKey()
	if err != nil {
		return err
	}
	return s.signRequest(req, body, serverKey.KeyID, serverKey.PrivateKeyPEM, req.Method != http.MethodGet)
}

// ---- crypto helpers -------------------------------------------------------

func verifyRSA(publicKeyPEM, base, signatureB64 string) error {
	publicKey, err := parsePublicKeyPEM(publicKeyPEM)
	if err != nil {
		return err
	}
	decoded, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return err
	}
	return verifyRSABytes(publicKey, base, decoded)
}

func verifyRSABytes(publicKey *rsa.PublicKey, base string, signature []byte) error {
	hash := sha256.Sum256([]byte(base))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, hash[:], signature); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}
	return nil
}

func parsePrivateKeyPEM(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("private key is not PEM encoded")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, nil
}

func parsePublicKeyPEM(value string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("public key is not PEM encoded")
	}
	if strings.Contains(value, "BEGIN RSA PUBLIC KEY") {
		return x509.ParsePKCS1PublicKey(block.Bytes)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		if certificate, certErr := x509.ParseCertificate(block.Bytes); certErr == nil {
			parsed = certificate.PublicKey
		} else {
			return nil, fmt.Errorf("parse public key: %w", err)
		}
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not RSA")
	}
	return key, nil
}

func generateKeyPair() (privatePEM, publicPEM string, err error) {
	key, err := rsa.GenerateKey(cryptoRandReader(), 2048)
	if err != nil {
		return "", "", err
	}
	privateBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	publicBlock := &pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}
	return string(pem.EncodeToMemory(privateBlock)), string(pem.EncodeToMemory(publicBlock)), nil
}

// readBody drains a request body once so the digest and the JSON decoder both
// see the same bytes.
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
