// Package webui serves Pulsar's hybrid SSR mainframe (Go html/template) and
// Vue 3 CSR islands (built into web/dist via Vite).
package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/metadata"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

//go:embed templates/*.html
var templateFS embed.FS

type manifestEntry struct {
	File string   `json:"file"`
	CSS  []string `json:"css"`
}

// OpenGraph holds SSR social-sharing metadata rendered into <head>.
type OpenGraph struct {
	Title       string
	Description string
	Image       string
	URL         string
	Type        string
	TwitterCard string
}

// NavUser represents the currently signed-in account in the SSR navbar.
type NavUser struct {
	ID        string
	Name      string
	Nick      string
	AvatarURL string
}

// PageData is the common envelope passed to every mainframe template.
type PageData struct {
	Title       string
	Description string
	ActiveNav   string
	CurrentUser *NavUser
	SignInURL   string
	OG          *OpenGraph
	DevViteURL  string
	EntryJS     string
	EntryCSS    []string
	// Page-specific payload
	Data any
}

// Handler renders SSR templates and serves built Vue island assets.
type Handler struct {
	core       *core.Services
	templates  map[string]*template.Template
	distFS     fs.FS
	entryJS    string
	entryCSS   []string
	devViteURL string
	DistError  error
}

// New constructs a webui Handler. Missing web/dist is recorded in DistError
// without failing, so SSR templates and API routes remain operational.
func New(services *core.Services) (*Handler, error) {
	h := &Handler{
		core:      services,
		templates: make(map[string]*template.Template),
	}
	if services != nil && services.Config != nil {
		h.devViteURL = strings.TrimRight(strings.TrimSpace(services.Config.Web.DevViteURL), "/")
		distPath := strings.TrimSpace(services.Config.Web.DistPath)
		if distPath != "" {
			if err := h.loadDist(distPath); err != nil {
				if distPath == "web/dist" {
					candidates := []string{"internal/pulsar/web/dist", "/app/web/dist"}
					if exe, exeErr := os.Executable(); exeErr == nil {
						candidates = append([]string{filepath.Join(filepath.Dir(exe), "web/dist")}, candidates...)
					}
					for _, candidate := range candidates {
						if fallbackErr := h.loadDist(candidate); fallbackErr == nil {
							err = nil
							break
						}
					}
				}
				h.DistError = err
			}
		}
	}

	funcs := template.FuncMap{
		"json": func(v any) string {
			raw, err := json.Marshal(v)
			if err != nil {
				return "{}"
			}
			return string(raw)
		},
		"formatDuration": formatDuration,
		"formatDate": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format("2006-01-02 15:04 UTC")
		},
		"domainLabel": domainLabel,
		"deref": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
		"derefInt": func(n *int) int {
			if n == nil {
				return 0
			}
			return *n
		},
		"statusLabel": func(s *string) string {
			if s == nil {
				return ""
			}
			switch *s {
			case model.StatusPlanToWatch:
				return "Plan to Watch"
			case model.StatusWatching:
				return "Watching"
			case model.StatusCompleted:
				return "Completed"
			case model.StatusOnHold:
				return "On Hold"
			case model.StatusDropped:
				return "Dropped"
			default:
				return strings.ReplaceAll(*s, "_", " ")
			}
		},
		"statusDotClass": func(s *string) string {
			if s == nil {
				return "bg-[var(--console-faint)]"
			}
			switch *s {
			case model.StatusWatching:
				return "bg-primary"
			case model.StatusCompleted:
				return "bg-success"
			case model.StatusOnHold:
				return "bg-warning"
			case model.StatusDropped:
				return "bg-error"
			default:
				return "bg-[var(--console-faint)]"
			}
		},
		"initial": func(s string) string {
			s = strings.TrimSpace(s)
			if s == "" {
				return "?"
			}
			r := []rune(s)
			return strings.ToUpper(string(r[0]))
		},
		"dict": func(values ...any) (map[string]any, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("dict expects even number of args")
			}
			m := make(map[string]any, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				k, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				m[k] = values[i+1]
			}
			return m, nil
		},
	}

	pages := []string{
		"home.html",
		"profile.html",
		"share_media.html",
		"collection.html",
		"catalog.html",
		"catalog_item.html",
		"activities.html",
		"history.html",
		"collections.html",
		"settings.html",
		"rewind.html",
	}
	for _, page := range pages {
		tmpl, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", page, err)
		}
		h.templates[page] = tmpl
	}

	return h, nil
}

