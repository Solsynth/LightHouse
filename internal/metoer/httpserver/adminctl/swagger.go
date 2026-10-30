package adminctl

// Static OpenAPI 3.0.1 manifest served at /swagger/v1/swagger.json,
// mirroring the SwaggerGen.cs contract ("DysonNetwork.Ring", "The realtime
// service in the Solar Network."). The C# service serves Swashbuckle output
// at the same URL; byte-identity with Swashbuckle is a documented plan
// deviation (dev tool only), same as Stargate's hand-built manifests.

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type openAPIDoc struct {
	OpenAPI    string                 `json:"openapi"`
	Info       openAPIInfo            `json:"info"`
	Paths      map[string]openAPIPath `json:"paths"`
	Components openAPIComponents      `json:"components"`
}

type openAPIInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

type openAPIPath map[string]openAPIOperation

type openAPIOperation struct {
	Summary    string                     `json:"summary,omitempty"`
	Tags       []string                   `json:"tags,omitempty"`
	Parameters []openAPIParameter         `json:"parameters,omitempty"`
	Responses  map[string]openAPIResponse `json:"responses"`
}

type openAPIParameter struct {
	Name     string        `json:"name"`
	In       string        `json:"in"`
	Required bool          `json:"required,omitempty"`
	Schema   openAPISchema `json:"schema"`
}

type openAPISchema struct {
	Type   string `json:"type,omitempty"`
	Format string `json:"format,omitempty"`
}

type openAPIResponse struct {
	Description string                      `json:"description"`
	Content     map[string]openAPIMediaType `json:"content,omitempty"`
}

type openAPIMediaType struct {
	Schema openAPISchemaRef `json:"schema"`
}

type openAPISchemaRef struct {
	Ref string `json:"$ref,omitempty"`
}

type openAPIComponents struct {
	Schemas map[string]openAPISchemaObject `json:"schemas,omitempty"`
}

type openAPISchemaObject struct {
	Type       string                   `json:"type,omitempty"`
	Properties map[string]openAPISchema `json:"properties,omitempty"`
}

// routeSpec is one entry of the static route table below.
type routeSpec struct {
	path      string
	method    string
	tag       string
	summary   string
	params    []string
	notFound  bool
	forbidden bool
}

var standardErrorResponses = map[string]openAPIResponse{
	"400": apiErrorResponse("Bad request — validation or semantic error"),
	"401": apiErrorResponse("Unauthorized — missing or invalid token"),
}

func apiErrorResponse(desc string) openAPIResponse {
	return openAPIResponse{
		Description: desc,
		Content: map[string]openAPIMediaType{
			"application/json": {Schema: openAPISchemaRef{Ref: "#/components/schemas/ApiError"}},
		},
	}
}

// buildDoc builds the manifest from the static route table.
func buildDoc(title, description string, routes []routeSpec) openAPIDoc {
	paths := make(map[string]openAPIPath)
	for _, r := range routes {
		responses := make(map[string]openAPIResponse, 6)
		for code, resp := range standardErrorResponses {
			responses[code] = resp
		}
		if r.forbidden {
			responses["403"] = apiErrorResponse("Forbidden — missing permission")
		}
		if r.notFound {
			responses["404"] = apiErrorResponse("Not found")
		}
		responses["200"] = openAPIResponse{Description: "Success"}

		op := openAPIOperation{
			Summary:   r.summary,
			Tags:      []string{r.tag},
			Responses: responses,
		}
		for _, p := range r.params {
			op.Parameters = append(op.Parameters, openAPIParameter{
				Name: p, In: "path", Required: true,
				Schema: openAPISchema{Type: "string"},
			})
		}
		pathOps, ok := paths[r.path]
		if !ok {
			pathOps = openAPIPath{}
			paths[r.path] = pathOps
		}
		pathOps[r.method] = op
	}

	return openAPIDoc{
		OpenAPI: "3.0.1",
		Info: openAPIInfo{
			Title:       title,
			Version:     "v1",
			Description: description,
		},
		Paths: paths,
		Components: openAPIComponents{
			Schemas: map[string]openAPISchemaObject{
				"ApiError": {
					Type: "object",
					Properties: map[string]openAPISchema{
						"code":    {Type: "string"},
						"message": {Type: "string"},
						"status":  {Type: "integer", Format: "int32"},
						"traceId": {Type: "string"},
					},
				},
			},
		},
	}
}

