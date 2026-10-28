package reader

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

// scrapedArticle is the wire payload of DyWebReaderService.ScrapeArticle.
type scrapedArticle struct {
	LinkEmbed model.LinkEmbed
	Content   *string
}

// extractLinkEmbed builds the OpenGraph preview of a fetched HTML page.
func extractLinkEmbed(finalURL *url.URL, body []byte) model.LinkEmbed {
	embed := model.LinkEmbed{URL: finalURL.String()}
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		host := finalURL.Host
		embed.Title = &host
		return embed
	}

	jsonLd := extractJSONLD(document)
	base := *finalURL

	ogTitle := metaContent(document, "og:title")
	ogDescription := metaContent(document, "og:description")
	ogImage := metaContent(document, "og:image")
	ogSiteName := metaContent(document, "og:site_name")
	ogType := metaContent(document, "og:type")

	twitterTitle := metaContent(document, "twitter:title")
	twitterDescription := metaContent(document, "twitter:description")
	twitterImage := metaContent(document, "twitter:image")

	metaTitle := metaContent(document, "title")
	metaDescription := metaContent(document, "description")

	pageTitle := strings.TrimSpace(textContent(findFirst(document, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "title"
	})))
	h1Title := strings.TrimSpace(textContent(findFirst(document, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "h1"
	})))
	firstParagraph := firstMeaningfulParagraph(document)
	firstImage := firstMeaningfulImage(document)



	favicon := faviconURL(document, &base)

	if canonical := canonicalURL(document); canonical != nil {
		embed.URL = canonical.String()
		base = *canonical
	}

	title := firstNonEmpty(ogTitle, twitterTitle, jsonLd.Title, metaTitle, pageTitle, h1Title, base.Host)
	if title != "" {
		embed.Title = &title
	}
	description := firstNonEmpty(ogDescription, twitterDescription, jsonLd.Description, metaDescription, firstParagraph)
	if description != "" {
		embed.Description = &description
	}
	image := resolveURL(firstNonEmpty(ogImage, twitterImage, jsonLd.ImageURL, firstImage), &base)
	if image != "" {
		embed.Image = &image
	}
	siteName := firstNonEmpty(ogSiteName, jsonLd.SiteName, metaContent(document, "application-name"), base.Host)
	if siteName != "" {
		embed.SiteName = &siteName
	}
	contentType := firstNonEmpty(ogType, jsonLd.Type)
	if contentType != "" {
		embed.Type = &contentType
	}
	if favicon != "" {
		embed.Icon = &favicon
	}
	return embed
}

// extractArticle returns the inner HTML of the page's first <article> element,
// which is what the C# reader exposes to clients.
func extractArticle(body []byte) *string {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	article := findFirst(document, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "article"
	})
	if article == nil {
		return nil
	}
	var rendered strings.Builder
	for child := article.FirstChild; child != nil; child = child.NextSibling {
		if err := html.Render(&rendered, child); err != nil {
			return nil
		}
	}
	content := rendered.String()
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return &content
}

// jsonLDData is the subset of schema.org metadata the reader understands.
type jsonLDData struct {
	Title       string
	Description string
	ImageURL    string
	SiteName    string
	Type        string
}

func extractJSONLD(document *html.Node) jsonLDData {
	var data jsonLDData
	scripts := findAll(document, func(n *html.Node) bool {
		if n.Type != html.ElementNode || n.Data != "script" {
			return false
		}
		return strings.EqualFold(attr(n, "type"), "application/ld+json")
	})

	for _, script := range scripts {
		raw := strings.TrimSpace(textContent(script))
		if raw == "" {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			continue
		}
		elements := jsonLDObjects(decoded)
		for _, element := range elements {
			if data.Title == "" {
				data.Title = firstNonEmpty(jsonLDString(element, "headline"), jsonLDString(element, "name"))
			}
			if data.Description == "" {
				data.Description = jsonLDString(element, "description")
			}
			if data.ImageURL == "" {
				data.ImageURL = jsonLDImage(element)
			}
			if data.SiteName == "" {
				if publisher, ok := element["publisher"].(map[string]any); ok {
					data.SiteName = jsonLDString(publisher, "name")
				}
			}
			if data.Type == "" {
				data.Type = jsonLDString(element, "@type")
			}
			if data.Title != "" || data.Description != "" {
				break
			}
		}
	}
	return data
}

