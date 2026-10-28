package publishers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// domainHTTPTimeout mirrors DomainVerificationService.RequestTimeout.
const domainHTTPTimeout = 10 * time.Second

// verifyDomain re-runs the ownership check and records the outcome.
func (i *instance) verifyDomain(ctx context.Context, publisherID uuid.UUID, domain string) (bool, error) {
	var record store.PublisherVerifiedDomainEntity
	err := i.store.DB.WithContext(ctx).
		Where("publisher_id = ? AND domain = ?", publisherID, domain).
		First(&record).Error
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	record.LastCheckedAt = &now

	publisher, err := i.store.GetPublisher(ctx, publisherID)
	if err != nil {
		record.Status = domainFailed
		record.FailedAttempts++
		message := "Publisher not found"
		record.LastError = &message
		_ = i.store.DB.WithContext(ctx).Save(&record).Error
		return false, nil
	}

	verified, failure := i.checkDomainOwnership(ctx, publisher.Name, domain)
	if verified {
		record.Status = domainVerified
		record.VerifiedAt = &now
		record.LastError = nil
	} else {
		record.Status = domainFailed
		record.FailedAttempts++
		record.LastError = &failure
	}
	if err := i.store.DB.WithContext(ctx).Save(&record).Error; err != nil {
		return verified, err
	}
	return verified, nil
}

// checkDomainOwnership validates the publisher name in
// https://<domain>/.well-known/dyson-domains.txt, falling back to a DNS TXT
// record. Every outbound connection is restricted to public addresses so a
// publisher cannot point the check at internal infrastructure.
func (i *instance) checkDomainOwnership(ctx context.Context, publisherName, domain string) (bool, string) {
	normalized, err := normalizeDomain(domain)
	if err != nil {
		return false, err.Error()
	}
	if _, err := resolvePublicHosts(ctx, normalized); err != nil {
		return false, err.Error()
	}

	client := &http.Client{
		Timeout:   domainHTTPTimeout,
		Transport: publicTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			host := req.URL.Hostname()
			if _, err := resolvePublicHosts(ctx, host); err != nil {
				return err
			}
			return nil
		},
	}

	url := fmt.Sprintf("https://%s/.well-known/dyson-domains.txt", normalized)
	response, err := client.Get(url)
	if err == nil {
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			content, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			if readErr == nil {
				if containsPublisherLine(string(content), publisherName) {
					return true, ""
				}
				if txtVerified(ctx, normalized, publisherName) {
					return true, ""
				}
				return false, "Publisher name not found in dyson-domains.txt"
			}
			return false, readErr.Error()
		}
		if txtVerified(ctx, normalized, publisherName) {
			return true, ""
		}
		return false, fmt.Sprintf("HTTP %d", response.StatusCode)
	}

	if txtVerified(ctx, normalized, publisherName) {
		return true, ""
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Timeout") || strings.Contains(err.Error(), "deadline exceeded") {
		return false, "Request timed out"
	}
	return false, err.Error()
}

func containsPublisherLine(content, publisherName string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, publisherName) {
			return true
		}
	}
	return false
}

// txtVerified accepts either a bare publisher name or `dyson-domains=<name>`
// in the `_dyson-domains.<domain>` TXT record.
func txtVerified(ctx context.Context, domain, publisherName string) bool {
	records, err := net.DefaultResolver.LookupTXT(ctx, "_dyson-domains."+domain)
	if err != nil {
		return false
	}
	for _, record := range records {
		for _, line := range strings.Split(record, "\n") {
			line = strings.TrimSpace(line)
			if strings.EqualFold(line, publisherName) {
				return true
			}
			if value, ok := strings.CutPrefix(line, "dyson-domains="); ok && strings.EqualFold(strings.TrimSpace(value), publisherName) {
				return true
			}
		}
	}
	return false
}

func normalizeDomain(raw string) (string, error) {
	domain := strings.TrimSpace(strings.ToLower(raw))
	if domain == "" {
		return "", errors.New("Domain is required.")
	}
	if strings.ContainsAny(domain, " /\\@") {
		return "", errors.New("Invalid domain")
	}
	parsed, err := url.Parse("https://" + domain)
	if err != nil || parsed.Host != domain || parsed.Hostname() == "" {
		return "", errors.New("Invalid domain")
	}
	return domain, nil
}

func publicTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           publicDialContext(dialer),
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: domainHTTPTimeout,
		DisableKeepAlives:     true,
	}
}

func publicDialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := resolvePublicHosts(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range addresses {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = errors.New("no public address")
		}
		return nil, lastErr
	}
}

func resolvePublicHosts(ctx context.Context, host string) ([]net.IP, error) {
	if host == "" {
		return nil, errors.New("empty host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return nil, errors.New("domain resolves to a non-public address")
		}
		return []net.IP{ip}, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return nil, fmt.Errorf("domain could not be resolved: %w", err)
	}
	public := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if !isPublicIP(address.IP) {
			return nil, errors.New("domain resolves to a non-public address")
		}
		public = append(public, address.IP)
	}
	if len(public) == 0 {
		return nil, errors.New("domain could not be resolved")
	}
	return public, nil
}

// isPublicIP rejects loopback, private, link-local, multicast and reserved
// ranges so the verification request cannot reach internal services.
func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 0:
			return false
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127:
			return false // 100.64.0.0/10 CGNAT
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0:
			return false // 192.0.0.0/24
		case ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19):
			return false // 198.18.0.0/15 benchmarking
		case ip4[0] == 169 && ip4[1] == 254:
			return false // link-local
		case ip4[0] >= 240:
			return false // reserved + broadcast
		}
		return true
	}
	// IPv6: reject documentation and unique-local ranges the stdlib misses.
	if len(ip) == net.IPv6len {
		if ip[0]&0xfe == 0xfc {
			return false // fc00::/7 unique local
		}
		if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8 {
			return false // 2001:db8::/32 documentation
		}
	}
	return true
}

func generateRSAKeyPair() (string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	privateDER := x509.MarshalPKCS1PrivateKey(key)
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privateDER})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	return string(privatePEM), string(publicPEM), nil
}
