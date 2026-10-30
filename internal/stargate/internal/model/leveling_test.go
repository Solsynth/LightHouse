package model

import (
	"math"
	"testing"
)

// TestLevelingMath pins the Leveling port against hand-computed C# values
// (Leveling.GetLevelFromExp / GetProgressToNextLevel / TotalExpForLevel).
func TestLevelingMath(t *testing.T) {
	// TotalExpForLevel: 100*L + 3.52*(L-1)*L*(2L-1)/6
	cases := []struct {
		level int
		want  float64
	}{
		{0, 0},
		{1, 100},
		{2, 200 + 3.52*1*2*3/6.0}, // 203.52
		{3, 300 + 3.52*2*3*5/6.0}, // 317.6
	}
	for _, tc := range cases {
		if got := TotalExpForLevel(tc.level); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("TotalExpForLevel(%d) = %v, want %v", tc.level, got, tc.want)
		}
	}

	// Level boundaries: level L starts at TotalExpForLevel(L).
	levelCases := []struct {
		xp   int
		want int
	}{
		{-5, 0},
		{0, 0},
		{99, 0},
		{100, 1},
		{203, 1},
		{204, 2}, // TotalExpForLevel(2) = 203.52, int comparison 204 > 203.52
	}
	for _, tc := range levelCases {
		if got := LevelFromExp(tc.xp); got != tc.want {
			t.Errorf("LevelFromExp(%d) = %d, want %d", tc.xp, got, tc.want)
		}
	}

	// Progress: xp 50 of [0, 100) = 0.5; at max level progress is 1.0.
	if got := LevelingProgressFromExp(50); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("LevelingProgressFromExp(50) = %v, want 0.5", got)
	}
	if got := LevelingProgressFromExp(0); got != 0 {
		t.Errorf("LevelingProgressFromExp(0) = %v, want 0", got)
	}
	if got := LevelingProgressFromExp(10_000_000); got != 1.0 {
		t.Errorf("LevelingProgressFromExp(max) = %v, want 1.0", got)
	}
}

// TestSocialCreditsLevel pins the quirky C# switch:
// <100 -> -1, 100 -> 1, 101-199 -> 0, >=200 -> 2.
func TestSocialCreditsLevel(t *testing.T) {
	cases := []struct {
		credits float64
		want    int
	}{
		{0, -1},
		{99.9, -1},
		{100, 1},
		{150, 0},
		{199.9, 0},
		{200, 2},
		{500, 2},
	}
	for _, tc := range cases {
		if got := SocialCreditsLevelFrom(tc.credits); got != tc.want {
			t.Errorf("SocialCreditsLevelFrom(%v) = %d, want %d", tc.credits, got, tc.want)
		}
	}
}

// TestComputeLeveling pins the profile-level derivation used by the scans.
func TestComputeLeveling(t *testing.T) {
	p := &Profile{Experience: 204, SocialCredits: 100}
	p.ComputeLeveling()
	if p.Level != 2 {
		t.Errorf("Level = %d, want 2", p.Level)
	}
	if p.SocialCreditsLevel != 1 {
		t.Errorf("SocialCreditsLevel = %d, want 1", p.SocialCreditsLevel)
	}
}
