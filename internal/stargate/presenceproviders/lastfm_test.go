package presenceproviders

import "testing"

func TestParseLastFMNowPlaying(t *testing.T) {
	raw := []byte(`{
		"recenttracks": {
			"track": [{
				"artist": {"#text": "Run The Jewels", "mbid": "artist-mbid"},
				"name": "Walking In The Snow",
				"mbid": "track-mbid",
				"album": {"#text": "RTJ4"},
				"url": "https://www.last.fm/music/x/_/y",
				"image": [
					{"#text": "https://img/small.png", "size": "small"},
					{"#text": "https://img/large.png", "size": "large"}
				],
				"@attr": {"nowplaying": "true"}
			}]
		}
	}`)
	track, apiErr, err := parseLastFMNowPlaying(raw)
	if err != nil || apiErr != nil {
		t.Fatalf("parse: track=%v apiErr=%v err=%v", track, apiErr, err)
	}
	if track == nil {
		t.Fatal("expected a now-playing track")
	}
	if track.Name != "Walking In The Snow" || track.Artist != "Run The Jewels" {
		t.Errorf("track = %+v", track)
	}
	if track.Album != "RTJ4" || track.MBID != "track-mbid" {
		t.Errorf("track = %+v", track)
	}
	if track.ImageURL != "https://img/large.png" {
		t.Errorf("image = %q", track.ImageURL)
	}
}

func TestParseLastFMNotNowPlaying(t *testing.T) {
	raw := []byte(`{"recenttracks":{"track":[{"name":"Old","artist":{"#text":"A"},"@attr":{"nowplaying":"false"}}]}}`)
	track, apiErr, err := parseLastFMNowPlaying(raw)
	if err != nil || apiErr != nil || track != nil {
		t.Fatalf("got track=%v apiErr=%v err=%v", track, apiErr, err)
	}
}

func TestParseLastFMError(t *testing.T) {
	raw := []byte(`{"error":17,"message":"Login required"}`)
	_, apiErr, err := parseLastFMNowPlaying(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if apiErr == nil || apiErr.Code != 17 || apiErr.Message != "Login required" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
}

func TestParseStage(t *testing.T) {
	for input, want := range map[string]Stage{
		"Active": StageActive,
		"maybe":  StageMaybe,
		" COLD ": StageCold,
	} {
		got, ok := ParseStage(input)
		if !ok || got != want {
			t.Errorf("ParseStage(%q) = %q, %v", input, got, ok)
		}
	}
	if _, ok := ParseStage("hot"); ok {
		t.Error("hot should not parse")
	}
}
