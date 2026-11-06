package actionlog

import (
	"strconv"
	"strings"

	"src.solsynth.dev/sosys/stargate/model"
)

// ContactTypeName renders a contact type the way action-log metadata should
// spell it: the C# enum name, not the numeric column value.
func ContactTypeName(t int) string {
	switch model.ContactType(t) {
	case model.ContactTypeEmail:
		return "Email"
	case model.ContactTypePhoneNumber:
		return "PhoneNumber"
	case model.ContactTypeAddress:
		return "Address"
	default:
		return strconv.Itoa(t)
	}
}

// MaskContactContent returns a short, non-reversible hint for an audit entry.
// The activity log is readable by the account owner and admins, but a stolen
// session should not turn it into an address-book dump: keep enough to tell
// which contact changed, never the whole value.
func MaskContactContent(t int, content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	switch model.ContactType(t) {
	case model.ContactTypeEmail:
		at := strings.LastIndexByte(content, '@')
		if at <= 0 || at == len(content)-1 {
			return maskMiddle(content, 1, 0)
		}
		local, domain := content[:at], content[at+1:]
		return maskMiddle(local, 1, 0) + "@" + domain
	case model.ContactTypePhoneNumber:
		return maskMiddle(content, 0, 2)
	default:
		return maskMiddle(content, 1, 0)
	}
}

// maskMiddle keeps the leading `head` and trailing `tail` runes and replaces
// everything between with "***". Values too short to hide anything are
// masked whole.
func maskMiddle(value string, head, tail int) string {
	runes := []rune(value)
	if len(runes) <= head+tail {
		return "***"
	}
	return string(runes[:head]) + "***" + string(runes[len(runes)-tail:])
}
