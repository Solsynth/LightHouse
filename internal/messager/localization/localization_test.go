package localization

import "testing"

func TestLocalizeEncryptedMessageBody(t *testing.T) {
	cases := map[string]string{
		"en-US": "You've received a message",
		"zh-CN": "您收到了一条消息",
		"fr-FR": "You've received a message",
		"":      "You've received a message",
	}
	for language, want := range cases {
		if got := Localize(language, "chatEncryptedMessageBody", nil); got != want {
			t.Errorf("Localize(%q) = %q, want %q", language, got, want)
		}
	}
}
