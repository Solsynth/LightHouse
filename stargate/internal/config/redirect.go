package config

import (
	"net/url"
	"strings"
)

// Sign-in hand-off policy.
//
// The console's sign-in page is shared: another web app opens
// `{siteUrl}/sign-in?redirectUri={its callback}` and gets the person back once
// they are signed in. Two things therefore have to hold.
//
// The session travels in the HttpOnly AuthToken cookie, which is scoped by
// [auth] cookieDomain. Every origin under that domain — a subdomain shares the
// parent's cookie — receives the session, so the set of apps that may reuse the
// login page is exactly the set of origins that share the cookie. Anything
// outside it would arrive signed out, so it is refused rather than silently
// losing the session.
//
// And the landing target arrives in the address bar, which any page can write.
// A target is honoured only when it names one of those cookie-sharing origins
// (or a page of this deployment's own site origin); anything else would turn
// the login page into an open redirector for an attacker's link.
//
// Landing on a cookie host and landing on a hand-off host are not the same
// trip, so the policy answers with two lists rather than one. A cookie host is
// reached by a plain navigation — the browser already has the session there. A
// hand-off host has no cookie to receive, so the sign-in has to finish by
// minting a single-use code; asking for one on a host the cookie already covers
// is refused by the issuer, which is why the split is published rather than
// left for the caller to infer from one flat list.

// SignInCookieHosts lists the hosts this deployment's session cookie reaches:
// [auth] cookieDomain and its subdomains, plus the [siteUrl] host. A sign-in
// that lands on one of them needs no further step — a plain navigation arrives
// signed in.
func (c *Config) SignInCookieHosts() []string {
	if c == nil {
		return nil
	}
	hosts := make([]string, 0, 2)
	addHost(&hosts, c.Auth.CookieDomain)
	if site, err := url.Parse(strings.TrimRight(c.SiteUrl, "/")); err == nil {
		addHost(&hosts, site.Host)
	}
	return hosts
}

// SignInHandoffHosts lists the hosts a registered hand-off client claims. No
// cookie can reach them (see internal/httpserver/authctl/handoff.go), so a
// sign-in there finishes by code exchange. A host the cookie already covers is
// left out: the cookie wins, and the issuer would refuse to mint a code for a
// host it did not register as a hand-off client anyway.
func (c *Config) SignInHandoffHosts() []string {
	if c == nil {
		return nil
	}
	cookieHosts := c.SignInCookieHosts()
	hosts := make([]string, 0, len(c.Auth.HandoffClients))
	for _, client := range c.Auth.HandoffClients {
		for _, host := range client.RedirectHosts {
			if HostUnder(host, cookieHosts) {
				continue
			}
			addHost(&hosts, host)
		}
	}
	return hosts
}

// SignInRedirectHosts is every host a finished sign-in may land on: the cookie
// hosts plus the hand-off hosts. Admission uses it; the console uses the split
// above to decide how the person gets there.
func (c *Config) SignInRedirectHosts() []string {
	if c == nil {
		return nil
	}
	hosts := c.SignInCookieHosts()
	for _, host := range c.SignInHandoffHosts() {
		addHost(&hosts, host)
	}
	return hosts
}

// addHost appends a host unless it is empty or already listed, so the same host
// named by the cookie domain and the site URL appears once. Subdomains stay
// separate entries: the published list is a set of origins, not a suffix tree.
func addHost(hosts *[]string, host string) {
	host = normalizeHost(host)
	if host == "" {
		return
	}
	for _, existing := range *hosts {
		if existing == host {
			return
		}
	}
	*hosts = append(*hosts, host)
}

// HostUnder reports whether host is one of allowed or a subdomain of one. Both
// sides are normalized first, so a cookie domain's leading dot and a stray port
// do not change the answer.
func HostUnder(host string, allowed []string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	for _, candidate := range allowed {
		base := normalizeHost(candidate)
		if base == "" {
			continue
		}
		if host == base || strings.HasSuffix(host, "."+base) {
			return true
		}
	}
	return false
}

// SignInRequireHTTPS reports whether a hand-off target must be https. The
// session cookie travels over https only when [auth] cookieSecure is set, so an
// http hand-off would land the app signed out.
func (c *Config) SignInRequireHTTPS() bool {
	return c != nil && c.Auth.CookieSecure
}

// AllowsSignInTarget reports whether a finished sign-in may land on `raw`:
// either a page of this deployment's own site origin (a root-relative path or
// an absolute URL on [siteUrl]) or an origin under a cookie-sharing host.
func (c *Config) AllowsSignInTarget(raw string) bool {
	if c == nil {
		return false
	}
	target := strings.TrimSpace(raw)
	if target == "" || strings.HasPrefix(target, "//") || strings.ContainsAny(target, "\r\n\t") {
		return false
	}
	if c.isSiteOriginTarget(target) {
		return true
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		// A scheme handler (javascript:, data:) or a bare host.
		return false
	}
	if scheme == "http" && c.SignInRequireHTTPS() {
		return false
	}
	return HostUnder(parsed.Host, c.SignInRedirectHosts())
}

// IsSiteOriginTarget reports whether raw names a page of this deployment's own
// site origin: a root-relative path or an absolute URL on [siteUrl]. The
// account-connection flow uses it to keep its `returnUrl` on its own pages.
func (c *Config) IsSiteOriginTarget(raw string) bool {
	if c == nil {
		return false
	}
	target := strings.TrimSpace(raw)
	if target == "" || strings.HasPrefix(target, "//") || strings.ContainsAny(target, "\r\n\t") {
		return false
	}
	return c.isSiteOriginTarget(target)
}

// isSiteOriginTarget accepts a root-relative path or an absolute URL whose host
// and scheme match [siteUrl].
func (c *Config) isSiteOriginTarget(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if parsed.Scheme == "" && parsed.Host == "" {
		return strings.HasPrefix(parsed.Path, "/")
	}
	site, serr := url.Parse(strings.TrimRight(c.SiteUrl, "/"))
	if serr != nil || site.Host == "" {
		return false
	}
	if !strings.EqualFold(parsed.Host, site.Host) {
		return false
	}
	return site.Scheme == "" || strings.EqualFold(parsed.Scheme, site.Scheme)
}

// normalizeHost strips the leading dot a cookie domain may carry (".solian.app"
// and "solian.app" name the same cookie scope), the port, and any trailing dot.
func normalizeHost(host string) string {
	return strings.Trim(hostnameOf(strings.TrimPrefix(strings.TrimSpace(host), ".")), ".")
}

// hostnameOf drops the port from an authority and keeps an IPv6 literal whole.
func hostnameOf(host string) string {
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end >= 0 {
			return strings.ToLower(host[:end+1])
		}
	}
	if idx := strings.LastIndex(host, ":"); idx >= 0 && strings.Count(host, ":") == 1 {
		return strings.ToLower(host[:idx])
	}
	return strings.ToLower(host)
}