// ringSwagger covers the Ring surface ported into Metoer.
var ringSwagger = buildDoc(
	"DysonNetwork.Ring",
	"The realtime service in the Solar Network.",
	[]routeSpec{
		{path: "/api/notifications/count", method: "GET", tag: "Notifications", summary: "Count unread notifications"},
		{path: "/api/notifications", method: "GET", tag: "Notifications", summary: "List notifications (X-Total header)"},
		{path: "/api/notifications/all/read", method: "POST", tag: "Notifications", summary: "Mark all notifications viewed", forbidden: true},
		{path: "/api/notifications/subscription", method: "PUT", tag: "Notifications", summary: "Subscribe a device to push notifications", forbidden: true},
		{path: "/api/notifications/subscription", method: "GET", tag: "Notifications", summary: "List the account's push subscriptions"},
		{path: "/api/notifications/subscription/current", method: "GET", tag: "Notifications", summary: "Get the current device's active subscription"},
		{path: "/api/notifications/subscription/{subscriptionId}", method: "DELETE", tag: "Notifications", summary: "Unsubscribe a device", params: []string{"subscriptionId"}, forbidden: true},
		{path: "/api/notifications/send", method: "POST", tag: "Notifications", summary: "Send a notification to accounts (batch)", forbidden: true},
		{path: "/api/notifications/preferences", method: "GET", tag: "Notifications", summary: "List notification preferences"},
		{path: "/api/notifications/preferences/{topic}", method: "GET", tag: "Notifications", summary: "Get a topic's preference level", params: []string{"topic"}},
		{path: "/api/notifications/preferences/{topic}", method: "PUT", tag: "Notifications", summary: "Set a topic's preference level", params: []string{"topic"}, forbidden: true},
		{path: "/api/notifications/preferences/{topic}", method: "DELETE", tag: "Notifications", summary: "Delete a topic preference", params: []string{"topic"}, forbidden: true},

		{path: "/api/notifications/sop/subscription", method: "POST", tag: "SOP", summary: "Register a SOP push token", forbidden: true},
		{path: "/api/notifications/sop", method: "GET", tag: "SOP", summary: "List notifications by SOP token (X-Total header)"},
		{path: "/api/notifications/sop/stream", method: "GET", tag: "SOP", summary: "Stream notifications over SSE"},

		{path: "/api/admin/email-plans", method: "POST", tag: "Email Plans", summary: "Create an email sending plan", forbidden: true},
		{path: "/api/admin/email-plans", method: "GET", tag: "Email Plans", summary: "List email sending plans (X-Total header)", forbidden: true},
		{path: "/api/admin/email-plans/{planId}", method: "GET", tag: "Email Plans", summary: "Get an email sending plan", params: []string{"planId"}, forbidden: true, notFound: true},
		{path: "/api/admin/email-plans/{planId}/pause", method: "POST", tag: "Email Plans", summary: "Pause an email sending plan", params: []string{"planId"}, forbidden: true, notFound: true},
		{path: "/api/admin/email-plans/{planId}/resume", method: "POST", tag: "Email Plans", summary: "Resume an email sending plan", params: []string{"planId"}, forbidden: true, notFound: true},
		{path: "/api/admin/email-plans/{planId}/advance", method: "POST", tag: "Email Plans", summary: "Advance an email sending plan manually", params: []string{"planId"}, forbidden: true, notFound: true},

		{path: "/api/admin/delivery-observability/emails", method: "GET", tag: "Delivery Observability", summary: "Email delivery overview", forbidden: true},
		{path: "/api/admin/delivery-observability/notifications", method: "GET", tag: "Delivery Observability", summary: "Notification delivery overview", forbidden: true},
		{path: "/api/admin/stats", method: "GET", tag: "Admin", summary: "Notification statistics", forbidden: true},
	},
)

// ServeSwagger serves the static OpenAPI manifest at
// /swagger/v1/swagger.json.
var ServeSwagger = serveSwagger(ringSwagger)

// swaggerUIHTML is a minimal Swagger UI page (dev tool; mirrors the
// Swashbuckle /swagger page).
const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>DysonNetwork.Ring — Swagger</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = function () {
      SwaggerUIBundle({ url: "/swagger/v1/swagger.json", dom_id: "#swagger-ui" });
    };
  </script>
</body>
</html>`

// ServeSwaggerUI serves the Swagger UI page at /swagger.
func ServeSwaggerUI(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerUIHTML))
}

// serveSwagger returns a handler serving the static manifest.
func serveSwagger(doc openAPIDoc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, doc)
	}
}