func (h *Handler) loadDist(distPath string) error {
	info, err := os.Stat(distPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", distPath)
	}
	root := os.DirFS(distPath)
	raw, err := fs.ReadFile(root, ".vite/manifest.json")
	if err != nil {
		return fmt.Errorf(".vite/manifest.json: %w", err)
	}
	var manifest map[string]manifestEntry
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("parse manifest.json: %w", err)
	}
	h.distFS = root
	if entry, ok := manifest["src/main.ts"]; ok {
		if entry.File != "" {
			h.entryJS = "/" + strings.TrimLeft(entry.File, "/")
		}
		for _, css := range entry.CSS {
			h.entryCSS = append(h.entryCSS, "/"+strings.TrimLeft(css, "/"))
		}
	}
	return nil
}

// RegisterRoutes mounts the SSR mainframe pages and static asset routes on the Gin engine.
func (h *Handler) RegisterRoutes(engine *gin.Engine) {
	if engine == nil {
		return
	}
	engine.GET("/assets/*filepath", h.serveAsset)
	engine.GET("/favicon.ico", h.serveRootFile("favicon.ico"))
	engine.GET("/favicon.png", h.serveRootFile("favicon.png"))
	engine.GET("/icon-outline.svg", h.serveRootFile("icon-outline.svg"))

	engine.GET("/", h.handleHome)
	engine.GET("/catalog", h.handleCatalog)
	engine.GET("/catalog/:id", h.handleCatalogItem)
	engine.GET("/activities", h.handleActivities)
	engine.GET("/u/:identifier", h.handleProfile)
	engine.GET("/u/:identifier/rewind/:year", h.handleUserRewind)
	engine.GET("/rewind/:year", h.handleMyRewind)
	engine.GET("/c/:id", h.handleCollection)
	engine.GET("/history", h.handleHistory)
	engine.GET("/collections", h.handleCollections)
	engine.GET("/settings", h.handleSettings)
}

func (h *Handler) serveAsset(c *gin.Context) {
	if h.distFS == nil {
		c.Status(http.StatusNotFound)
		return
	}
	clean := strings.TrimPrefix(path.Clean("/assets"+c.Param("filepath")), "/")
	if clean == "" || strings.Contains(clean, "..") {
		c.Status(http.StatusNotFound)
		return
	}
	h.serveDistFile(c, clean, true)
}

func (h *Handler) serveRootFile(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.distFS == nil {
			c.Status(http.StatusNotFound)
			return
		}
		h.serveDistFile(c, name, false)
	}
}

func (h *Handler) serveDistFile(c *gin.Context, name string, immutable bool) {
	f, err := h.distFS.Open(name)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		c.Header("Content-Type", ct)
	}
	if immutable {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Header("Cache-Control", "public, max-age=3600")
	}
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(c.Writer, c.Request, stat.Name(), stat.ModTime(), rs)
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Data(http.StatusOK, c.Writer.Header().Get("Content-Type"), data)
}

func (h *Handler) basePage(c *gin.Context, title, desc, activeNav string, og *OpenGraph, data any) PageData {
	var navUser *NavUser
	if acc := authn.CurrentUser(c); acc != nil {
		name := acc.GetName()
		nick := acc.GetNick()
		if nick == "" {
			nick = name
		}
		navUser = &NavUser{
			ID:        acc.GetId(),
			Name:      name,
			Nick:      nick,
			AvatarURL: accountAvatarURL(acc),
		}
	}
	signInURL := "/sign-in"
	if h.core != nil && h.core.Config != nil && strings.TrimSpace(h.core.Config.Web.StargateURL) != "" {
		signInURL = strings.TrimRight(strings.TrimSpace(h.core.Config.Web.StargateURL), "/") + "/sign-in"
	}
	return PageData{
		Title:       title,
		Description: desc,
		ActiveNav:   activeNav,
		CurrentUser: navUser,
		SignInURL:   signInURL,
		OG:          og,
		DevViteURL:  h.devViteURL,
		EntryJS:     h.entryJS,
		EntryCSS:    h.entryCSS,
		Data:        data,
	}
}

