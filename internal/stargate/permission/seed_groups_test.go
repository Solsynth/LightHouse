package permission

import "testing"

// TestBadgeAdminKeyIsNotSelfService pins the split between self-service badge
// activation (progression.badges.manage, granted to every account) and badge
// administration (grant/revoke/force-activate on other accounts), which must
// stay a moderator capability. Both keys used to be the same, so any account
// could grant itself or others arbitrary badges.
func TestBadgeAdminKeyIsNotSelfService(t *testing.T) {
	for _, keys := range [][]string{defaultPermissionKeys, verifiedPermissionKeys} {
		for _, key := range keys {
			if key == ProgressionBadgesAdmin {
				t.Fatalf("%q must not be granted by a self-service group", key)
			}
		}
	}
	// Self-service activation stays available to every account.
	found := false
	for _, key := range defaultPermissionKeys {
		if key == ProgressionBadgesManage {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%q must stay in the default group for self-service activation", ProgressionBadgesManage)
	}
	found = false
	for _, key := range moderatorPermissionKeys {
		if key == ProgressionBadgesAdmin {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%q must be granted to the moderator group", ProgressionBadgesAdmin)
	}
}
