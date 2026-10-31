package swagger

// Builder configures one documentation page during Handler construction.
// Retaining the draft does not permit changes to the published handler.
//
// Example:
//
//	configure := func(page *swagger.Builder) {
//		page.Title("Billing API")
//	}
//	_, err := swagger.Handler("/docs", "/openapi.json", configure)
//	if err != nil {
//		log.Fatal(err)
//	}
type Builder struct {
	// title is plain text, escaped when inserted into the HTML document.
	title string
}

// BuilderFunc configures a fresh Builder synchronously during Handler.
// Callbacks run once in order; nil entries and callback panics propagate.
//
// Example:
//
//	var configure swagger.BuilderFunc = func(page *swagger.Builder) {
//		page.Title("Billing API")
//	}
//	_, err := swagger.Handler("/docs", "/openapi.json", configure)
//	if err != nil {
//		log.Fatal(err)
//	}
type BuilderFunc = func(*Builder)

// Title replaces the browser-tab title with plain text and returns the draft.
// The default is "API documentation". An empty title remains empty; HTML in
// the value is displayed as text rather than executed.
//
// Example:
//
//	_, err := swagger.Handler("/docs", "/openapi.json", func(page *swagger.Builder) {
//		page.Title("Billing API")
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
func (page *Builder) Title(title string) *Builder {
	page.title = title

	return page
}