func (h *Handler) render(c *gin.Context, status int, page string, data PageData) {
	tmpl, ok := h.templates[page]
	if !ok {
		c.String(http.StatusInternalServerError, "template not found: %s", page)
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)
	if err := tmpl.ExecuteTemplate(c.Writer, "layout.html", data); err != nil && h.core != nil && h.core.Log != nil {
		h.core.Log.Error("render template", "page", page, "error", err)
	}
}

func (h *Handler) handleHome(c *gin.Context) {
	page := h.basePage(c, "Pulsar · Media & Scrobbling", "Track music, anime, games, books, and video across the Solar Network.", "home", &OpenGraph{
		Title:       "Pulsar · Solar Network",
		Description: "Universal multi-domain playback tracking, scrobbling, and collections.",
		Type:        "website",
	}, map[string]any{
		"Year": time.Now().UTC().Year(),
	})
	h.render(c, http.StatusOK, "home.html", page)
}

func (h *Handler) handleCatalog(c *gin.Context) {
	page := h.basePage(c, "Catalog & Creators · Pulsar", "Browse the Pulsar media catalog and creators.", "catalog", nil, nil)
	h.render(c, http.StatusOK, "catalog.html", page)
}

func (h *Handler) handleCatalogItem(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, errs.NotFound("Catalog item not found."))
		return
	}
	if h.core == nil || h.core.Store == nil {
		c.JSON(http.StatusServiceUnavailable, errs.New("UNAVAILABLE", "store unavailable", http.StatusServiceUnavailable))
		return
	}
	item, err := h.core.Store.GetCatalogItem(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.NotFound("Catalog item not found."))
		return
	}
	artwork := ""
	if item.ArtworkURL != nil {
		artwork = *item.ArtworkURL
	}
	desc := fmt.Sprintf("%s · %s on Pulsar", item.Title, domainLabel(item.Domain))
	if item.Subtitle != nil && strings.TrimSpace(*item.Subtitle) != "" {
		desc = fmt.Sprintf("%s — %s", item.Title, *item.Subtitle)
	}
	page := h.basePage(c, fmt.Sprintf("%s · Pulsar", item.Title), desc, "catalog", &OpenGraph{
		Title:       item.Title,
		Description: desc,
		Image:       artwork,
		Type:        "website",
	}, item)
	h.render(c, http.StatusOK, "catalog_item.html", page)
}

func (h *Handler) handleActivities(c *gin.Context) {
	page := h.basePage(c, "Activities · Pulsar", "Manage active playback sessions and broadcast activities.", "activities", nil, nil)
	h.render(c, http.StatusOK, "activities.html", page)
}

func (h *Handler) handleHistory(c *gin.Context) {
	page := h.basePage(c, "History · Pulsar", "Your playback and scrobble history.", "history", nil, nil)
	h.render(c, http.StatusOK, "history.html", page)
}

func (h *Handler) handleCollections(c *gin.Context) {
	page := h.basePage(c, "Collections · Pulsar", "Playlists, watchlists, backlogs, and favourites.", "collections", nil, nil)
	h.render(c, http.StatusOK, "collections.html", page)
}

func (h *Handler) handleSettings(c *gin.Context) {
	page := h.basePage(c, "Settings · Pulsar", "Manage Bangumi sync and library connections.", "settings", nil, nil)
	h.render(c, http.StatusOK, "settings.html", page)
}

func (h *Handler) handleMyRewind(c *gin.Context) {
	year, err := strconv.Atoi(c.Param("year"))
	if err != nil || year < 2000 || year > 2100 {
		year = time.Now().UTC().Year()
	}
	user := authn.CurrentUser(c)
	identifier := ""
	if user != nil {
		identifier = user.GetId()
	}
	page := h.basePage(c, fmt.Sprintf("Rewind %d · Pulsar", year), "Your year in music, anime, games, and media.", "home", nil, map[string]any{
		"Year":       year,
		"Identifier": identifier,
		"Personal":   true,
	})
	h.render(c, http.StatusOK, "rewind.html", page)
}

