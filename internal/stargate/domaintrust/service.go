package domaintrust

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ValidationResult is DomainValidationResult. Null fields are serialized,
// matching System.Text.Json's default (no ignore condition).
type ValidationResult struct {
	IsAllowed     bool       `json:"is_allowed"`
	IsVerified    bool       `json:"is_verified"`
	BlockReason   *string    `json:"block_reason"`
	MatchedRule   *BlockView `json:"matched_rule"`
	MatchedSource *string    `json:"matched_source"`
}

// Service evaluates URLs against the rule table and tracks metrics.
type Service struct {
	Store    *Store
	Settings Settings
	Now      func() time.Time

	metricsMu sync.Mutex
	pending   map[string]*metricDelta
}

// NewService builds the service.
func NewService(store *Store, settings Settings) *Service {
	return &Service{Store: store, Settings: settings, Now: time.Now, pending: map[string]*metricDelta{}}
}

type metricDelta struct {
	checks   int64
	blocked  int64
	verified int64
	lastSeen time.Time
}

// ValidateURL mirrors DomainTrustService.ValidateUrlAsync.
func (s *Service) ValidateURL(ctx context.Context, rawURL string) (ValidationResult, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return blockedResult("Invalid URL format", nil, nil), nil
	}
	host := strings.ToLower(parsed.Hostname())
	scheme := strings.ToLower(parsed.Scheme)
	port := effectivePort(parsed)

	if s.Settings.BlockHTTPByDefault && scheme == "http" {
		httpRule, err := s.findHTTPRule(ctx)
		if err != nil {
			return ValidationResult{}, err
		}
		source := "default_http_block"
		return ValidationResult{
			IsAllowed:     false,
			BlockReason:   new("HTTP protocol is blocked by default (not secure)"),
			MatchedSource: &source,
			MatchedRule:   httpRule,
		}, nil
	}

	if s.Settings.BlockIPAddressesByDefault && IsIPAddress(host) {
		if s.Settings.BlockPrivateNetworksByDefault && IsPrivateNetwork(host) {
			source := "default_private_network_block"
			return ValidationResult{
				IsAllowed:     false,
				BlockReason:   new("Private network addresses are blocked by default"),
				MatchedSource: &source,
			}, nil
		}
		source := "default_ip_block"
		return ValidationResult{
			IsAllowed:     false,
			BlockReason:   new("Direct IP addresses are blocked by default"),
			MatchedSource: &source,
		}, nil
	}

	if port != nil && containsPort(s.Settings.blockedPorts(), *port) {
		source := "default_port_block"
		return ValidationResult{
			IsAllowed:     false,
			BlockReason:   new("Port " + strconv.Itoa(*port) + " is blocked by default"),
			MatchedSource: &source,
		}, nil
	}

	rule, err := s.FindMatchingRule(ctx, host, scheme, port)
	if err != nil {
		return ValidationResult{}, err
	}
	if rule != nil {
		source := "database_rule"
		view := BlockViewFrom(rule)
		if rule.TrustLevel == int(TrustBlocked) {
			reason := "Domain is blocked"
			if rule.Reason != nil && *rule.Reason != "" {
				reason = *rule.Reason
			}
			return ValidationResult{
				IsAllowed:     false,
				IsVerified:    false,
				BlockReason:   &reason,
				MatchedRule:   &view,
				MatchedSource: &source,
			}, nil
		}
		return ValidationResult{
			IsAllowed:     true,
			IsVerified:    rule.TrustLevel == int(TrustVerified),
			MatchedRule:   &view,
			MatchedSource: &source,
		}, nil
	}

	return ValidationResult{IsAllowed: true}, nil
}

func blockedResult(reason string, rule *BlockView, source *string) ValidationResult {
	return ValidationResult{
		IsAllowed:     false,
		BlockReason:   &reason,
		MatchedRule:   rule,
		MatchedSource: source,
	}
}

