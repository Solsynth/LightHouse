package store

import (
	"reflect"
	"testing"
)

// TestDecodeJSONArray verifies jsonb column bytes decode into a string slice,
// matching how the driver returns jsonb values (raw []byte).
func TestDecodeJSONArray(t *testing.T) {
	for name, tt := range map[string]struct {
		in   []byte
		want []string
	}{
		"empty array":  {in: []byte(`[]`), want: []string{}},
		"string array": {in: []byte(`["a","b"]`), want: []string{"a", "b"}},
		"empty bytes":  {in: nil, want: nil},
		"zero length":  {in: []byte{}, want: nil},
		"null literal": {in: []byte(`null`), want: nil},
		"invalid json": {in: []byte(`not-json`), want: nil},
		"wrong shape":  {in: []byte(`{"a":1}`), want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := decodeJSONArray(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decodeJSONArray(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
