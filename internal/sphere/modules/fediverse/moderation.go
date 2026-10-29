package fediverse

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/config"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// FediverseModerationRuleType values mirror the C# enum.
const (
	ruleDomainBlock     = 1
	ruleDomainAllow     = 2
	ruleKeywordBlock    = 3
	ruleKeywordAllow    = 4
	ruleReportThreshold = 5
	ruleActorSuspend    = 6
	ruleActorAllow      = 7
)

// FediverseModerationAction values mirror the C# enum.
const (
	actionBlock   = 1
	actionAllow   = 2
	actionSilence = 3
	actionSuspend = 4
	actionFlag    = 5
	actionDerank  = 6
)

// moderationResult mirrors FediverseModerationResult.
type moderationResult struct {
	IsBlocked       bool    `json:"is_blocked"`
	IsSilenced      bool    `json:"is_silenced"`
	IsSuspended     bool    `json:"is_suspended"`
	IsDeranked      bool    `json:"is_deranked"`
	ShouldFlag      bool    `json:"should_flag"`
	MatchedRuleName *string `json:"matched_rule_name"`
	MatchedDomain   *string `json:"matched_domain"`
	MatchedKeyword  *string `json:"matched_keyword"`
	Action          *int    `json:"action"`
}

type moderationService struct {
	db     *gorm.DB
	log    *slog.Logger
	mu     sync.Mutex
	cached []store.FediverseModerationRuleEntity
}

func newModerationService(db *gorm.DB, log *slog.Logger) *moderationService {
	return &moderationService{db: db, log: log}
}

func (m *moderationService) invalidate() {
	m.mu.Lock()
	m.cached = nil
	m.mu.Unlock()
}

func (m *moderationService) rules(ctx context.Context) []store.FediverseModerationRuleEntity {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached != nil {
		return m.cached
	}
	var rows []store.FediverseModerationRuleEntity
	now := time.Now().UTC()
	if err := m.db.WithContext(ctx).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Order("priority ASC").
		Find(&rows).Error; err != nil {
		m.log.Warn("failed to load fediverse moderation rules", "error", err)
		return nil
	}
	m.cached = rows
	return rows
}

func (m *moderationService) checkDomain(ctx context.Context, domain string) moderationResult {
	return m.checkInstance(ctx, domain, false)
}

func (m *moderationService) checkInstance(ctx context.Context, domain string, includeAll bool) moderationResult {
	result := moderationResult{}
	if strings.TrimSpace(domain) == "" {
		return result
	}
	lowered := strings.ToLower(domain)
	for _, rule := range m.rules(ctx) {
		if !rule.IsEnabled || (!includeAll && rule.IsSystemRule) {
			continue
		}
		switch rule.Type {
		case ruleDomainBlock:
			if rule.Domain != nil && matchesPattern(lowered, strings.ToLower(*rule.Domain), rule.IsRegex) {
				result.IsBlocked = true
				result.MatchedRuleName = &rule.Name
				result.MatchedDomain = &domain
				action := rule.Action
				result.Action = &action
				applyAction(&result, rule.Action)
				return result
			}
		case ruleDomainAllow:
			if includeAll && rule.Domain != nil && matchesPattern(lowered, strings.ToLower(*rule.Domain), rule.IsRegex) {
				return result
			}
		}
	}
	return result
}

func (m *moderationService) checkActor(ctx context.Context, actorURI, content, actorDomain string) moderationResult {
	var result moderationResult
	domain := actorDomain
	if domain == "" {
		domain = hostOfURL(actorURI)
	}
	if domain != "" {
		result = m.checkDomain(ctx, domain)
		if result.IsBlocked || result.IsSilenced || result.IsSuspended {
			return result
		}
	}
	if content == "" {
		return result
	}
	for _, rule := range m.rules(ctx) {
		if !rule.IsEnabled || rule.IsSystemRule {
			continue
		}
		if (rule.Type != ruleKeywordBlock && rule.Type != ruleKeywordAllow) || rule.KeywordPattern == nil {
			continue
		}
		if matchesPattern(content, *rule.KeywordPattern, rule.IsRegex) {
			result.ShouldFlag = true
			result.MatchedRuleName = &rule.Name
			result.MatchedKeyword = rule.KeywordPattern
			action := rule.Action
			result.Action = &action
			applyAction(&result, rule.Action)
			return result
		}
	}
	return result
}

