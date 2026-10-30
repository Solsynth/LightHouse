package progression

import (
	"embed"
	"io/fs"
)

//go:embed badgeicons/*.svg
var badgeIcons embed.FS

// BadgeIcons is the icon set shipped with the module. Passport served these
// from Resources/BadgeIcons, configured through [Badges] iconsPath; a
// deployment that does not configure that option still gets the manifest
// icon_url values and real SVG bytes.
func BadgeIcons() fs.FS {
	sub, err := fs.Sub(badgeIcons, "badgeicons")
	if err != nil {
		panic(err)
	}
	return sub
}