func (h *Handler) handleUserRewind(c *gin.Context) {
	identifier := c.Param("identifier")
	year, err := strconv.Atoi(c.Param("year"))
	if err != nil || year < 2000 || year > 2100 {
		year = time.Now().UTC().Year()
	}
	page := h.basePage(c, fmt.Sprintf("%s's %d Rewind · Pulsar", identifier, year), "Year in review on Pulsar.", "profile", &OpenGraph{
		Title:       fmt.Sprintf("%s · %d Rewind on Pulsar", identifier, year),
		Description: fmt.Sprintf("Explore %s's %d listening, watching, and gaming stats.", identifier, year),
		Type:        "profile",
	}, map[string]any{
		"Year":       year,
		"Identifier": identifier,
		"Personal":   false,
	})
	h.render(c, http.StatusOK, "rewind.html", page)
}

type profileViewData struct {
	Identifier  string
	DisplayName string
	Username    string
	AvatarURL   string
	AccountID   string
	Stats       *store.MediaStats
	Active      []model.ActiveSession
	Scrobbles   []model.Scrobble
	Collections []model.Collection
	Shelves     []model.Collection
	CurrentYear int
}

func (h *Handler) handleProfile(c *gin.Context) {
	identifier := strings.TrimSpace(c.Param("identifier"))
	if h.core == nil || h.core.Store == nil {
		c.JSON(http.StatusServiceUnavailable, errs.New("UNAVAILABLE", "store unavailable", http.StatusServiceUnavailable))
		return
	}

	ctx := c.Request.Context()
	accountID, err := h.core.ResolveAccount(ctx, identifier)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.NotFound("User not found."))
		return
	}

	displayName := identifier
	username := identifier
	avatarURL := ""
	if h.core.Clients != nil && h.core.Clients.Account != nil {
		if acc, err := h.core.Clients.Account.GetAccount(ctx, &gen.DyGetAccountRequest{
			Id: accountID.String(),
		}); err == nil && acc != nil {
			if acc.GetName() != "" {
				username = acc.GetName()
			}
			if acc.GetNick() != "" {
				displayName = acc.GetNick()
			} else if username != "" {
				displayName = username
			}
			avatarURL = accountAvatarURL(acc)
		}
	}

	stats, _ := h.core.Store.GetMediaStats(ctx, accountID, "", nil, nil)
	active, _ := h.core.Store.GetActiveSessionsForAccount(ctx, accountID)
	scrobbles, _, _ := h.core.Store.ListScrobbles(ctx, accountID, "", nil, nil, 0, 12)
	allColls, _, _ := h.core.Store.ListCollections(ctx, accountID, "", "", 0, 20)
	var publicColls []model.Collection
	var shelves []model.Collection
	for _, col := range allColls {
		if !col.IsPublic || col.Kind == model.KindFavorites {
			continue
		}
		publicColls = append(publicColls, col)
		if len(shelves) < 8 {
			if full, err := h.core.Store.GetCollection(ctx, col.ID); err == nil {
				for idx := range full.Items {
					it := &full.Items[idx]
					if it.CatalogID != nil && (it.Note == nil || strings.TrimSpace(*it.Note) == "" || it.Rating == nil) {
						if rev, rErr := h.core.Store.GetCatalogReview(ctx, accountID, *it.CatalogID); rErr == nil {
							if (it.Note == nil || strings.TrimSpace(*it.Note) == "") && rev.Body != nil && strings.TrimSpace(*rev.Body) != "" {
								it.Note = rev.Body
							}
							if it.Rating == nil && rev.Rating != nil {
								it.Rating = rev.Rating
							}
						}
					}
				}
				shelves = append(shelves, *full)
			}
		}
	}

	desc := fmt.Sprintf("%s on Pulsar", displayName)
	if stats != nil {
		desc = fmt.Sprintf("%s · %d scrobbles across %s", displayName, stats.TotalScrobbles, formatDuration(stats.TotalSeconds))
	}

	data := profileViewData{
		Identifier:  identifier,
		DisplayName: displayName,
		Username:    username,
		AvatarURL:   avatarURL,
		AccountID:   accountID.String(),
		Stats:       stats,
		Active:      active,
		Scrobbles:   scrobbles,
		Collections: publicColls,
		Shelves:     shelves,
		CurrentYear: time.Now().UTC().Year(),
	}

	page := h.basePage(c, fmt.Sprintf("%s (@%s) · Pulsar", displayName, username), desc, "profile", &OpenGraph{
		Title:       fmt.Sprintf("%s (@%s) · Pulsar", displayName, username),
		Description: desc,
		Type:        "profile",
	}, data)
	h.render(c, http.StatusOK, "profile.html", page)
}

