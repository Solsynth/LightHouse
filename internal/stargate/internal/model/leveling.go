package model

import "math"

// Leveling mirrors DysonNetwork.Shared.Models.Leveling. In the C# fleet
// Level, LevelingProgress and SocialCreditsLevel are [NotMapped] computed
// properties (never stored), so Stargate derives them on every profile load
// via ComputeLeveling instead of persisting them.

const (
	levelingMaxLevel = 120
	levelingBaseExp  = 100.0
	levelingK        = 3.52 // balance tweak, matches the C# constant
)

// ExpForLevel is the single-level XP requirement (from L to L+1).
func ExpForLevel(level int) float64 {
	if level < 1 {
		return 0
	}
	return levelingBaseExp + levelingK*math.Pow(float64(level-1), 2)
}

// TotalExpForLevel is the cumulative XP required to reach a level.
func TotalExpForLevel(level int) float64 {
	if level < 1 {
		return 0
	}
	l := float64(level)
	return levelingBaseExp*float64(level) + levelingK*((l-1)*l*(2*l-1))/6.0
}

// LevelFromExp mirrors Leveling.GetLevelFromExp.
func LevelFromExp(xp int) int {
	if xp < 0 {
		return 0
	}
	level := 0
	for level < levelingMaxLevel && TotalExpForLevel(level+1) <= float64(xp) {
		level++
	}
	return level
}

// LevelingProgressFromExp mirrors Leveling.GetProgressToNextLevel (0.0-1.0).
func LevelingProgressFromExp(xp int) float64 {
	current := LevelFromExp(xp)
	if current >= levelingMaxLevel {
		return 1.0
	}
	prev := TotalExpForLevel(current)
	next := TotalExpForLevel(current + 1)
	return (float64(xp) - prev) / (next - prev)
}

// SocialCreditsLevelFrom mirrors the SnAccountProfile computed property
// (including its quirks: 100 -> 1, 101-199 -> 0).
func SocialCreditsLevelFrom(credits float64) int {
	switch {
	case credits < 100:
		return -1
	case credits > 100 && credits < 200:
		return 0
	case credits < 200:
		return 1
	default:
		return 2
	}
}

// ComputeLeveling fills the derived leveling fields of a profile, mirroring
// the C# [NotMapped] computed properties.
func (p *Profile) ComputeLeveling() {
	p.Level = LevelFromExp(p.Experience)
	p.LevelingProgress = LevelingProgressFromExp(p.Experience)
	p.SocialCreditsLevel = SocialCreditsLevelFrom(p.SocialCredits)
}
