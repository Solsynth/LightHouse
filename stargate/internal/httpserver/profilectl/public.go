package profilectl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Public account routes from Passport's AccountPublicController plus the
// followers/following endpoints.

func (d Deps) getAccountByName(c *gin.Context) {
	name := c.Param("name")
	account, err := d.resolveAccount(c.Request.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, notFound(name))
			return
		}
		internalError(c, err)
		return
	}
	d.enrichPublic(c.Request.Context(), account)
	c.JSON(http.StatusOK, account)
}

func (d Deps) getAccountByID(c *gin.Context) {
	id := c.Param("id")
	account, err := d.resolveAccount(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, notFound(id))
			return
		}
		internalError(c, err)
		return
	}
	d.enrichPublic(c.Request.Context(), account)
	c.JSON(http.StatusOK, account)
}

// searchAccounts ports SearchAccounts: pg_trgm ILIKE search on name/nick.
func (d Deps) searchAccounts(c *gin.Context) {
	query := c.Query("query")
	_, take := parsePagination(c)
	if strings.TrimSpace(query) == "" {
		c.JSON(http.StatusOK, []model.Account{})
		return
	}
	ctx := c.Request.Context()
	accounts, err := d.Store.SearchAccounts(ctx, query, take)
	if err != nil {
		internalError(c, err)
		return
	}
	if len(accounts) > take {
		accounts = accounts[:take]
	}
	for i := range accounts {
		d.enrichSearchResult(ctx, &accounts[i])
	}
	if accounts == nil {
		accounts = []model.Account{}
	}
	c.JSON(http.StatusOK, accounts)
}

// enrichSearchResult mirrors the C# search loop: ensure profile, badges,
// empty contacts, perk subscription.
func (d Deps) enrichSearchResult(ctx context.Context, account *model.Account) {
	if account.Profile == nil {
		if profile, err := d.Store.GetOrCreateAccountProfile(ctx, accountIDOf(account)); err == nil {
			account.Profile = profile
		}
	}
	if d.Clients != nil && d.Clients.Pass != nil {
		if badges, err := d.listBadges(ctx, account.Id); err == nil {
			account.Badges = badges
		}
	}
	account.Contacts = []model.Contact{}
	d.hydratePerk(ctx, account)
}

// getAccountPicture ports GetAccountPicture: 302 to the file URL.
func (d Deps) getAccountPicture(c *gin.Context) {
	name := c.Param("name")
	account, err := d.resolveAccount(c.Request.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, notFound(name))
			return
		}
		internalError(c, err)
		return
	}
	if account.Profile == nil {
		if profile, err := d.Store.GetOrCreateAccountProfile(c.Request.Context(), accountIDOf(account)); err == nil {
			account.Profile = profile
		}
	}
	if account.Profile == nil || account.Profile.Picture == nil {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACCOUNT_PICTURE_NOT_FOUND", "Account picture not found.", http.StatusNotFound))
		return
	}
	c.Redirect(http.StatusFound, fileURL(d.Cfg, account.Profile.Picture))
}

// getAccountBackground ports GetAccountBackground: 302 to the file URL.
func (d Deps) getAccountBackground(c *gin.Context) {
	name := c.Param("name")
	account, err := d.resolveAccount(c.Request.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, notFound(name))
			return
		}
		internalError(c, err)
		return
	}
	if account.Profile == nil {
		if profile, err := d.Store.GetOrCreateAccountProfile(c.Request.Context(), accountIDOf(account)); err == nil {
			account.Profile = profile
		}
	}
	if account.Profile == nil || account.Profile.Background == nil {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_ACCOUNT_BACKGROUND_NOT_FOUND", "Account background not found.", http.StatusNotFound))
		return
	}
	c.Redirect(http.StatusFound, fileURL(d.Cfg, account.Profile.Background))
}

// publicConnectionResponse mirrors PublicAccountConnectionResponse.
type publicConnectionResponse struct {
	Provider           string `json:"provider"`
	ProvidedIdentifier string `json:"provided_identifier"`
	Url                string `json:"url"`
}

// getPublicConnections ports GetPublicConnections (public-only connections).
func (d Deps) getPublicConnections(c *gin.Context) {
	name := c.Param("name")
	account, err := d.resolveAccount(c.Request.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, notFound(name))
			return
		}
		internalError(c, err)
		return
	}
	connections, err := d.Store.ListPublicConnections(c.Request.Context(), accountIDOf(account))
	if err != nil {
		internalError(c, err)
		return
	}
	response := make([]publicConnectionResponse, 0, len(connections))
	for i := range connections {
		response = append(response, publicConnectionResponse{
			Provider:           connections[i].Provider,
			ProvidedIdentifier: connections[i].ProvidedIdentifier,
			Url:                buildPublicConnectionUrl(&connections[i]),
		})
	}
	c.JSON(http.StatusOK, response)
}

// buildPublicConnectionUrl ports the C# steam/github URL building.
func buildPublicConnectionUrl(connection *model.Connection) string {
	if strings.EqualFold(connection.Provider, "steam") {
		return "https://steamcommunity.com/profiles/" + url.PathEscape(connection.ProvidedIdentifier)
	}
	if strings.EqualFold(connection.Provider, "github") {
		if username, ok := metaString(connection.Meta["preferred_username"]); ok && strings.TrimSpace(username) != "" {
			return "https://github.com/" + url.PathEscape(username)
		}
	}
	return ""
}

// metaString mirrors GetMetadataString (string or JSON-encoded string).
func metaString(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		var decoded string
		if err := json.Unmarshal([]byte(v), &decoded); err == nil {
			return decoded, true
		}
		return v, true
	default:
		return "", false
	}
}

// getFollowPage serves GET /api/accounts/{name|me}/followers|following.
// "me" requires auth; other names are public lookups.
func (d Deps) getFollowPage(isFollowing bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("name")
		ctx := c.Request.Context()
		var accountID string
		if name == "me" {
			user := requireCurrentUser(c)
			if user == nil {
				return
			}
			accountID = user.Id
		} else {
			account, err := d.resolveAccount(ctx, name)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					c.JSON(http.StatusNotFound, notFound(name))
					return
				}
				internalError(c, err)
				return
			}
			accountID = account.Id
		}
		offset, take := parsePagination(c)
		var (
			accounts []model.Account
			total    int
			err      error
		)
		if isFollowing {
			accounts, total, err = d.Store.ListFollowing(ctx, middleware.AccountIDOf(accountID), offset, take)
		} else {
			accounts, total, err = d.Store.ListFollowers(ctx, middleware.AccountIDOf(accountID), offset, take)
		}
		if err != nil {
			internalError(c, err)
			return
		}
		c.Header("X-Total", strconv.Itoa(total))
		if accounts == nil {
			accounts = []model.Account{}
		}
		c.JSON(http.StatusOK, accounts)
	}
}
