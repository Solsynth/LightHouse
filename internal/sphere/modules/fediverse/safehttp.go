package fediverse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// errBlockedAddress marks a target that resolves to a non-public address. The
// SSRF guard refuses to dial it, so user-controlled fetches can never reach
// loopback, private, link-local or metadata space.
var errBlockedAddress = errors.New("fediverse: outbound address is not public")

// blockedAddress mirrors SsrfGuard.IsBlockedAddress.
func blockedAddress(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		switch v4[0] {
		case 0: // 0.0.0.0/8
			return true
		case 10: // 10.0.0.0/8
			return true
		case 100:
			return v4[1] >= 64 && v4[1] <= 127 // 100.64.0.0/10 CGNAT
		case 127: // loopback
			return true
		case 169:
			return v4[1] == 254 // link-local, includes 169.254.169.254
		case 172:
			return v4[1] >= 16 && v4[1] <= 31
		case 192:
			return v4[1] == 168 ||
				(v4[1] == 0 && (v4[2] == 0 || v4[2] == 2)) ||
				(v4[1] == 88 && v4[2] == 99)
		case 198:
			return v4[1] == 18 || v4[1] == 19 ||
				(v4[1] == 51 && v4[2] == 100)
		case 203:
			return v4[1] == 0 && v4[2] == 113
		case 224, 225, 226, 227, 228, 229, 230, 231, 232, 233, 234, 235,
			236, 237, 238, 239, 240, 241, 242, 243, 244, 245, 246, 247,
			248, 249, 250, 251, 252, 253, 254, 255:
			return true
		}
		return false
	}
	if ip.To16() != nil {
		if ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
			ip.IsMulticast() {
			return true
		}
		bytes := ip.To16()
		if len(bytes) == 16 {
			if bytes[0]&0xfe == 0xfc { // fc00::/7 unique local
				return true
			}
			if bytes[0] == 0xfe && bytes[1]&0xc0 == 0xc0 { // fec0::/10 site local
				return true
			}
			if bytes[0] == 0x20 && bytes[1] == 0x02 { // 2002::/16 6to4
				return true
			}
			if bytes[0] == 0x20 && bytes[1] == 0x01 && bytes[2] == 0x00 && bytes[3] == 0x00 {
				return true // 2001::/32 Teredo
			}
		}
		return false
	}
	return true
}

func allowedScheme(scheme string) bool {
	return scheme == "http" || scheme == "https"
}

// validateOutboundURL checks the scheme and every resolved address before a
// request is issued. The dial-time check repeats it on the actual endpoint.
func validateOutboundURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() {
		return fmt.Errorf("fediverse: invalid URL %q", raw)
	}
	if !allowedScheme(parsed.Scheme) {
		return fmt.Errorf("fediverse: unsupported scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("fediverse: URL has no host")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return fmt.Errorf("fediverse: resolve %s: %w", host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("fediverse: %s resolved to no addresses", host)
	}
	for _, address := range addresses {
		if blockedAddress(address.IP) {
			return errBlockedAddress
		}
	}
	return nil
}

// newSafeClient returns an HTTP client whose transport refuses to connect to
// non-public addresses. Because the guard runs on the address that is actually
// dialled it also covers DNS rebinding, and the redirect policy re-validates
// every hop (scheme, host, and the https→http downgrade).
func newSafeClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          32,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, resolved := range addresses {
				if blockedAddress(resolved.IP) {
					return nil, errBlockedAddress
				}
			}
			var lastErr error
			for _, resolved := range addresses {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = errBlockedAddress
			}
			return nil, lastErr
		},
	}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("fediverse: too many redirects")
			}
			if !allowedScheme(req.URL.Scheme) {
				return fmt.Errorf("fediverse: redirect to unsupported scheme %q", req.URL.Scheme)
			}
			if previous := via[len(via)-1]; previous.URL.Scheme == "https" && req.URL.Scheme == "http" {
				return errors.New("fediverse: refusing to downgrade https to http")
			}
			return validateOutboundURL(req.URL.String())
		},
	}
}
