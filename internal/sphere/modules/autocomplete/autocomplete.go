// Package autocomplete ports DysonNetwork.Sphere/Autocompletion: the
// suggestion endpoint and the gRPC service that Messager calls. Suggestions
// come from the accounts port when it is available and always fall back to the
// local publishers, stickers and tags.
package autocomplete

import (
	"context"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/accountjson"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
)

// ModuleID is the autocomplete module's registry ID.
const ModuleID = "app.solian.sphere.autocomplete"

// PostsModuleID is the module whose tables (posts, tags) this feature reads.
const PostsModuleID = "app.solian.sphere.posts"

// DefaultLimit is the suggestion count used when the caller sends none,
// matching the C# service default.
const DefaultLimit = 10

// Factory returns the autocomplete module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{PostsModuleID},
		Ports:          []module.PortRequirement{{ID: ports.PortAccounts, Optional: true}},
		Capabilities:   []string{"autocomplete.text"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core     *core.Services
	db       *gorm.DB
	log      *slog.Logger
	accounts ports.Accounts
}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	i.db = host.Core.DB
	i.log = host.Log
	// The accounts port is optional: without it @-mentions degrade to the local
	// publisher table.
	i.accounts, _ = module.Get[ports.Accounts](host.Ports, ports.PortAccounts)

	host.API.POST("/autocomplete", authn.RequireAuth(), i.textAutocomplete)
	gen.RegisterDyAutocompletionServiceServer(host.GRPC, &grpcServer{svc: i})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

// autocompletionRequest mirrors the shared AutocompletionRequest. chat_id and
// realm_id are accepted for wire compatibility; this port publishes no chat or
// realm scoped source, so they do not narrow the search.
type autocompletionRequest struct {
	Content string  `json:"content"`
	ChatID  *string `json:"chat_id"`
	RealmID *string `json:"realm_id"`
	Limit   *int    `json:"limit"`
}

func (i *instance) textAutocomplete(c *gin.Context) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return
	}

	var request autocompletionRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	// AutocompletionRequest.Content is [Required] in the C# contract, so an
	// empty composer value is a validation failure rather than an empty page.
	if strings.TrimSpace(request.Content) == "" {
		httpx.Fail(c, errs.Validation(map[string][]string{
			"content": {"The Content field is required."},
		}))
		return
	}

	limit := DefaultLimit
	if request.Limit != nil && *request.Limit > 0 {
		limit = *request.Limit
	}

	results := i.complete(c.Request.Context(), request.Content, limit)
	httpx.List(c, results, int64(len(results)))
}

// complete resolves suggestions for the content the composer currently holds.
// "@" prefixes mention autocompletion, ":" sticker packs and "#" tags; anything
// else yields no suggestions, matching the C# service.
func (i *instance) complete(ctx context.Context, content string, limit int) []model.Autocompletion {
	if strings.TrimSpace(content) == "" {
		return []model.Autocompletion{}
	}
	limit = i.clampLimit(limit)

	switch content[0] {
	case '@':
		return i.completeMention(ctx, content[1:], limit)
	case ':':
		return i.completeSticker(ctx, content[1:], limit)
	case '#':
		return i.completeTag(ctx, content[1:], limit)
	default:
		return []model.Autocompletion{}
	}
}

// clampLimit keeps a caller-supplied limit inside the configured pagination cap.
func (i *instance) clampLimit(limit int) int {
	limitMax := 100
	if i.core != nil && i.core.Config != nil && i.core.Config.Security.MaxTake > 0 {
		limitMax = i.core.Config.Security.MaxTake
	}
	if limit <= 0 {
		return DefaultLimit
	}
	if limit > limitMax {
		return limitMax
	}
	return limit
}

// completeMention handles "@query" and "@type/query". The type segment selects
// the source: u accounts, p publishers, r realms (unavailable in this port).
func (i *instance) completeMention(ctx context.Context, body string, limit int) []model.Autocompletion {
	kind := "u"
	query := body
	hadSlash := strings.Contains(body, "/")
	if hadSlash {
		parts := strings.SplitN(body, "/", 2)
		kind = parts[0]
		query = ""
		if len(parts) > 1 {
			query = parts[1]
		}
	}

	switch kind {
	case "u":
		if suggestions := i.completeAccount(ctx, query, hadSlash, limit); suggestions != nil {
			return suggestions
		}
		// No accounts port (or it failed): degrade to local publisher matches.
		return i.completePublisher(ctx, query, limit)
	case "p":
		return i.completePublisher(ctx, query, limit)
	default:
		return []model.Autocompletion{}
	}
}

// completeAccount queries the accounts port. It returns nil when the port is
// absent or failed so the caller can fall back to local data.
func (i *instance) completeAccount(ctx context.Context, query string, hadSlash bool, limit int) []model.Autocompletion {
	if i.accounts == nil {
		return nil
	}
	accounts, err := i.accounts.SearchAccounts(ctx, query, limit)
	if err != nil {
		if i.log != nil {
			i.log.Warn("search accounts for autocomplete", "error", err)
		}
		return nil
	}

	results := make([]model.Autocompletion, 0, len(accounts))
	for _, account := range accounts {
		if account == nil || account.GetName() == "" {
			continue
		}
		keyword := "@" + account.GetName()
		if hadSlash {
			keyword = "@u/" + account.GetName()
		}
		results = append(results, model.Autocompletion{
			Type:    "user",
			Keyword: keyword,
			Data:    accountjson.FromProto(account),
		})
	}
	return results
}
