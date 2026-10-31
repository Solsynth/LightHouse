// Package ipcheckctl ports Passport's IpCheckController: the anonymous
// client geo lookup clients use as a weather/location fallback, and the
// admin-only diagnostic dump of client/remote IPs, forwarding headers and
// geo resolution.
package ipcheckctl

import (
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/stargate/ipcheck"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
)

// Deps is the dependency set the IP-check controller needs.
type Deps struct {
	Geo  ipcheck.GeoService
	Perm middleware.PermissionChecker
}

// Register mounts /api/ip-check.
func Register(api *gin.RouterGroup, d Deps) {
	group := api.Group("/ip-check")
	group.GET("/geo", d.getClientGeo)
	group.GET("", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.AdminIpCheck), d.check)
}

func (d Deps) getClientGeo(c *gin.Context) {
	if d.Geo == nil {
		geoNotFound(c)
		return
	}
	point := d.Geo.GetPointFromIp(middleware.ClientIP(c.Request))
	if point == nil {
		geoNotFound(c)
		return
	}
	if !ipcheck.HasCoordinates(point) {
		c.JSON(http.StatusNotFound, errs.New(
			"PASSPORT_IP_GEO_NO_COORDINATES", "Geo location has no coordinates.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, ipcheck.ToGeoResponse(point))
}

func (d Deps) check(c *gin.Context) {
	clientIP := middleware.ClientIP(c.Request)
	geo := ipcheck.ToGeoResponse(geoPoint(d.Geo, clientIP))

	response := ipcheck.IpCheckResponse{
		ClientIP:        optional(clientIP),
		RemoteIP:        optional(remoteIP(c.Request)),
		XForwardedFor:   header(c, "X-Forwarded-For"),
		XForwardedProto: header(c, "X-Forwarded-Proto"),
		XForwardedHost:  header(c, "X-Forwarded-Host"),
		XRealIP:         header(c, "X-Real-IP"),
		CFConnectingIP:  header(c, "CF-Connecting-IP"),
		Geo:             geo,
		Headers:         optional(headerDump(c.Request)),
	}
	c.JSON(http.StatusOK, response)
}

func geoPoint(service ipcheck.GeoService, clientIP string) *model.GeoPoint {
	if service == nil {
		return nil
	}
	return service.GetPointFromIp(clientIP)
}

// remoteIP mirrors HttpContext.Connection.RemoteIpAddress: the peer address
// without the port.
func remoteIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

func header(c *gin.Context, name string) *string {
	return optional(strings.Join(c.Request.Header.Values(name), ","))
}

// headerDump newline-joins every request header as "Name: value". Keys are
// sorted so the diagnostic output is stable between requests.
func headerDump(request *http.Request) string {
	keys := make([]string, 0, len(request.Header))
	for key := range request.Header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for i, key := range keys {
		if i > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(key)
		builder.WriteString(": ")
		builder.WriteString(strings.Join(request.Header.Values(key), ","))
	}
	return builder.String()
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func geoNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New(
		"PASSPORT_IP_GEO_NOT_FOUND", "Could not resolve geo location for client IP.", http.StatusNotFound))
}
