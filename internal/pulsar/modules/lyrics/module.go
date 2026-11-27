// Package lyrics shares what listeners know about a recording's words and
// metadata: their own picked lyrics, and their corrections to the catalogue
// record behind it.
//
// The library mirror (librarysync) is one account's private copy. This module
// is the other direction — a contribution one account makes and every other
// listener can read, ranked by what those listeners kept. A lyric that came
// from LRCLIB can be re-fetched by anyone, so its contribution earns standing
// from adoption and votes rather than from being first; a lyric someone typed
// by hand is the only copy in existence, which is the case this module exists
// for.
package lyrics

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.lyrics",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         model.LyricsContributionModels(),
		SQL: []module.SchemaHook{module.DDL(
			// The ranked listing for one recording: every suggestion query
			// starts from (catalog, language) and reads the standing off the
			// index rather than sorting a table.
			`CREATE INDEX IF NOT EXISTS ix_shared_lyrics_rank
				ON shared_lyrics (catalog_id, language, score DESC)`,
			`CREATE INDEX IF NOT EXISTS ix_meta_sugg_rank
				ON metadata_suggestions (catalog_id, field, score DESC)`,
		)},
		Capabilities: []string{"lyrics", "lyrics.contributions", "metadata.suggestions"},
		New:          func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core  *core.Services
	store *store.Store
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	i.store = host.Core.Store

	// Reading is public: a share page or a signed-out client still benefits
	// from what listeners agreed on, and the ranking is a shared artefact
	// rather than any one account's data.
	//
	// /lyrics/catalog names the id a client needs before it can ask for
	// anything: it holds a track key, not a catalog uuid, and the catalog
	// module's list endpoint pages by title rather than by that key.
	host.API.GET("/lyrics/catalog", i.resolveCatalog)
	host.API.GET("/lyrics/suggestions", i.listLyrics)
	host.API.GET("/lyrics/:id", i.getLyric)

	// Writing needs an account: a contribution is an opinion attributed to
	// somebody, and a vote from nobody would let one client stuff the ballot.
	host.API.PUT("/lyrics", authn.RequireAuth(), i.putLyric)
	host.API.DELETE("/lyrics/:id", authn.RequireAuth(), i.deleteLyric)
	host.API.POST("/lyrics/:id/adopt", authn.RequireAuth(), i.adoptLyric)
	host.API.POST("/lyrics/:id/vote", authn.RequireAuth(), i.voteLyric)

	// Metadata corrections ride the same rules: public to read, gated to
	// propose. They live outside the /metadata group the metadata module owns
	// because they are contributions, not resolved upstream records.
	host.API.GET("/metadata/suggestions", i.listSuggestions)
	host.API.GET("/metadata/suggestions/leaders", i.listLeaders)
	host.API.PUT("/metadata/suggestions", authn.RequireAuth(), i.putSuggestion)
	host.API.POST("/metadata/suggestions/:id/vote", authn.RequireAuth(), i.voteSuggestion)

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

// ─────────────────────────── Lyrics ───────────────────────────

// lyricContribution is one suggestion as the client reads it.
type lyricContribution struct {
	model.SharedLyric
	// Mine is true when the caller contributed this row, so a client can
	// offer "edit" instead of "vote".
	Mine bool `json:"mine"`
}

type putLyricRequest struct {
	CatalogID uuid.UUID `json:"catalog_id" binding:"required"`
	// Language is the language of the words; empty means "not stated".
	Language string `json:"language,omitempty"`
	// Lines is the client's JSON line array. Non-empty is required: an empty
	// lyric is a deletion, and deletion has its own verb.
	Lines               string  `json:"lines" binding:"required"`
	Synced              bool    `json:"synced,omitempty"`
	TranslationLanguage *string `json:"translation_language,omitempty"`
	TranslationEngine   *string `json:"translation_engine,omitempty"`
	Origin              string  `json:"origin,omitempty"`
	RemoteID            *int64  `json:"remote_id,omitempty"`
}

