// Package website renders the embedded public application welcome page.
package website

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"

	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
)

// welcomeHTML is the self-contained welcome page. Its Kiln colors and small
// corner radii follow website/src/styles/kiln.css. Fonts and the favicon are
// embedded; the only application-provided template value is its public name.
//
//go:embed welcome.html
var welcomeHTML string

// welcomeGeistRegular is Geist 400 from Google Fonts (Geist v5).
// The WOFF2 subset includes Latin, punctuation, and arrows, retaining font
// metadata. Its SIL Open Font License is in fonts/Geist-OFL.txt.
//
//go:embed fonts/geist-regular.woff2
var welcomeGeistRegular []byte

// welcomeGeistMedium is the matching Geist 500 subset for headings.
//
//go:embed fonts/geist-medium.woff2
var welcomeGeistMedium []byte

// welcomeMapleMedium is a matching subset of the website's Maple Mono 500.
// Its SIL Open Font License is in fonts/Maple-Mono-OFL.txt.
//
//go:embed fonts/maple-mono-medium.woff2
var welcomeMapleMedium []byte

// welcomePage holds the public name and trusted, package-owned font data URLs.
type welcomePage struct {
	// Name is the application name, escaped as ordinary template text.
	Name string
	// GeistRegular is the bundled body font URL.
	GeistRegular template.URL
	// GeistMedium is the bundled heading font URL.
	GeistMedium template.URL
	// MapleMedium is the bundled label font URL.
	MapleMedium template.URL
}

// welcomeFontURL encodes package-owned WOFF2 bytes for the inline stylesheet.
// Only bundled font bytes may cross this trusted template URL boundary.
func welcomeFontURL(font []byte) template.URL {
	return template.URL("data:font/woff2;base64," + base64.StdEncoding.EncodeToString(font))
}

// Handler renders the embedded public page without operational details.
func Handler(name string) router.Handler {
	page, err := template.New("welcome").Parse(welcomeHTML)
	data := welcomePage{
		Name:         name,
		GeistRegular: welcomeFontURL(welcomeGeistRegular),
		GeistMedium:  welcomeFontURL(welcomeGeistMedium),
		MapleMedium:  welcomeFontURL(welcomeMapleMedium),
	}

	return func(writer http.ResponseWriter, _ *http.Request) error {
		if err != nil {
			return fmt.Errorf("application: parse welcome page: %w", err)
		}

		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; "+
			"font-src data:; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")

		return response.HTMLTemplate(writer, http.StatusOK, page, data)
	}
}
