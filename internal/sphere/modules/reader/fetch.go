package reader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
)

const (
	// userAgent is the identity the C# reader presents; social sites answer it
	// with OpenGraph metadata.
	userAgent = "facebookexternalhit/1.1"

	maxRedirectHops  = 5
	maxResponseBytes = 10 << 20 // 10 MiB
	requestTimeout   = 3 * time.Second
)

// WebReaderError marks an outbound fetch that the controller reports as
// SCRAP_LINK_ERROR with a 400.
type WebReaderError struct {
	Message string
	Err     error
}

func (e *WebReaderError) Error() string { return e.Message }

func (e *WebReaderError) Unwrap() error { return e.Err }

func newWebReaderError(message string, err error) *WebReaderError {
	return &WebReaderError{Message: message, Err: err}
}

// fetchResult is one successful outbound fetch.
type fetchResult struct {
	finalURL    *url.URL
	contentType string
	body        []byte
}

// fetcher performs SSRF-guarded GETs: every address it dials and every redirect
// hop it follows must be public, http(s) and allowed by the domain blocklist.
type fetcher struct {
	client  *http.Client
	domains ports.Domains
	schemes map[string]bool
	log     *slog.Logger
}

func newFetcher(domains ports.Domains, schemes []string, log *slog.Logger) *fetcher {
	allowed := map[string]bool{"http": true, "https": true}
	if len(schemes) > 0 {
		allowed = make(map[string]bool, len(schemes))
		for _, scheme := range schemes {
			scheme = strings.ToLower(strings.TrimSpace(scheme))
			if scheme != "" {
				allowed[scheme] = true
			}
		}
	}
	return &fetcher{
		client: &http.Client{
			Transport: &http.Transport{
				// User-controlled fetches never use a proxy from the environment.
				Proxy:                 nil,
				DialContext:           guardedDialContext,
				TLSHandshakeTimeout:   requestTimeout,
				ResponseHeaderTimeout: requestTimeout,
				DisableKeepAlives:     true,
				MaxIdleConns:          0,
			},
			Timeout: requestTimeout,
			// Redirects are followed manually so each hop is validated before it
			// is dialed.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		domains: domains,
		schemes: allowed,
		log:     log,
	}
}

// fetch downloads rawURL, validating the original URL and every redirect hop.
func (f *fetcher) fetch(ctx context.Context, rawURL string) (*fetchResult, error) {
	current, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !current.IsAbs() || current.Host == "" {
		return nil, newWebReaderError("Invalid URL format.", err)
	}

	if err := f.validate(ctx, current, nil); err != nil {
		return nil, err
	}

	for hop := 0; hop <= maxRedirectHops; hop++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return nil, newWebReaderError("Invalid URL format.", err)
		}
		request.Header.Set("User-Agent", userAgent)
		request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		request.Header.Set("Accept-Language", "en-US,en;q=0.9")

		response, err := f.client.Do(request)
		if err != nil {
			return nil, newWebReaderError("Failed to fetch URL: "+current.String(), err)
		}

		if isRedirect(response.StatusCode) {
			location := response.Header.Get("Location")
			_ = response.Body.Close()
			if location == "" {
				return nil, newWebReaderError("Redirect without a Location header: "+current.String(), nil)
			}
			next, err := current.Parse(location)
			if err != nil {
				return nil, newWebReaderError("Invalid redirect target: "+location, err)
			}
			if err := f.validate(ctx, next, current); err != nil {
				return nil, err
			}
			current = next
			continue
		}

		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_ = response.Body.Close()
			return nil, newWebReaderError(
				fmt.Sprintf("URL returned status %d: %s", response.StatusCode, current.String()), nil)
		}

		body, err := readLimited(response.Body)
		if err != nil {
			return nil, err
		}
		return &fetchResult{
			finalURL:    current,
			contentType: mediaType(response.Header.Get("Content-Type")),
			body:        body,
		}, nil
	}

	return nil, newWebReaderError("Too many redirects while fetching URL: "+rawURL, nil)
}