func (i *instance) putLyric(c *gin.Context) {
	accountID, ok := caller(c)
	if !ok {
		return
	}

	var req putLyricRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	if strings.TrimSpace(req.Lines) == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "lines cannot be empty")
		return
	}
	if len(req.Lines) > maxLyricBytes {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "lines exceeds the maximum size")
		return
	}
	// The record must exist: a contribution filed under an id nobody can
	// resolve would never be read again.
	if _, err := i.store.GetCatalogItem(c.Request.Context(), req.CatalogID); err != nil {
		httpx.NotFound(c, "catalog item not found")
		return
	}

	origin := strings.ToLower(strings.TrimSpace(req.Origin))
	switch origin {
	case model.LyricOriginLRCLIB, model.LyricOriginManual, model.LyricOriginFile, model.LyricOriginOther:
	case "":
		origin = model.LyricOriginManual
	default:
		httpx.BadRequest(c, "INVALID_ARGUMENT", "origin must be one of lrclib, manual, file, other")
		return
	}

	contribution := &model.SharedLyric{
		CatalogID:           req.CatalogID,
		AccountID:           accountID,
		Language:            strings.ToLower(strings.TrimSpace(req.Language)),
		Lines:               req.Lines,
		Synced:              req.Synced,
		TranslationLanguage: req.TranslationLanguage,
		TranslationEngine:   req.TranslationEngine,
		Origin:              origin,
		RemoteID:            req.RemoteID,
	}
	saved, created, err := i.store.UpsertSharedLyric(c.Request.Context(), contribution)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if created {
		httpx.Created(c, saved)
		return
	}
	httpx.OK(c, saved)
}

// maxLyricBytes caps one contribution. A karaoke page of a long track is tens
// of kilobytes; a megabyte is not a lyric but a payload nobody reads.
const maxLyricBytes = 512 * 1024

func (i *instance) getLyric(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	lyric, err := i.store.GetSharedLyric(c.Request.Context(), id)
	if err != nil {
		httpx.NotFound(c, "lyric not found")
		return
	}
	httpx.OK(c, lyricContribution{SharedLyric: *lyric, Mine: mineOf(c, lyric.AccountID)})
}

func (i *instance) listLyrics(c *gin.Context) {
	page, ok := httpx.PageQuery(c, i.core.Config.Security.MaxTake)
	if !ok {
		return
	}
	catalogID, ok := queryID(c, "catalog_id")
	if !ok {
		return
	}

	query := store.SharedLyricQuery{
		CatalogID:  catalogID,
		Language:   c.Query("language"),
		SyncedOnly: c.Query("synced") == "true" || c.Query("synced_only") == "true",
	}
	if c.Query("exclude_me") == "true" {
		if accountID, ok := callerID(c); ok {
			query.ExcludeAccount = &accountID
		}
	}

	rows, total, err := i.store.ListSharedLyrics(c.Request.Context(), query, page.Offset, page.Take)
	if err != nil {
		httpx.Internal(c)
		return
	}
	items := make([]lyricContribution, 0, len(rows))
	for _, row := range rows {
		items = append(items, lyricContribution{SharedLyric: row, Mine: mineOf(c, row.AccountID)})
	}
	httpx.List(c, items, total)
}

// resolveCatalog answers the id behind a track key, so a client can ask for a
// recording's suggestions without holding its uuid. A key that has never been
// written has no record: the caller shares the track first, which is the same
// write a share link performs.
func (i *instance) resolveCatalog(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider == "" {
		provider = "groovybox"
	}
	key := strings.TrimSpace(c.Query("provider_key"))
	if key == "" {
		key = strings.TrimSpace(c.Query("key"))
	}
	if key == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "a provider_key query parameter is required")
		return
	}
	item, err := i.store.FindCatalogItemByProviderKey(c.Request.Context(), provider, key)
	if err != nil {
		httpx.NotFound(c, "catalog item not found")
		return
	}
	httpx.OK(c, item)
}

func (i *instance) deleteLyric(c *gin.Context) {
	accountID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := i.store.DeleteSharedLyric(c.Request.Context(), accountID, id); err != nil {
		httpx.NotFound(c, "lyric not found")
		return
	}
	httpx.NoContent(c)
}

func (i *instance) adoptLyric(c *gin.Context) {
	accountID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	lyric, err := i.store.AdoptSharedLyric(c.Request.Context(), accountID, id)
	if err != nil {
		notFoundOrInternal(c, err)
		return
	}
	httpx.OK(c, lyric)
}

// voteBody is one thumb. value is 1 or -1.
type voteBody struct {
	Value int `json:"value" binding:"required"`
}

func (i *instance) voteLyric(c *gin.Context) {
	accountID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var body voteBody
	if !httpx.BindJSON(c, &body) {
		return
	}
	lyric, err := i.store.VoteSharedLyric(c.Request.Context(), accountID, id, body.Value)
	if err != nil {
		suggestionFailure(c, err)
		return
	}
	httpx.OK(c, lyric)
}

// ─────────────────────────── Metadata suggestions ───────────────────────────

type putSuggestionRequest struct {
	CatalogID uuid.UUID `json:"catalog_id" binding:"required"`
	Field     string    `json:"field" binding:"required"`
	Value     string    `json:"value" binding:"required"`
}

