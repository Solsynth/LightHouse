// Package webui serves the built Vue single-page application (web/dist) from
// the Stargate HTTP server. Files that exist in dist are served from a
// middleware mounted before the auth chain (a fingerprinted asset must not cost
// a session lookup); unknown non-API paths fall back to index.html from the
// NoRoute handler so client-side routes survive a reload. API, well-known,
// swagger and health paths stay API-only and answer with the canonical JSON
// 404.
package webui

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/go/pkg/errs"
)

// reservedPrefixes are served by the API (or the edge) and must never fall
// back to the SPA. Matching is segment-based, so /apiary is not reserved.
var reservedPrefixes = []string{"/api", "/.well-known", "/swagger", "/health", "/alive"}

// Handler serves the SPA from a dist directory. The zero value (and a handler
// built with an empty dist path) is disabled: it only answers the JSON 404.
type Handler struct {
	root fs.FS
}

// New opens the built SPA at dist. An empty dist returns a disabled handler; a
// missing directory or a directory without index.html returns an error and a
// disabled handler.
func New(dist string) (*Handler, error) {
	dist = strings.TrimSpace(dist)
	if dist == "" {
		return &Handler{}, nil
	}
	info, err := os.Stat(dist)
	if err != nil {
		return &Handler{}, err
	}
	if !info.IsDir() {
		return &Handler{}, fmt.Errorf("%s is not a directory", dist)
	}
	root := os.DirFS(dist)
	if _, err := fs.Stat(root, "index.html"); err != nil {
		return &Handler{}, fmt.Errorf("index.html: %w", err)
	}
	return &Handler{root: root}, nil
}

// ServeStatic is a gin middleware mounted before the auth middleware: a GET or
// HEAD for an existing file in dist is served here and never reaches the API
// chain; every other request continues down the chain.
func (h *Handler) ServeStatic(c *gin.Context) {
	name, ok := h.file(c)
	if !ok {
		c.Next()
		return
	}
	h.serveFile(c, name)
	c.Abort()
}

// Serve is the gin NoRoute handler: unknown extension-less paths get
// index.html (client-side routing), missing assets and reserved/API paths get
// the canonical JSON 404.
func (h *Handler) Serve(c *gin.Context) {
	if isReserved(c.Request.URL.Path) || hasDotSegment(c.Request.URL.Path) ||
		(c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) {
		notFound(c)
		return
	}
	if h.root == nil {
		notFound(c)
		return
	}
	name, ok := h.file(c)
	if ok {
		h.serveFile(c, name)
		return
	}
	// A missing path with a file extension is a missing asset, not a route.
	if cleaned := strings.TrimPrefix(path.Clean("/"+c.Request.URL.Path), "/"); cleaned != "" && path.Ext(cleaned) != "" {
		notFound(c)
		return
	}
	h.serveIndex(c)
}

// file resolves the request path to an existing regular file in dist. Dot
// segments are rejected outright (rather than silently cleaned into a
// different path), and fs.Stat on an os.DirFS keeps the request path from ever
// escaping dist.
func (h *Handler) file(c *gin.Context) (string, bool) {
	if h.root == nil || (c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) {
		return "", false
	}
	if isReserved(c.Request.URL.Path) || hasDotSegment(c.Request.URL.Path) {
		return "", false
	}
	name := strings.TrimPrefix(path.Clean("/"+c.Request.URL.Path), "/")
	if name == "" {
		return "", false
	}
	info, err := fs.Stat(h.root, name)
	if err != nil || info.IsDir() {
		return "", false
	}
	return name, true
}

func (h *Handler) serveFile(c *gin.Context, name string) {
	file, info, err := h.open(name)
	if err != nil {
		notFound(c)
		return
	}
	defer file.Close()
	// Vite fingerprints every asset name, so those can be cached forever.
	// Anything else in dist (favicon.ico) must revalidate.
	if strings.HasPrefix(name, "assets/") {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Header("Cache-Control", "no-cache")
	}
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}

func (h *Handler) serveIndex(c *gin.Context) {
	file, info, err := h.open("index.html")
	if err != nil {
		notFound(c)
		return
	}
	defer file.Close()
	c.Header("Cache-Control", "no-cache")
	http.ServeContent(c.Writer, c.Request, "index.html", info.ModTime(), file)
}

func (h *Handler) open(name string) (*closerReadSeeker, fs.FileInfo, error) {
	file, err := h.root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		file.Close()
		return nil, nil, fmt.Errorf("%s is not seekable", name)
	}
	return &closerReadSeeker{ReadSeeker: seeker, file: file}, info, nil
}

// closerReadSeeker keeps the Close method out of the returned ReadSeeker while
// still closing the underlying fs.File.
type closerReadSeeker struct {
	io.ReadSeeker
	file fs.File
}

func (c *closerReadSeeker) Close() error { return c.file.Close() }

func isReserved(p string) bool {
	for _, prefix := range reservedPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// hasDotSegment reports whether any segment is "." or "..". The request path
// arrives already percent-decoded, so encoded traversal reaches this check.
func hasDotSegment(p string) bool {
	for _, segment := range strings.Split(p, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func notFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.NotFound("Not found."))
}