// validate enforces the scheme allowlist, refuses HTTPS downgrades and consults
// the domain blocklist port.
func (f *fetcher) validate(ctx context.Context, next, current *url.URL) error {
	scheme := strings.ToLower(next.Scheme)
	if !f.schemes[scheme] {
		return newWebReaderError(fmt.Sprintf("URL scheme '%s' is not supported: %s", next.Scheme, next), nil)
	}
	if current != nil && strings.EqualFold(current.Scheme, "https") && scheme == "http" {
		return newWebReaderError("Insecure redirect from HTTPS to HTTP is not allowed: "+next.String(), nil)
	}
	if f.domains == nil {
		return nil
	}
	allowed, err := f.domains.ValidateURL(ctx, next.String())
	if err != nil {
		// A domain service outage must not turn the reader into an open proxy
		// bypass; log and continue, the address guard still applies.
		if f.log != nil {
			f.log.Warn("validate outbound URL", "url", next.String(), "error", err)
		}
		return nil
	}
	if !allowed {
		return newWebReaderError("URL is blocked: "+next.String(), nil)
	}
	return nil
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func mediaType(header string) string {
	if header == "" {
		return ""
	}
	value, _, err := mime.ParseMediaType(header)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(header, ";")[0]))
	}
	return strings.ToLower(value)
}

// readLimited reads at most maxResponseBytes and fails when the body is larger.
func readLimited(body io.ReadCloser) ([]byte, error) {
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, newWebReaderError("Failed to read the response body.", err)
	}
	if len(data) > maxResponseBytes {
		return nil, newWebReaderError("Response exceeds the 10 MiB limit.", nil)
	}
	return data, nil
}

// guardedDialContext refuses to open a socket to a non-public address. The
// check runs on the addresses actually resolved for the connection, so DNS
// rebinding and unvalidated hops cannot reach internal networks.
func guardedDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	addresses, err := resolveHost(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("host %q did not resolve to any address", host)
	}
	// Fail closed when any answer is non-public, so a host that mixes public and
	// internal records is never contacted at all.
	for _, ip := range addresses {
		if isBlockedAddress(ip) {
			return nil, fmt.Errorf("refusing to connect to non-public address %s (host %q)", ip, host)
		}
	}

	dialer := &net.Dialer{Timeout: requestTimeout}
	var lastErr error
	for _, ip := range addresses {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("connection failed")
	}
	return nil, lastErr
}

func resolveHost(ctx context.Context, host string) ([]net.IP, error) {
	if literal := net.ParseIP(host); literal != nil {
		return []net.IP{literal}, nil
	}
	resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	addresses := make([]net.IP, 0, len(resolved))
	for _, entry := range resolved {
		addresses = append(addresses, entry.IP)
	}
	return addresses, nil
}

// blockedNetworks are the ranges a user-controlled fetch must never reach.
var blockedNetworks = mustParseCIDRs(
	"0.0.0.0/8",       // "this network", includes 0.0.0.0
	"10.0.0.0/8",      // private
	"100.64.0.0/10",   // CGNAT
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, includes the cloud metadata endpoint
	"172.16.0.0/12",   // private
	"192.0.0.0/24",    // reserved
	"192.0.2.0/24",    // TEST-NET-1
	"192.88.99.0/24",  // 6to4 relay anycast
	"192.168.0.0/16",  // private
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, includes 255.255.255.255
	"::/128",          // unspecified
	"::1/128",         // loopback
	"fc00::/7",        // unique local
	"fe80::/10",       // link-local
	"fec0::/10",       // deprecated site-local
	"ff00::/8",        // multicast
	"2001::/32",       // Teredo, tunnels IPv4
	"2002::/16",       // 6to4, embeds IPv4
)

func mustParseCIDRs(values ...string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			panic("reader: invalid blocked CIDR " + value)
		}
		networks = append(networks, network)
	}
	return networks
}

// isBlockedAddress reports whether ip belongs to a range a user-controlled
// outbound request must never reach. Unknown address families fail closed.
func isBlockedAddress(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else if ip.To16() == nil {
		return true
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, network := range blockedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