func (h *Handler) handleCollection(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, errs.NotFound("Collection not found."))
		return
	}
	if h.core == nil || h.core.Store == nil {
		c.JSON(http.StatusServiceUnavailable, errs.New("UNAVAILABLE", "store unavailable", http.StatusServiceUnavailable))
		return
	}
	coll, err := h.core.Store.GetCollection(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.NotFound("Collection not found."))
		return
	}
	if !coll.IsPublic {
		viewerID := authn.AccountID(c)
		if viewerID != coll.AccountID.String() {
			c.JSON(http.StatusForbidden, errs.Forbidden("This collection is private."))
			return
		}
	}

	desc := fmt.Sprintf("%s · %s collection on Pulsar", coll.Name, domainLabel(coll.Domain))
	if coll.Description != nil && strings.TrimSpace(*coll.Description) != "" {
		desc = *coll.Description
	}
	artwork := ""
	if coll.ArtworkURL != nil {
		artwork = *coll.ArtworkURL
	}

	page := h.basePage(c, fmt.Sprintf("%s · Pulsar", coll.Name), desc, "collections", &OpenGraph{
		Title:       coll.Name,
		Description: desc,
		Image:       artwork,
		Type:        "website",
	}, coll)
	h.render(c, http.StatusOK, "collection.html", page)
}

// RenderMediaShare renders a media share page (/m/:id, /share/m/:id) inside
// the SSR mainframe template with OpenGraph tags and interactive Vue islands.
func (h *Handler) RenderMediaShare(c *gin.Context, m *metadata.UnifiedMedia) {
	if m == nil {
		c.JSON(http.StatusNotFound, errs.NotFound("Media not found."))
		return
	}
	desc := strings.TrimSpace(m.Description)
	if desc == "" {
		if m.Subtitle != "" {
			desc = fmt.Sprintf("%s by %s on Pulsar", m.Title, m.Subtitle)
		} else {
			desc = fmt.Sprintf("%s on Pulsar", m.Title)
		}
	}
	ogType := "website"
	switch m.Domain {
	case model.DomainMusic:
		ogType = "music.song"
	case model.DomainVideo, model.DomainAnime:
		ogType = "video.movie"
	}
	ogTitle := m.Title
	if m.Subtitle != "" {
		ogTitle = m.Title + " · " + m.Subtitle
	}

	var catalogIDStr string
	if m.ID != uuid.Nil {
		catalogIDStr = m.ID.String()
	}

	page := h.basePage(c, m.Title+" · Pulsar", desc, "home", &OpenGraph{
		Title:       ogTitle,
		Description: desc,
		Image:       m.ArtworkURL,
		URL:         m.ShareURL,
		Type:        ogType,
		TwitterCard: "summary_large_image",
	}, map[string]any{
		"Media":     m,
		"CatalogID": catalogIDStr,
	})
	h.render(c, http.StatusOK, "share_media.html", page)
}

func accountAvatarURL(acc *gen.DyAccount) string {
	if acc == nil {
		return ""
	}
	pic := acc.GetPicture()
	if pic == nil {
		return ""
	}
	if u := strings.TrimSpace(pic.GetUrl()); u != "" {
		if strings.HasPrefix(u, "/") {
			return "https://api.solian.app" + u
		}
		return u
	}
	if id := strings.TrimSpace(pic.GetId()); id != "" {
		return "https://api.solian.app/drive/files/" + id
	}
	return ""
}

func formatDuration(sec int64) string {
	if sec <= 0 {
		return "0m"
	}
	hours := sec / 3600
	mins := (sec % 3600) / 60
	if hours >= 24 {
		days := hours / 24
		remHours := hours % 24
		return fmt.Sprintf("%dd %dh", days, remHours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	if mins > 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%ds", sec)
}

func domainLabel(domain string) string {
	switch strings.ToLower(domain) {
	case model.DomainMusic:
		return "Music"
	case model.DomainAnime:
		return "Anime"
	case model.DomainGaming:
		return "Gaming"
	case model.DomainVideo:
		return "Video"
	case model.DomainBook:
		return "Book"
	default:
		if domain == "" {
			return "Media"
		}
		return strings.ToUpper(domain[:1]) + domain[1:]
	}
}
