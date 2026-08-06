package adminctl

// Port of Padlock's AccountGeographyStatsAdminController
// (/api/admin/stats/users/geography): latest session location per account
// aggregated by country or city.

import (
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
)

// accountGeographyBucket mirrors AccountGeographyBucket.
type accountGeographyBucket struct {
	CountryCode string  `json:"country_code"`
	Country     *string `json:"country,omitempty"`
	City        *string `json:"city,omitempty"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	UserCount   int64   `json:"user_count"`
}

// accountGeographyStatsResponse mirrors AccountGeographyStatsResponse.
type accountGeographyStatsResponse struct {
	CalculatedAt         model.Time               `json:"calculated_at"`
	Since                model.Time               `json:"since"`
	Precision            string                   `json:"precision"`
	AccountsWithLocation int64                    `json:"accounts_with_location"`
	Buckets              []accountGeographyBucket `json:"buckets"`
}

func registerGeography(g *gin.RouterGroup, d Deps) {
	g.GET("", requirePerm(d, permission.AccountsView), getUserGeography(d))
}

func getUserGeography(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		precision := strings.ToLower(strings.TrimSpace(c.DefaultQuery("precision", "country")))
		if precision != "country" && precision != "city" {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_STATS_PRECISION_INVALID", "Precision must be either country or city.", http.StatusBadRequest))
			return
		}
		now := time.Now().UTC()
		startAt := now.Add(-30 * 24 * time.Hour)
		if since, ok := parseTimeParam(c, "since"); ok && since != nil {
			startAt = *since
		}
		if startAt.After(now) {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_STATS_SINCE_FUTURE", "Since cannot be in the future.", http.StatusBadRequest))
			return
		}

		locations, err := d.Store.AdminLatestAccountLocations(c.Request.Context(), startAt)
		if err != nil {
			serverError(c, err, d)
			return
		}

		type bucketAgg struct {
			location geoLocationRef
			count    int64
			latSum   float64
			lonSum   float64
		}
		grouped := map[string]*bucketAgg{}
		var order []string
		for _, entry := range locations {
			loc := entry.Location
			if strings.TrimSpace(loc.CountryCode) == "" || loc.Latitude == nil || loc.Longitude == nil {
				continue
			}
			if precision == "city" && strings.TrimSpace(loc.City) == "" {
				continue
			}
			key := strings.ToUpper(loc.CountryCode)
			if precision == "city" {
				key = key + ":" + strings.TrimSpace(loc.City)
			}
			agg, ok := grouped[key]
			if !ok {
				agg = &bucketAgg{location: geoLocationRef{countryCode: loc.CountryCode, country: &loc.Country, city: loc.City}}
				grouped[key] = agg
				order = append(order, key)
			}
			agg.count++
			agg.latSum += *loc.Latitude
			agg.lonSum += *loc.Longitude
		}

		buckets := make([]accountGeographyBucket, 0, len(order))
		for _, key := range order {
			agg := grouped[key]
			bucket := accountGeographyBucket{
				CountryCode: strings.ToUpper(agg.location.countryCode),
				Latitude:    round1(agg.latSum / float64(agg.count)),
				Longitude:   round1(agg.lonSum / float64(agg.count)),
				UserCount:   agg.count,
			}
			if agg.location.country != nil {
				bucket.Country = agg.location.country
			}
			if precision == "city" {
				city := strings.TrimSpace(agg.location.city)
				bucket.City = &city
			}
			buckets = append(buckets, bucket)
		}
		// Order by user count desc, then country code, then city (the C#
		// ordering; city comparison applies only for city precision).
		sortGeographyBuckets(buckets, precision)

		c.JSON(http.StatusOK, accountGeographyStatsResponse{
			CalculatedAt:         model.Time(now),
			Since:                model.Time(startAt),
			Precision:            precision,
			AccountsWithLocation: int64(len(locations)),
			Buckets:              buckets,
		})
	}
}

type geoLocationRef struct {
	countryCode string
	country     *string
	city        string
}

// round1 mirrors Math.Round(x, 1, MidpointRounding.AwayFromZero).
func round1(value float64) float64 {
	return math.Round(value*10) / 10
}

func sortGeographyBuckets(buckets []accountGeographyBucket, precision string) {
	for i := 1; i < len(buckets); i++ {
		for j := i; j > 0; j-- {
			a, b := buckets[j-1], buckets[j]
			swap := b.UserCount > a.UserCount
			if b.UserCount == a.UserCount {
				if b.CountryCode < a.CountryCode {
					swap = true
				} else if b.CountryCode == a.CountryCode && precision == "city" {
					ca, cb := "", ""
					if a.City != nil {
						ca = *a.City
					}
					if b.City != nil {
						cb = *b.City
					}
					swap = cb < ca
				}
			}
			if !swap {
				break
			}
			buckets[j-1], buckets[j] = buckets[j], buckets[j-1]
		}
	}
}
