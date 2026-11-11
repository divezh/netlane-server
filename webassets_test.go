package webassets

import (
	"io/fs"
	"regexp"
	"testing"
)

// The console's CSP is style-src 'self': a style attribute in rendered markup is silently
// dropped by the browser. app.js writes data-css instead and applies it through the CSSOM.
func TestConsoleMarkupHasNoStyleAttributes(t *testing.T) {
	attribute := regexp.MustCompile(`\sstyle\s*=\s*["'$]`)
	for _, name := range []string{"web/index.html", "web/app.js"} {
		data, err := fs.ReadFile(application, name)
		if err != nil {
			t.Fatal(err)
		}
		if found := attribute.Find(data); found != nil {
			t.Errorf("%s has a style attribute (%q), which the console CSP blocks; use data-css", name, found)
		}
	}
}
