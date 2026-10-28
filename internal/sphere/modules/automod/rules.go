package automod

import (
	"regexp"
	"strings"
	"sync/atomic"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// RuleType mirrors the C# AutomodRuleType enum.
const (
	RuleTypeLinkBlocklist    = 1
	RuleTypeKeywordBlocklist = 2
)

// RuleAction mirrors the C# AutomodRuleAction enum.
const (
	ActionDerank = 1
	ActionHide   = 2
	ActionFlag   = 3
)

// Rule is the compiled form of one enabled automod rule. The posts pipeline
// consumes it through Evaluate; the API layer converts rows with compileRule.
type Rule struct {
	ID           string
	Name         string
	Type         int
	Action       int
	DerankWeight int
	Pattern      string
	IsRegex      bool

	// expression is the compiled pattern. It is nil when the rule is literal
	// or when the configured pattern does not compile, which makes the rule
	// inert instead of failing the whole evaluation.
	expression *regexp.Regexp
}

// Result is one rule hit: the rule that matched, the action it asks for and the
// text that triggered it.
type Result struct {
	RuleID       string
	RuleName     string
	Type         int
	Action       int
	DerankWeight int
	MatchedText  string
}

// snapshot holds the enabled rules the automod module last published. It is
// replaced wholesale on every rule mutation so readers never see a half-built
// rule set.
var snapshot atomic.Pointer[[]Rule]

// SetRules publishes the enabled rule set Evaluate matches against. The automod
// module calls it after loading rules and after every mutation; other packages
// may call it in tests.
func SetRules(rules []Rule) {
	compiled := make([]Rule, len(rules))
	copy(compiled, rules)
	for idx := range compiled {
		compiled[idx].compile()
	}
	snapshot.Store(&compiled)
}

// Rules returns the currently published rule set.
func Rules() []Rule {
	current := snapshot.Load()
	if current == nil {
		return nil
	}
	return *current
}

// Evaluate matches content against the rule set the automod module published.
// It returns one Result per matching rule in priority order, which is the order
// the rules were loaded in.
func Evaluate(content string) []Result {
	return EvaluateRules(Rules(), content)
}

// EvaluateRules matches content against an explicit rule set. Rules carrying an
// uncompilable regex never match, mirroring the C# service.
func EvaluateRules(rules []Rule, content string) []Result {
	if len(rules) == 0 || content == "" {
		return nil
	}
	results := make([]Result, 0, len(rules))
	for _, rule := range rules {
		matched, ok := matchRule(rule, content)
		if !ok {
			continue
		}
		results = append(results, Result{
			RuleID:       rule.ID,
			RuleName:     rule.Name,
			Type:         rule.Type,
			Action:       rule.Action,
			DerankWeight: rule.DerankWeight,
			MatchedText:  matched,
		})
	}
	return results
}

// Penalty is the aggregate moderation effect of a rule hit: the derank weight
// to subtract and whether the content must be hidden.
type Penalty struct {
	Weight float64
	Hide   bool
}

// Summarize folds rule hits into one penalty, mirroring the C# automod service:
// Hide adds 100 to the penalty and sets the hide flag, Derank adds its weight
// and Flag leaves the content untouched.
func Summarize(results []Result) Penalty {
	var penalty Penalty
	for _, result := range results {
		switch result.Action {
		case ActionHide:
			penalty.Hide = true
			penalty.Weight += 100
		case ActionDerank:
			penalty.Weight += float64(result.DerankWeight)
		}
	}
	return penalty
}

// compileRule builds the compiled form of a stored rule.
func compileRule(entity *store.AutomodRuleEntity) Rule {
	rule := Rule{
		ID:           entity.ID.String(),
		Name:         entity.Name,
		Type:         entity.Type,
		Action:       entity.DefaultAction,
		DerankWeight: entity.DerankWeight,
		Pattern:      entity.Pattern,
		IsRegex:      entity.IsRegex,
	}
	rule.compile()
	return rule
}

func (r *Rule) compile() {
	if !r.IsRegex || r.Pattern == "" {
		r.expression = nil
		return
	}
	// C# compiles with RegexOptions.IgnoreCase; (?i) is the RE2 equivalent.
	expression, err := regexp.Compile("(?i)" + r.Pattern)
	if err != nil {
		r.expression = nil
		return
	}
	r.expression = expression
}

// matchRule reports the matched text for a rule. Literal rules match
// case-insensitively and report the configured pattern, exactly like the C#
// implementation.
func matchRule(rule Rule, content string) (string, bool) {
	if rule.Pattern == "" {
		return "", false
	}
	if !rule.IsRegex {
		if strings.Contains(strings.ToLower(content), strings.ToLower(rule.Pattern)) {
			return rule.Pattern, true
		}
		return "", false
	}
	if rule.expression == nil {
		return "", false
	}
	match := rule.expression.FindString(content)
	if match == "" {
		return "", false
	}
	return match, true
}

// ParseType maps the configuration strings to the enum values, defaulting to
// KeywordBlocklist like the C# boot seeder.
func ParseType(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "linkblocklist", "link_blocklist", "link":
		return RuleTypeLinkBlocklist
	default:
		return RuleTypeKeywordBlocklist
	}
}

// ParseAction maps the configuration strings to the enum values, defaulting to
// Derank like the C# boot seeder.
func ParseAction(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hide":
		return ActionHide
	case "flag":
		return ActionFlag
	default:
		return ActionDerank
	}
}
