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

// SignInRedirectHosts lists the hosts a finished sign-in may be handed to:
// the cookie domain, this deployment's own site host, and every host a
// hand-off client is registered for. The cookie hosts can complete a shared
// sign-in with no further step; the hand-off hosts need one, because no cookie
// can reach them (see internal/httpserver/authctl/handoff.go). The console
// fetches this list so it can refuse a hand-off the server would refuse anyway
// instead of navigating away and losing the session.
func (c *Config) SignInRedirectHosts() []string {
	if c == nil {
		return nil
	}
	hosts := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(host string) {
		host = normalizeHost(host)
		if host == "" {
			return
		}
		if _, dup := seen[host]; dup {
			return
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	add(c.Auth.CookieDomain)
	if site, err := url.Parse(strings.TrimRight(c.SiteUrl, "/")); err == nil {
		add(site.Host)
	}
	for _, client := range c.Auth.HandoffClients {
		for _, host := range client.RedirectHosts {
			add(host)
		}
	}
	return hosts
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
	host := hostnameOf(parsed.Host)
	for _, allowed := range c.SignInRedirectHosts() {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
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
