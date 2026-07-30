package azureapi

import "testing"

func TestHTMLToMarkdown(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"blank", "   \n ", ""},
		{"plain text passes through", "Just a sentence.", "Just a sentence."},
		{"entities are decoded", "a &amp; b &lt;c&gt; &quot;d&quot;", `a & b <c> "d"`},
		{"non-breaking spaces normalize", "one&nbsp;two", "one two"},
		{"paragraphs separate", "<p>One</p><p>Two</p>", "One\n\nTwo"},
		{"divs separate", "<div>One</div><div>Two</div>", "One\n\nTwo"},
		{"breaks are single newlines", "One<br>Two<br />Three", "One\nTwo\nThree"},
		{"bold and italic", "<b>b</b> <strong>s</strong> <i>i</i> <em>e</em>", "**b** **s** *i* *e*"},
		{"inline code", "run <code>make test</code>", "run `make test`"},
		{"headings by level", "<h1>One</h1><h3>Three</h3>", "# One\n\n### Three"},
		{"unordered list", "<ul><li>a</li><li>b</li></ul>", "- a\n- b"},
		{"ordered list numbers", "<ol><li>a</li><li>b</li><li>c</li></ol>", "1. a\n2. b\n3. c"},
		{"nested list indents", "<ul><li>a<ul><li>b</li></ul></li></ul>", "- a\n  - b"},
		{"links", `see <a href="https://example.com">docs</a>`, "see [docs](https://example.com)"},
		{"link href entities decode", `<a href="/x?a=1&amp;b=2">x</a>`, "[x](/x?a=1&b=2)"},
		{"images", `<img src="https://example.com/a.png" alt="shot">`, "![shot](https://example.com/a.png)"},
		{"image without src is dropped", `<img alt="shot">`, ""},
		{"unknown tags drop but keep text", "<span class=\"x\">kept</span>", "kept"},
		{"blank lines collapse", "<p>One</p><p></p><p></p><p>Two</p>", "One\n\nTwo"},
		{"stray angle bracket stays text", "a < b", "a < b"},
	}
	for _, tc := range cases {
		if got := htmlToMarkdown(tc.in); got != tc.want {
			t.Errorf("%s: htmlToMarkdown(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// The Azure DevOps editor wraps each list item's body in its own div. The bullet
// must keep its text on the same line rather than being orphaned above it.
func TestHTMLToMarkdownRealEditorOutput(t *testing.T) {
	in := `<div>Implement the thing.</div><div><br></div><div>Steps:</div>` +
		`<ul><li><div>First&nbsp;step</div></li><li><div>Second step</div></li></ul>`
	want := "Implement the thing.\n\nSteps:\n- First step\n- Second step"
	if got := htmlToMarkdown(in); got != want {
		t.Errorf("htmlToMarkdown = %q, want %q", got, want)
	}
}

func TestTextToHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single block", "Hello", "<div>Hello</div>"},
		{"single newline becomes a break", "a\nb", "<div>a<br>b</div>"},
		{"blank line splits blocks", "a\n\nb", "<div>a</div><div>b</div>"},
		{"crlf normalizes", "a\r\n\r\nb", "<div>a</div><div>b</div>"},
		{"markup is escaped", "<script>x</script> & y", "<div>&lt;script&gt;x&lt;/script&gt; &amp; y</div>"},
	}
	for _, tc := range cases {
		if got := textToHTML(tc.in); got != tc.want {
			t.Errorf("%s: textToHTML(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// A body written by trau and read back must survive the round trip, so the
// markdown injected into a prompt matches what was filed.
func TestTextToHTMLRoundTrips(t *testing.T) {
	in := "Trau QA blocked TRAU-1.\n\nFailures:\n- one\n- two"
	if got := htmlToMarkdown(textToHTML(in)); got != in {
		t.Errorf("round trip = %q, want %q", got, in)
	}
}

func TestParseTag(t *testing.T) {
	cases := []struct {
		raw     string
		name    string
		closing bool
		href    string
	}{
		{"br", "br", false, ""},
		{"br /", "br", false, ""},
		{"/ul", "ul", true, ""},
		{"P", "p", false, ""},
		{`a href="https://x"`, "a", false, "https://x"},
		{`a target="_blank" href='https://y'`, "a", false, "https://y"},
		{`a href=unquoted`, "a", false, ""},
	}
	for _, tc := range cases {
		name, attrs, closing := parseTag(tc.raw)
		if name != tc.name || closing != tc.closing || attrs["href"] != tc.href {
			t.Errorf("parseTag(%q) = (%q, href=%q, closing=%v), want (%q, href=%q, closing=%v)",
				tc.raw, name, attrs["href"], closing, tc.name, tc.href, tc.closing)
		}
	}
}