// findHTTPRule is the rule the C# HTTP default block reports as its match: an
// active `http` rule for `*`, if one exists.
func (s *Service) findHTTPRule(ctx context.Context) (*BlockView, error) {
	rules, err := s.Store.ActiveBlocks(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rules {
		if rules[i].DomainPattern == "*" && rules[i].Protocol != nil &&
			strings.EqualFold(*rules[i].Protocol, "http") {
			view := BlockViewFrom(&rules[i])
			return &view, nil
		}
	}
	return nil, nil
}

// FindMatchingRule mirrors DomainTrustService.FindMatchingRuleAsync. The port
// is nil for a default port, which is why a rule with PortRestriction set does
// not match a URL without an explicit port.
func (s *Service) FindMatchingRule(ctx context.Context, host, scheme string, port *int) (*DomainBlockEntity, error) {
	rules, err := s.Store.ActiveBlocks(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rules {
		if matchesRule(&rules[i], host, scheme, port) {
			return &rules[i], nil
		}
	}
	return nil, nil
}

func matchesRule(rule *DomainBlockEntity, host, scheme string, port *int) bool {
	if rule.Protocol != nil && *rule.Protocol != "" && scheme != "" &&
		!strings.EqualFold(*rule.Protocol, scheme) {
		return false
	}
	if rule.PortRestriction != nil {
		if port == nil || *rule.PortRestriction != *port {
			return false
		}
	}
	return MatchesDomainPattern(rule.DomainPattern, host)
}

// MatchesDomainPattern is DomainTrustService.MatchesDomainPattern.
func MatchesDomainPattern(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(strings.TrimSpace(host))
	if pattern == "" || host == "" {
		return false
	}
	switch {
	case pattern == "*":
		return true
	case strings.HasPrefix(pattern, "**."):
		base := pattern[3:]
		return host == base || strings.HasSuffix(host, "."+base)
	case strings.HasPrefix(pattern, "*."):
		base := pattern[2:]
		return strings.HasSuffix(host, "."+base)
	}
	if strings.Contains(pattern, "*") || strings.Contains(pattern, "?") {
		expression := regexp.QuoteMeta(pattern)
		expression = strings.ReplaceAll(expression, `\*`, `.*`)
		expression = strings.ReplaceAll(expression, `\?`, `.`)
		matched, err := regexp.MatchString("(?i)^"+expression+"$", host)
		return err == nil && matched
	}
	return strings.EqualFold(pattern, host)
}

// effectivePort mirrors Uri.IsDefaultPort: an omitted or scheme-default port
// is nil.
func effectivePort(parsed *url.URL) *int {
	raw := parsed.Port()
	if raw == "" {
		return nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		if port == 80 {
			return nil
		}
	case "https":
		if port == 443 {
			return nil
		}
	}
	return &port
}

// ─────────────────────────── metrics ───────────────────────────

// RecordValidation queues one metric sample, mirroring the C# controller's
// FlushBufferService enqueue. Only the HTTP surface tracks metrics.
func (s *Service) RecordValidation(rawURL string, result ValidationResult) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !parsed.IsAbs() {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if domain == "" {
		return
	}
	now := s.now()
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	delta, ok := s.pending[domain]
	if !ok {
		delta = &metricDelta{}
		s.pending[domain] = delta
	}
	delta.checks++
	if !result.IsAllowed {
		delta.blocked++
	}
	if result.IsVerified {
		delta.verified++
	}
	if now.After(delta.lastSeen) {
		delta.lastSeen = now
	}
}

// FlushMetrics writes the queued deltas. It is safe to call concurrently with
// RecordValidation; the map is swapped under the lock so slow writes do not
// block checks.
func (s *Service) FlushMetrics(ctx context.Context) error {
	s.metricsMu.Lock()
	pending := s.pending
	s.pending = map[string]*metricDelta{}
	s.metricsMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	var firstErr error
	for domain, delta := range pending {
		if delta.checks == 0 {
			continue
		}
		if err := s.Store.IncrementMetric(ctx, domain, delta.blocked == 0, delta.verified > 0, delta.lastSeen); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// PendingMetricCount reports queued domains; used by tests.
func (s *Service) PendingMetricCount() int {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	return len(s.pending)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ─────────────────────────── CRUD ───────────────────────────

// RuleInput is the create/update payload after binding.
type RuleInput struct {
	DomainPattern   *string `json:"domain_pattern"`
	Protocol        *string `json:"protocol"`
	PortRestriction *int    `json:"port_restriction"`
	Reason          *string `json:"reason"`
	Priority        *int    `json:"priority"`
	TrustLevel      *int    `json:"trust_level"`
	IsActive        *bool   `json:"is_active"`
}

// CreateRule mirrors DomainTrustService.CreateRuleAsync.
func (s *Service) CreateRule(ctx context.Context, input RuleInput, creator *uuid.UUID) (*DomainBlockEntity, error) {
	row := &DomainBlockEntity{
		ID:                 uuid.New(),
		DomainPattern:      derefOrEmpty(input.DomainPattern),
		Protocol:           input.Protocol,
		PortRestriction:    input.PortRestriction,
		Reason:             input.Reason,
		Priority:           0,
		TrustLevel:         int(TrustBlocked),
		IsActive:           true,
		CreatedByAccountID: creator,
	}
	if input.Priority != nil {
		row.Priority = *input.Priority
	}
	if input.TrustLevel != nil {
		row.TrustLevel = *input.TrustLevel
	}
	if input.IsActive != nil {
		row.IsActive = *input.IsActive
	}
	if err := s.Store.CreateBlock(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// UpdateRule applies a partial update, mirroring the C# Action<> patch.
func (s *Service) UpdateRule(ctx context.Context, id uuid.UUID, input RuleInput) (*DomainBlockEntity, error) {
	row, err := s.Store.GetBlock(ctx, id)
	if err != nil {
		return nil, err
	}
	if input.DomainPattern != nil {
		row.DomainPattern = *input.DomainPattern
	}
	if input.Protocol != nil {
		row.Protocol = input.Protocol
	}
	if input.PortRestriction != nil {
		row.PortRestriction = input.PortRestriction
	}
	if input.Reason != nil {
		row.Reason = input.Reason
	}
	if input.Priority != nil {
		row.Priority = *input.Priority
	}
	if input.TrustLevel != nil {
		row.TrustLevel = *input.TrustLevel
	}
	if input.IsActive != nil {
		row.IsActive = *input.IsActive
	}
	if err := s.Store.SaveBlock(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// IsNotFound reports a missing rule/metric.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
