package model

import (
	"time"

	"src.solsynth.dev/sosys/go/pkg/models"
)

// NowTime returns the current UTC instant as a models.Time.
func NowTime() models.Time {
	return models.Time(time.Now().UTC())
}

// TimePtr returns a *models.Time for t (nil when zero).
func TimePtr(t time.Time) *models.Time {
	return models.NewTime(t)
}
