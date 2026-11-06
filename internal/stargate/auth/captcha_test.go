package auth

import (
	"net/url"
	"testing"
)

// TestCaptchaVerifyFormEncodesToken pins the form encoding: the token is
// attacker-controlled, and raw concatenation let `x&secret=ATTACKER` inject a
// second `secret` parameter into the provider request.
func TestCaptchaVerifyFormEncodesToken(t *testing.T) {
	const secret = "s3cret"
	const token = "x&secret=ATTACKER"
	values, err := url.ParseQuery(captchaVerifyForm(secret, token))
	if err != nil {
		t.Fatalf("parse form: %v", err)
	}
	if got := values["secret"]; len(got) != 1 || got[0] != secret {
		t.Fatalf("secret = %v, want exactly [%q]", got, secret)
	}
	if got := values.Get("response"); got != token {
		t.Fatalf("response = %q, want %q", got, token)
	}
}
