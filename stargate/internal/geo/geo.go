// Package geo wraps the GeoLite2 MaxMind database for IP geolocation,
// mirroring Padlock's GeoService.
package geo

import (
	"errors"
	"net"
	"sync"

	"github.com/oschwald/geoip2-golang"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Service resolves IP addresses to GeoPoints and country codes.
type Service struct {
	mu     sync.RWMutex
	reader *geoip2.Reader
	path   string
}

// NewService creates a geo service; the database is loaded lazily so a
// missing database does not prevent startup.
func NewService(path string) *Service {
	return &Service{path: path}
}

func (s *Service) ensureOpen() (*geoip2.Reader, error) {
	s.mu.RLock()
	if s.reader != nil {
		r := s.reader
		s.mu.RUnlock()
		return r, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reader != nil {
		return s.reader, nil
	}
	if s.path == "" {
		return nil, errors.New("geoip database path is not configured")
	}
	reader, err := geoip2.Open(s.path)
	if err != nil {
		return nil, err
	}
	s.reader = reader
	return reader, nil
}

// GetPointFromIp resolves an IP to a GeoPoint (nil when resolution fails).
func (s *Service) GetPointFromIp(ipAddress string) *model.GeoPoint {
	reader, err := s.ensureOpen()
	if err != nil {
		return nil
	}
	ip := net.ParseIP(ipAddress)
	if ip == nil {
		return nil
	}
	record, err := reader.City(ip)
	if err != nil {
		return nil
	}
	lat := record.Location.Latitude
	lon := record.Location.Longitude
	point := &model.GeoPoint{
		Latitude:    &lat,
		Longitude:   &lon,
		CountryCode: record.Country.IsoCode,
		Country:     record.Country.Names["en"],
		City:        record.City.Names["en"],
	}
	return point
}

// GetCountryCodeFromIp resolves an IP to its ISO country code.
func (s *Service) GetCountryCodeFromIp(ipAddress string) string {
	reader, err := s.ensureOpen()
	if err != nil {
		return ""
	}
	ip := net.ParseIP(ipAddress)
	if ip == nil {
		return ""
	}
	record, err := reader.City(ip)
	if err != nil {
		return ""
	}
	return record.Country.IsoCode
}