// checkActorRule applies per-actor suspend/allow rules; an allow rule always
// wins so operators can carve an actor out of a domain rule.
func (m *moderationService) checkActorRule(ctx context.Context, actorURI string) moderationResult {
	var result moderationResult
	if actorURI == "" {
		return result
	}
	rules := m.rules(ctx)
	for _, rule := range rules {
		if !rule.IsEnabled || rule.Domain == nil || rule.Type != ruleActorAllow {
			continue
		}
		if matchesPattern(actorURI, *rule.Domain, rule.IsRegex) {
			return result
		}
	}
	for _, rule := range rules {
		if !rule.IsEnabled || rule.Domain == nil || rule.Type != ruleActorSuspend {
			continue
		}
		if matchesPattern(actorURI, *rule.Domain, rule.IsRegex) {
			result.IsSuspended = true
			result.IsBlocked = true
			result.MatchedRuleName = &rule.Name
			domain := hostOfURL(actorURI)
			result.MatchedDomain = &domain
			action := rule.Action
			result.Action = &action
			applyAction(&result, rule.Action)
			return result
		}
	}
	return result
}

func applyAction(result *moderationResult, action int) {
	switch action {
	case actionBlock:
		result.IsBlocked = true
	case actionSilence:
		result.IsSilenced = true
	case actionSuspend:
		result.IsSuspended = true
	case actionFlag:
		result.ShouldFlag = true
	case actionDerank:
		result.IsDeranked = true
	}
}

func matchesPattern(input, pattern string, isRegex bool) bool {
	if isRegex {
		compiled, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return false
		}
		return compiled.MatchString(input)
	}
	return strings.Contains(strings.ToLower(input), strings.ToLower(pattern))
}

func matchesActorURI(actorURI, pattern string, isRegex bool) bool {
	if isRegex {
		return matchesPattern(actorURI, pattern, true)
	}
	return strings.EqualFold(strings.TrimRight(actorURI, "/"), strings.TrimRight(pattern, "/"))
}

func hostOfURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// seedModerationRules upserts the configured rules by name, like
// InitializeFediverseModerationRulesAsync.
func (s *server) seedModerationRules(ctx context.Context, seeds []config.FediverseRuleSeed) error {
	if len(seeds) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for _, seed := range seeds {
		name := strings.TrimSpace(seed.Name)
		if name == "" {
			continue
		}
		ruleType := parseRuleType(seed.Type)
		ruleAction := parseRuleAction(seed.Action)

		var existing store.FediverseModerationRuleEntity
		err := s.db.WithContext(ctx).Where("lower(name) = ?", strings.ToLower(name)).First(&existing).Error
		description := stringPtr(seed.Description)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			rule := store.FediverseModerationRuleEntity{
				ID:           uuid.New(),
				Name:         name,
				Description:  description,
				Type:         ruleType,
				Action:       ruleAction,
				Domain:       stringPtr(seed.Domain),
				IsEnabled:    seed.IsEnabled,
				Priority:     seed.Priority,
				IsSystemRule: true,
				EntityBase: store.EntityBase{
					CreatedAt: now,
					UpdatedAt: now,
				},
			}
			if createErr := s.db.WithContext(ctx).Create(&rule).Error; createErr != nil {
				return createErr
			}
			continue
		}
		if err != nil {
			return err
		}
		existing.Description = description
		existing.Type = ruleType
		existing.Action = ruleAction
		existing.Domain = stringPtr(seed.Domain)
		existing.IsEnabled = seed.IsEnabled
		existing.Priority = seed.Priority
		existing.IsSystemRule = true
		existing.UpdatedAt = now
		if saveErr := s.db.WithContext(ctx).Save(&existing).Error; saveErr != nil {
			return saveErr
		}
	}
	s.moderation.invalidate()
	return nil
}

func parseRuleType(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "domainblock", "domain_block", "block":
		return ruleDomainBlock
	case "domainallow", "domain_allow", "allow":
		return ruleDomainAllow
	case "keywordblock", "keyword_block", "keyword":
		return ruleKeywordBlock
	case "keywordallow", "keyword_allow":
		return ruleKeywordAllow
	case "reportthreshold", "report_threshold":
		return ruleReportThreshold
	case "actorsuspend", "actor_suspend":
		return ruleActorSuspend
	case "actorallow", "actor_allow":
		return ruleActorAllow
	default:
		return ruleDomainBlock
	}
}

func parseRuleAction(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "block":
		return actionBlock
	case "allow":
		return actionAllow
	case "silence":
		return actionSilence
	case "suspend":
		return actionSuspend
	case "flag":
		return actionFlag
	case "derank":
		return actionDerank
	default:
		return actionFlag
	}
}
