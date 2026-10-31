// Package ipcheck is the ported Passport IP diagnostics domain: the public
// client geo lookup and the admin header/IP dump. It owns no tables; the
// answers come from the shared GeoIP service.
package ipcheck

import "src.solsynth.dev/sosys/stargate/model"

// ModuleID is the feature module identifier.
const ModuleID = "app.solian.id.ipcheck"

// GeoService is the shared GeoIP resolver (internal/geo.Service).
type GeoService interface {
	GetPointFromIp(ipAddress string) *model.GeoPoint
}

// GeoResponse is IpCheckController.GeoIpResponse. The shared GeoPoint only
// carries city/country/coordinates, so subdivision, continent and timezone
// stay nil (the C# response serialized them from MaxMind directly).
type GeoResponse struct {
	City            *string  `json:"city"`
	Country         *string  `json:"country"`
	CountryCode     *string  `json:"country_code"`
	Subdivision     *string  `json:"subdivision"`
	SubdivisionCode *string  `json:"subdivision_code"`
	ContinentCode   *string  `json:"continent_code"`
	TimeZone        *string  `json:"time_zone"`
	Latitude        *float64 `json:"latitude"`
	Longitude       *float64 `json:"longitude"`
}

// IpCheckResponse is IpCheckController.IpCheckResponse.
type IpCheckResponse struct {
	ClientIP        *string      `json:"client_ip"`
	RemoteIP        *string      `json:"remote_ip"`
	XForwardedFor   *string      `json:"x_forwarded_for"`
	XForwardedProto *string      `json:"x_forwarded_proto"`
	XForwardedHost  *string      `json:"x_forwarded_host"`
	XRealIP         *string      `json:"x_real_ip"`
	CFConnectingIP  *string      `json:"cf_connecting_ip"`
	Geo             *GeoResponse `json:"geo"`
	Headers         *string      `json:"headers"`
}

// ToGeoResponse maps a GeoPoint to the wire shape. An empty string field is
// omitted as nil, matching a missing MaxMind record.
func ToGeoResponse(point *model.GeoPoint) *GeoResponse {
	if point == nil {
		return nil
	}
	return &GeoResponse{
		City:        optionalString(point.City),
		Country:     optionalString(point.Country),
		CountryCode: optionalString(point.CountryCode),
		Latitude:    point.Latitude,
		Longitude:   point.Longitude,
	}
}

// HasCoordinates mirrors MaxMind's Location.HasCoordinates check.
func HasCoordinates(point *model.GeoPoint) bool {
	return point != nil && point.Latitude != nil && point.Longitude != nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
