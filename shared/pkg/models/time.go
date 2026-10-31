// Package models mirrors DysonNetwork.Shared.Models (the NeTo module): the
// wire types shared by the fleet's Go services. JSON is snake_case with nulls
// omitted; enums serialize as integers; times serialize as UTC RFC3339 with
// second precision via Time.
package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Time is a UTC instant serialized as ISO-8601 (RFC3339, seconds precision,
// e.g. 2026-07-27T15:32:00Z), the Go counterpart of NodaTime's Instant.
// Null is represented by a nil *Time and omitted from JSON.
type Time time.Time

func (t Time) Time() time.Time { return time.Time(t) }

func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC().Format(time.RFC3339))
}

func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// NodaTime emits up to 7 fractional digits; Go's RFC3339 parser
		// accepts up to nanoseconds, so this fallback is defensive.
		parsed, err = time.Parse("2006-01-02T15:04:05.9999999Z07:00", s)
		if err != nil {
			return err
		}
	}
	*t = Time(parsed.UTC())
	return nil
}

// NewTime converts a time.Time into a *Time (nil for the zero value).
func NewTime(t time.Time) *Time {
	if t.IsZero() {
		return nil
	}
	v := Time(t.UTC())
	return &v
}

// Scan implements sql.Scanner so pgx can scan timestamptz columns into
// *Time fields (and &*Time targets).
func (t *Time) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*t = Time{}
	case time.Time:
		*t = Time(x.UTC())
	case string:
		return t.scanString(x)
	case []byte:
		return t.scanString(string(x))
	default:
		return fmt.Errorf("cannot scan %T into models.Time", v)
	}
	return nil
}

func (t *Time) scanString(s string) error {
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		parsed, err = time.Parse("2006-01-02T15:04:05.9999999Z07:00", s)
		if err != nil {
			return err
		}
	}
	*t = Time(parsed.UTC())
	return nil
}

// Value implements driver.Valuer so pgx can encode *Time arguments as
// timestamptz.
func (t *Time) Value() (driver.Value, error) {
	if t == nil {
		return nil, nil
	}
	return time.Time(*t).UTC(), nil
}

// MarshalText implements encoding.TextMarshaler (RFC3339 UTC).
func (t Time) MarshalText() ([]byte, error) {
	return []byte(time.Time(t).UTC().Format(time.RFC3339)), nil
}

// FromTime converts a *Time back to time.Time (zero when nil).
func (t *Time) FromTime() time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.Time()
}
