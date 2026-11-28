package eventbus

import (
	"reflect"
	"testing"
)

func TestSubjectIsSubsetMatch(t *testing.T) {
	tests := []struct {
		subj, pattern string
		want          bool
	}{
		{"timeline.pulsar.scrobble.created", "timeline.>", true},
		{"timeline.>", "timeline.>", true},
		{"timeline.pulsar.created", "timeline.*", false},
		{"timeline.pulsar", "timeline.*", true},
		{"timeline.>", "timeline.*", false},
		{"timeline.>", "timeline.pulsar.>", false},
		{"timeline.pulsar.created", "timeline.pulsar.created", true},
		{"timeline.pulsar.created", "timeline.pulsar.*", true},
		{"foo", "foo.>", false},
		{"foo.bar", "foo.>", true},
		{"foo.bar.baz", ">", true},
	}
	for _, tt := range tests {
		if got := subjectIsSubsetMatch(tt.subj, tt.pattern); got != tt.want {
			t.Errorf("subjectIsSubsetMatch(%q, %q) = %v, want %v", tt.subj, tt.pattern, got, tt.want)
		}
	}
}

func TestMergeSubjects(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		wanted   []string
		want     []string
	}{
		{
			// The production regression: timeline_events already registers
			// timeline.>, so adding a concrete publisher subject would make
			// JetStream reject the config (code 10052).
			name:     "covered by registered wildcard",
			existing: []string{"timeline.>"},
			wanted:   []string{"timeline.pulsar.scrobble.created"},
			want:     []string{"timeline.>"},
		},
		{
			name:     "wildcard replaces registered concrete",
			existing: []string{"timeline.pulsar.scrobble.created"},
			wanted:   []string{"timeline.>"},
			want:     []string{"timeline.>"},
		},
		{
			name:     "disjoint subjects union sorted",
			existing: []string{"payment_orders"},
			wanted:   []string{"filesystem.file.updated.v1"},
			want:     []string{"filesystem.file.updated.v1", "payment_orders"},
		},
		{
			name:     "duplicates collapse",
			existing: []string{"a.b"},
			wanted:   []string{"a.b"},
			want:     []string{"a.b"},
		},
		{
			name:     "star wildcard covers one token",
			existing: []string{"timeline.*"},
			wanted:   []string{"timeline.pulsar"},
			want:     []string{"timeline.*"},
		},
		{
			name:     "wanted pair pruned to broader",
			existing: nil,
			wanted:   []string{"timeline.pulsar.created", "timeline.>"},
			want:     []string{"timeline.>"},
		},
	}
	for _, tt := range tests {
		if got := mergeSubjects(tt.existing, tt.wanted); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: mergeSubjects(%v, %v) = %v, want %v", tt.name, tt.existing, tt.wanted, got, tt.want)
		}
	}
}