// jsonLDObjects normalizes a JSON-LD document into a list of objects.
func jsonLDObjects(decoded any) []map[string]any {
	switch value := decoded.(type) {
	case map[string]any:
		return []map[string]any{value}
	case []any:
		objects := make([]map[string]any, 0, len(value))
		for _, item := range value {
			if object, ok := item.(map[string]any); ok {
				objects = append(objects, object)
			}
		}
		return objects
	default:
		return nil
	}
}

func jsonLDString(object map[string]any, key string) string {
	value, ok := object[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func jsonLDImage(object map[string]any) string {
	switch value := object["image"].(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok {
				return strings.TrimSpace(text)
			}
		}
	case map[string]any:
		return jsonLDString(value, "url")
	}
	return ""
}

// firstMeaningfulParagraph returns the first paragraph with a plausible
// description length, truncated at 300 characters like the C# reader.
func firstMeaningfulParagraph(document *html.Node) string {
	paragraphs := findAll(document, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "p"
	})
	for _, paragraph := range paragraphs {
		text := strings.TrimSpace(textContent(paragraph))
		if len(text) <= 30 || len(text) >= 500 {
			continue
		}
		if len(text) > 300 {
			return text[:297] + "..."
		}
		return text
	}
	return ""
}

// firstMeaningfulImage returns the first non-inline image that is not a
// tracking pixel.
func firstMeaningfulImage(document *html.Node) string {
	images := findAll(document, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "img"
	})
	for _, image := range images {
		src := strings.TrimSpace(attr(image, "src"))
		if src == "" || strings.HasPrefix(strings.ToLower(src), "data:") {
			continue
		}
		width, widthErr := strconv.Atoi(attr(image, "width"))
		height, heightErr := strconv.Atoi(attr(image, "height"))
		if widthErr == nil && heightErr == nil && width < 50 && height < 50 {
			continue
		}
		return src
	}
	return ""
}

// metaContent reads a meta tag by property first, then by name.
func metaContent(document *html.Node, key string) string {
	metas := findAll(document, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "meta"
	})
	for _, meta := range metas {
		if !strings.EqualFold(attr(meta, "property"), key) {
			continue
		}
		if value := strings.TrimSpace(attr(meta, "content")); value != "" {
			return value
		}
	}
	for _, meta := range metas {
		if !strings.EqualFold(attr(meta, "name"), key) {
			continue
		}
		if value := strings.TrimSpace(attr(meta, "content")); value != "" {
			return value
		}
	}
	return ""
}

// canonicalURL resolves the page's canonical link.
func canonicalURL(document *html.Node) *url.URL {
	link := findLink(document, "canonical")
	if link == nil {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(attr(link, "href")))
	if err != nil || !parsed.IsAbs() {
		return nil
	}
	return parsed
}

// faviconURL resolves the site icon, falling back to /favicon.ico.
func faviconURL(document *html.Node, base *url.URL) string {
	// The C# reader prefers the Apple touch icon over the plain icon.
	icon := findLink(document, "apple-touch-icon")
	if icon == nil {
		icon = findLink(document, "icon", "shortcut icon")
	}
	if icon != nil {
		if resolved := resolveURL(strings.TrimSpace(attr(icon, "href")), base); resolved != "" {
			return resolved
		}
	}
	return resolveURL("/favicon.ico", base)
}

// findLink returns the first <link> whose rel matches one of the values and
// that carries an href.
func findLink(document *html.Node, rels ...string) *html.Node {
	return findFirst(document, func(n *html.Node) bool {
		if n.Type != html.ElementNode || n.Data != "link" || attr(n, "href") == "" {
			return false
		}
		rel := strings.ToLower(attr(n, "rel"))
		for _, candidate := range rels {
			if rel == candidate {
				return true
			}
		}
		return false
	})
}

// resolveURL resolves a possibly relative reference against the page URL.
func resolveURL(reference string, base *url.URL) string {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return ""
	}
	parsed, err := url.Parse(reference)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	return base.ResolveReference(parsed).String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// findFirst walks the tree in document order and returns the first match.
func findFirst(root *html.Node, match func(*html.Node) bool) *html.Node {
	if root == nil {
		return nil
	}
	if match(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findFirst(child, match); found != nil {
			return found
		}
	}
	return nil
}

// findAll collects every match in document order.
func findAll(root *html.Node, match func(*html.Node) bool) []*html.Node {
	var found []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if match(node) {
			found = append(found, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	if root != nil {
		walk(root)
	}
	return found
}

// attr returns an attribute value (case-insensitive key).
func attr(node *html.Node, key string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, key) {
			return attribute.Val
		}
	}
	return ""
}

// textContent concatenates the text nodes below n.
func textContent(n *html.Node) string {
	if n == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			builder.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return builder.String()
}
