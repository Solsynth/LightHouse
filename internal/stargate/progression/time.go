package progression

import (
	"encoding/json"
	"time"
)

// TimeValue is the nullable instant the state listings and admin views emit:
// UTC RFC3339 with second precision, matching model.Time.
type TimeValue time.Time

// TimeOf converts a *time.Time into the wire value (nil stays nil).
func TimeOf(value *time.Time) *TimeValue {
	if value == nil || value.IsZero() {
		return nil
	}
	converted := TimeValue(value.UTC())
	return &converted
}

// MarshalJSON renders the instant the way the fleet's model.Time does.
func (t TimeValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC().Format(time.RFC3339))
}

// UnmarshalJSON accepts an RFC3339 string (or NodaTime's 7-digit fraction).
func (t *TimeValue) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		parsed, err = time.Parse("2006-01-02T15:04:05.9999999Z07:00", text)
		if err != nil {
			return err
		}
	}
	*t = TimeValue(parsed.UTC())
	return nil
}

// Time dereferences the wire value.
func (t *TimeValue) Time() time.Time {
	if t == nil {
		return time.Time{}
	}
	return time.Time(*t)
}
