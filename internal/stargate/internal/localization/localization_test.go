package localization

import "testing"

func TestLocalizeNotificationUsesLocaleAndArguments(t *testing.T) {
	got := Localize("zh-CN", "newLoginBody", map[string]string{
		"deviceName": "MacBook",
		"ipAddress":  "192.0.2.1",
	})
	want := "您的账户已在名为 MacBook 的设备上登录，IP 地址为 192.0.2.1"
	if got != want {
		t.Fatalf("Localize() = %q, want %q", got, want)
	}
}

func TestLocalizeFallsBackToEnglish(t *testing.T) {
	got := Localize("fr-FR", "authCodeBody", map[string]string{"code": "123456"})
	want := "123456 is your verification code. It expires in 5 minutes."
	if got != want {
		t.Fatalf("Localize() = %q, want %q", got, want)
	}
}

func TestLocalizeUnknownKeyReturnsKey(t *testing.T) {
	if got := Localize("en", "missingKey", nil); got != "missingKey" {
		t.Fatalf("Localize() = %q, want missingKey", got)
	}
}
