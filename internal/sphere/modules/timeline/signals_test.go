package timeline

import (
	"context"
	"log/slog"
	"testing"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

type fakeSignals struct {
	sponsored *model.Post
	shown     []string
	err       error
}

func (f *fakeSignals) IncrementViews(context.Context, string, string) error { return nil }

func (f *fakeSignals) RecordInterest(context.Context, string, int, string, float64, string) error {
	return nil
}

func (f *fakeSignals) SponsoredPost(context.Context) (*model.Post, error) {
	return f.sponsored, f.err
}

func (f *fakeSignals) MarkSponsoredShown(_ context.Context, postID string) error {
	f.shown = append(f.shown, postID)
	return nil
}

func postWithID(id string) model.Post { return model.Post{ID: id} }

func idsOf(posts []model.Post) []string {
	out := make([]string, 0, len(posts))
	for i := range posts {
		out = append(out, posts[i].ID)
	}
	return out
}

func TestInsertSponsoredBehaviour(t *testing.T) {
	ad := postWithID("ad")
	cases := []struct {
		name      string
		posts     []model.Post
		ad        *model.Post
		want      []string
		wantShown bool
	}{
		{
			name:      "inserts at the ad slot index",
			posts:     []model.Post{postWithID("a"), postWithID("b"), postWithID("c"), postWithID("d"), postWithID("e")},
			ad:        &ad,
			want:      []string{"a", "b", "c", "d", "ad", "e"},
			wantShown: true,
		},
		{
			name:      "appends when the page is shorter than the index",
			posts:     []model.Post{postWithID("a")},
			ad:        &ad,
			want:      []string{"a", "ad"},
			wantShown: true,
		},
		{
			name:  "skips when the sponsored post is already on the page",
			posts: []model.Post{postWithID("a"), postWithID("ad")},
			ad:    &ad,
			want:  []string{"a", "ad"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signals := &fakeSignals{sponsored: tc.ad}
			svc := &service{log: slog.Default(), signals: signals}
			out := idsOf(svc.insertSponsored(context.Background(), tc.posts))
			if len(out) != len(tc.want) {
				t.Fatalf("got %v want %v", out, tc.want)
			}
			for i := range out {
				if out[i] != tc.want[i] {
					t.Fatalf("got %v want %v", out, tc.want)
				}
			}
			if tc.wantShown && len(signals.shown) != 1 {
				t.Fatalf("expected one impression, got %v", signals.shown)
			}
			if !tc.wantShown && len(signals.shown) != 0 {
				t.Fatalf("expected no impression, got %v", signals.shown)
			}
		})
	}
}

func TestInsertSponsoredDegradesWithoutPortOrProvider(t *testing.T) {
	posts := []model.Post{postWithID("a")}
	svc := &service{log: slog.Default()}
	if out := idsOf(svc.insertSponsored(context.Background(), posts)); len(out) != 1 {
		t.Fatalf("page changed without the postsignals port")
	}
	svc.signals = &fakeSignals{sponsored: nil}
	if out := idsOf(svc.insertSponsored(context.Background(), posts)); len(out) != 1 {
		t.Fatalf("page changed with no active sponsor")
	}
	svc.signals = &fakeSignals{sponsored: &posts[0], err: context.DeadlineExceeded}
	if out := idsOf(svc.insertSponsored(context.Background(), posts)); len(out) != 1 {
		t.Fatalf("page changed when the port failed")
	}
}
