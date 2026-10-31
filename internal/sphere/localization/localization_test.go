package localization

import "testing"

func TestLocalize(t *testing.T) {
	cases := []struct {
		lang string
		key  string
		args map[string]string
		want string
	}{
		{
			lang: "en",
			key:  "postMentionTitle",
			args: map[string]string{"user": "Bob"},
			want: "Bob mentioned you in a post",
		},
		{
			lang: "zh-CN",
			key:  "postMentionTitle",
			args: map[string]string{"user": "鲍勃"},
			want: "鲍勃 在帖子中提到了您",
		},
		{
			lang: "zh-hans",
			key:  "postReactBody",
			args: map[string]string{"user": "Alice", "reaction": "👍"},
			want: "Alice 用 👍 做出了反应",
		},
		{
			lang: "fr", // fallback to en
			key:  "subscriberAddedTitle",
			args: map[string]string{"publisher": "Sphere News"},
			want: "You've been added to Sphere News",
		},
		{
			lang: "en",
			key:  "unknownKey",
			args: map[string]string{},
			want: "unknownKey",
		},
	}

	for _, c := range cases {
		got := Localize(c.lang, c.key, c.args)
		if got != c.want {
			t.Errorf("Localize(%q, %q) = %q, want %q", c.lang, c.key, got, c.want)
		}
	}
}