func (i *instance) putSuggestion(c *gin.Context) {
	accountID, ok := caller(c)
	if !ok {
		return
	}
	var req putSuggestionRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	field := strings.ToLower(strings.TrimSpace(req.Field))
	if !model.MetadataFields[field] {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "field is not a metadata field this endpoint accepts")
		return
	}
	value := strings.TrimSpace(req.Value)
	if value == "" {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "value cannot be empty")
		return
	}
	if len(value) > 4096 {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "value exceeds the maximum size")
		return
	}
	item, err := i.store.GetCatalogItem(c.Request.Context(), req.CatalogID)
	if err != nil {
		httpx.NotFound(c, "catalog item not found")
		return
	}
	// Proposing the value the record already has says nothing. Answering 400
	// keeps a client from filling the listing with agreements on the status quo.
	if strings.EqualFold(fieldValue(item, field), value) {
		httpx.BadRequest(c, "NO_CHANGE", "the catalog item already has this value")
		return
	}

	suggestion, created, err := i.store.UpsertMetadataSuggestion(c.Request.Context(), &model.MetadataSuggestion{
		CatalogID: req.CatalogID,
		AccountID: accountID,
		Field:     field,
		Value:     value,
	})
	if err != nil {
		httpx.Internal(c)
		return
	}
	if created {
		httpx.Created(c, suggestion)
		return
	}
	httpx.OK(c, suggestion)
}

func (i *instance) listSuggestions(c *gin.Context) {
	page, ok := httpx.PageQuery(c, i.core.Config.Security.MaxTake)
	if !ok {
		return
	}
	catalogID, ok := queryID(c, "catalog_id")
	if !ok {
		return
	}
	field := strings.ToLower(strings.TrimSpace(c.Query("field")))
	if field != "" && !model.MetadataFields[field] {
		httpx.BadRequest(c, "INVALID_ARGUMENT", "unknown metadata field")
		return
	}
	var viewer *uuid.UUID
	if accountID, ok := callerID(c); ok {
		viewer = &accountID
	}
	rows, total, err := i.store.ListMetadataSuggestions(c.Request.Context(), catalogID, field, viewer, page.Offset, page.Take)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.List(c, rows, total)
}

func (i *instance) listLeaders(c *gin.Context) {
	catalogID, ok := queryID(c, "catalog_id")
	if !ok {
		return
	}
	leaders, err := i.store.MetadataFieldLeaders(c.Request.Context(), catalogID, 2)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, leaders)
}

func (i *instance) voteSuggestion(c *gin.Context) {
	accountID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var body voteBody
	if !httpx.BindJSON(c, &body) {
		return
	}
	suggestion, err := i.store.VoteMetadataSuggestion(c.Request.Context(), accountID, id, body.Value)
	if err != nil {
		suggestionFailure(c, err)
		return
	}
	httpx.OK(c, suggestion)
}

// ─────────────────────────── Helpers ───────────────────────────

// fieldValue reads one catalog field the suggestion endpoints accept.
func fieldValue(item *model.CatalogItem, field string) string {
	switch field {
	case model.MetadataFieldTitle:
		return item.Title
	case model.MetadataFieldSubtitle:
		return derefString(item.Subtitle)
	case model.MetadataFieldCaption:
		return derefString(item.Caption)
	case model.MetadataFieldReleaseDate:
		return derefString(item.ReleaseDate)
	case model.MetadataFieldArtworkURL:
		return derefString(item.ArtworkURL)
	case model.MetadataFieldGenre:
		return strings.Join(item.GetGenres(), ", ")
	}
	return ""
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// caller resolves the authenticated account, answering 401 when the session
// carries none.
func caller(c *gin.Context) (uuid.UUID, bool) {
	id, ok := callerID(c)
	if !ok {
		httpx.Unauthorized(c)
	}
	return id, ok
}

// callerID resolves the authenticated account without writing a response, for
// public handlers that only want to mark the caller's own rows.
func callerID(c *gin.Context) (uuid.UUID, bool) {
	raw := authn.AccountID(c)
	if raw == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// mineOf reports whether the caller owns one contribution.
func mineOf(c *gin.Context, owner uuid.UUID) bool {
	id, ok := callerID(c)
	return ok && id == owner
}

func pathID(c *gin.Context, param string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(param))
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid "+param)
		return uuid.Nil, false
	}
	return id, true
}

func queryID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Query(name))
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "a "+name+" query parameter is required")
		return uuid.Nil, false
	}
	return id, true
}

// suggestionFailure maps a contribution write failure onto its status: an
// unknown id is 404, voting on one's own contribution is 403, and anything
// else is the server's problem.
func suggestionFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpx.NotFound(c, "contribution not found")
	case errors.Is(err, store.ErrSuggestionOwn):
		httpx.Fail(c, errs.Forbidden("You cannot vote on your own contribution."))
	default:
		httpx.Internal(c)
	}
}

func notFoundOrInternal(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.NotFound(c, "contribution not found")
		return
	}
	httpx.Internal(c)
}
