package spellctl

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/model"
)

func newTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	api := e.Group("/api")
	Register(api, Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return e
}

// TestSpellRoutesRegistered pins the route tree: the static
// contact-verification/resend path coexists with the :word / :id param
// routes without a gin panic, and every C# MagicSpellController path exists.
func TestSpellRoutesRegistered(t *testing.T) {
	e := newTestEngine()
	var paths []string
	for _, route := range e.Routes() {
		paths = append(paths, route.Method+" "+route.Path)
	}
	for _, want := range []string{
		"POST /api/spells/contact-verification/resend",
		"GET /api/spells/:word",
		"POST /api/spells/:word/apply",
		"POST /api/spells/:word/resend",
	} {
		found := false
		for _, got := range paths {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("route %q not registered; got %v", want, paths)
		}
	}
}

// TestResendContactVerificationRequiresAuth: the resend endpoint is
// [Authorize]-protected and returns the canonical 401 without a session.
func TestResendContactVerificationRequiresAuth(t *testing.T) {
	e := newTestEngine()
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/spells/contact-verification/resend", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestSpellAppliedAuditPasswordReset(t *testing.T) {
	action, meta, ok := spellAppliedAudit(&model.MagicSpell{
		Id:        "spell-1",
		Type:      model.MagicSpellTypeAuthPasswordReset,
		AccountId: "account-1",
	})
	if !ok {
		t.Fatal("password-reset spell produced no audit event")
	}
	if action != model.ActionLogAuthFactorResetPassword {
		t.Fatalf("action = %q, want %q", action, model.ActionLogAuthFactorResetPassword)
	}
	if meta["factor_type"] != "Password" || meta["source"] != "recovery_spell" || meta["spell_id"] != "spell-1" {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
}

func TestSpellAppliedAuditContactVerificationMasksAddress(t *testing.T) {
	action, meta, ok := spellAppliedAudit(&model.MagicSpell{
		Id:   "spell-2",
		Type: model.MagicSpellTypeContactVerification,
		Meta: map[string]any{
			"contact_id":     "contact-1",
			"contact_method": "alice@example.com",
		},
	})
	if !ok {
		t.Fatal("contact-verification spell produced no audit event")
	}
	if action != model.ActionLogAccountContactVerify {
		t.Fatalf("action = %q, want %q", action, model.ActionLogAccountContactVerify)
	}
	if meta["contact_id"] != "contact-1" {
		t.Fatalf("contact id missing: %#v", meta)
	}
	if meta["contact_hint"] != "a***@example.com" {
		t.Fatalf("contact hint = %v, want masked address", meta["contact_hint"])
	}
	if _, leaked := meta["contact_method"]; leaked {
		t.Fatal("audit metadata leaked the raw contact address")
	}
}

func TestSpellAppliedAuditIgnoresUnrelatedSpells(t *testing.T) {
	if _, _, ok := spellAppliedAudit(&model.MagicSpell{Type: model.MagicSpellTypeAccountRemoval}); ok {
		t.Fatal("unrelated spell should not produce an audit event")
	}
	if _, _, ok := spellAppliedAudit(nil); ok {
		t.Fatal("nil spell should not produce an audit event")
	}
}
