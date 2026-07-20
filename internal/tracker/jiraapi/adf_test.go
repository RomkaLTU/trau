package jiraapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// buildADF upgrades the markdown constructs the pipeline writes to real ADF
// nodes: headings carry their level, emphasis/code/links become marks, fenced
// blocks keep their language, and list items land in real lists — so Jira
// renders formatted content instead of the literal syntax.
func TestBuildADFMarkdownStructure(t *testing.T) {
	doc := buildADF(strings.Join([]string{
		"## Root cause",
		"",
		"The `buildADF` writer is **flat**, see [docs](https://example.com/adf).",
		"",
		"```go",
		"func main() {}",
		"```",
		"",
		"- one",
		"- two",
		"",
		"3. third",
	}, "\n"))

	c := doc.Content
	if len(c) != 5 {
		t.Fatalf("blocks = %d (%+v), want 5", len(c), c)
	}
	if c[0].Type != "heading" || c[0].Attrs["level"] != 2 || c[0].Content[0].Text != "Root cause" {
		t.Errorf("heading = %+v, want level-2 heading %q", c[0], "Root cause")
	}

	para := c[1]
	if para.Type != "paragraph" || len(para.Content) != 7 {
		t.Fatalf("paragraph = %+v, want 7 inline nodes", para)
	}
	if n := para.Content[1]; n.Text != "buildADF" || len(n.Marks) != 1 || n.Marks[0].Type != "code" {
		t.Errorf("code span = %+v, want %q with a code mark", n, "buildADF")
	}
	if n := para.Content[3]; n.Text != "flat" || len(n.Marks) != 1 || n.Marks[0].Type != "strong" {
		t.Errorf("bold span = %+v, want %q with a strong mark", n, "flat")
	}
	if n := para.Content[5]; n.Text != "docs" || len(n.Marks) != 1 || n.Marks[0].Type != "link" || n.Marks[0].Attrs["href"] != "https://example.com/adf" {
		t.Errorf("link span = %+v, want %q linked to the docs URL", n, "docs")
	}

	if c[2].Type != "codeBlock" || c[2].Attrs["language"] != "go" || c[2].Content[0].Text != "func main() {}" {
		t.Errorf("code block = %+v, want a go codeBlock with the fence body", c[2])
	}
	if c[3].Type != "bulletList" || len(c[3].Content) != 2 || c[3].Content[0].Content[0].Content[0].Text != "one" {
		t.Errorf("bullet list = %+v, want two listItems", c[3])
	}
	if c[4].Type != "orderedList" || c[4].Attrs["order"] != 3 || c[4].Content[0].Content[0].Content[0].Text != "third" {
		t.Errorf("ordered list = %+v, want an order-3 orderedList", c[4])
	}
}

// A description mixing every supported construct round-trips through buildADF
// and back out of adfToText as the same markdown, so sync reading back a
// description trau wrote sees the structure rather than a re-flattened wall.
func TestBuildADFMarkdownRoundTrip(t *testing.T) {
	in := strings.Join([]string{
		"# Goal",
		"",
		"Ship **structured** Jira bodies with `code` and *emphasis*.",
		"",
		"```go",
		"func main() {}",
		"```",
		"",
		"- one",
		"- two",
		"  - nested",
		"",
		"1. first",
		"2. second",
		"",
		"| File | Role |",
		"| --- | --- |",
		"| `client.go` | writer |",
		"",
		"---",
		"",
		"See [the ADR](https://example.com/adr-17).",
	}, "\n")

	raw, err := json.Marshal(buildADF(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := adfToText(raw); got != in {
		t.Errorf("round-trip = %q, want %q", got, in)
	}
}

// A code span nested in bold or italic keeps only the marks ADF permits on
// code — the mark itself plus an enclosing link — since Jira rejects any
// document combining code with emphasis.
func TestBuildADFCodeSpanInsideEmphasisDropsEmphasis(t *testing.T) {
	doc := buildADF("**run `make build` now** and *try `go vet`* via [see `docs`](https://example.com/d)")
	para := doc.Content[0]
	if len(para.Content) != 9 {
		t.Fatalf("inline nodes = %+v, want 9", para.Content)
	}
	if n := para.Content[0]; n.Text != "run " || len(n.Marks) != 1 || n.Marks[0].Type != "strong" {
		t.Errorf("bold text = %+v, want a strong mark", n)
	}
	if n := para.Content[1]; n.Text != "make build" || len(n.Marks) != 1 || n.Marks[0].Type != "code" {
		t.Errorf("code span in bold = %+v, want a lone code mark", n)
	}
	if n := para.Content[5]; n.Text != "go vet" || len(n.Marks) != 1 || n.Marks[0].Type != "code" {
		t.Errorf("code span in italic = %+v, want a lone code mark", n)
	}
	if n := para.Content[8]; n.Text != "docs" || len(n.Marks) != 2 || n.Marks[0].Type != "link" || n.Marks[1].Type != "code" {
		t.Errorf("code span in link = %+v, want link and code marks", n)
	}
}

// Consecutive plain lines share one paragraph split by hard breaks; blank
// lines separate paragraphs — both survive the trip back through adfToText.
func TestBuildADFParagraphGrouping(t *testing.T) {
	doc := buildADF("one\ntwo\n\nthree")
	if len(doc.Content) != 2 {
		t.Fatalf("blocks = %+v, want 2 paragraphs", doc.Content)
	}
	first := doc.Content[0]
	if len(first.Content) != 3 || first.Content[1].Type != "hardBreak" {
		t.Errorf("first paragraph = %+v, want text/hardBreak/text", first)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := adfToText(raw); got != "one\ntwo\n\nthree" {
		t.Errorf("round-trip = %q, want the blank line kept", got)
	}
}

// Broken or unsupported markdown never drops content: unmatched delimiters,
// image references, would-be tables and quotes all come back verbatim as the
// plain paragraphs the writer used to send.
func TestBuildADFUnparseableFallsBackToPlainText(t *testing.T) {
	cases := []string{
		"**unclosed bold",
		"`unclosed code",
		"a * b * c",
		"snake_case_stays_flat",
		"#hashtag not a heading",
		"![shot.png](attachment://shot.png)",
		"| not | a table",
		"> just a quoted line",
		"12345678901. too long for a list",
	}
	for _, in := range cases {
		raw, err := json.Marshal(buildADF(in))
		if err != nil {
			t.Fatalf("marshal buildADF(%q): %v", in, err)
		}
		if got := adfToText(raw); got != in {
			t.Errorf("fallback round-trip: adfToText(buildADF(%q)) = %q", in, got)
		}
	}
}

// An empty body still ships a single empty paragraph, the shape Jira accepts
// for a cleared description.
func TestBuildADFEmptyBody(t *testing.T) {
	doc := buildADF("")
	if len(doc.Content) != 1 || doc.Content[0].Type != "paragraph" || len(doc.Content[0].Content) != 0 {
		t.Fatalf("empty body = %+v, want one empty paragraph", doc.Content)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := adfToText(raw); got != "" {
		t.Errorf("adfToText = %q, want empty", got)
	}
}

// A fence left unclosed swallows the rest of the input as code instead of
// dropping it or leaking stray fence syntax.
func TestBuildADFUnclosedFenceKeepsContent(t *testing.T) {
	doc := buildADF("```sh\nmake build\nmake test")
	if len(doc.Content) != 1 || doc.Content[0].Type != "codeBlock" {
		t.Fatalf("blocks = %+v, want one codeBlock", doc.Content)
	}
	if doc.Content[0].Content[0].Text != "make build\nmake test" {
		t.Errorf("code body = %q, want both lines", doc.Content[0].Content[0].Text)
	}
}

// A fenced body's blank runs survive the trip back out: the blank-line
// collapse that tidies block spacing leaves code fences alone.
func TestBuildADFCodeBlockBlankLinesRoundTrip(t *testing.T) {
	in := "```\nfirst\n\n\nsecond\n```"
	raw, err := json.Marshal(buildADF(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := adfToText(raw); got != in {
		t.Errorf("round-trip = %q, want %q", got, in)
	}
}

// A Jira-authored body renders back as markdown: marks become syntax, headings
// gain their hash prefix, ordered lists keep their start number, code blocks
// their fences, quotes their markers — and mentions survive via their attrs.
func TestADFToMarkdownRendersJiraAuthoredStructure(t *testing.T) {
	doc := adfBody(`{"type":"heading","attrs":{"level":3},"content":[{"type":"text","text":"Plan"}]},` +
		`{"type":"paragraph","content":[{"type":"text","text":"run "},{"type":"text","text":"trau","marks":[{"type":"code"}]},{"type":"text","text":" via "},{"type":"text","text":"the hub","marks":[{"type":"link","attrs":{"href":"https://trau.sh"}}]}]},` +
		`{"type":"orderedList","attrs":{"order":4},"content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"step"}]}]}]},` +
		`{"type":"codeBlock","attrs":{"language":"sh"},"content":[{"type":"text","text":"make build"}]},` +
		`{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"quoted"}]}]},` +
		`{"type":"paragraph","content":[{"type":"text","text":"ping "},{"type":"mention","attrs":{"id":"1","text":"@rd"}}]}`)
	want := strings.Join([]string{
		"### Plan",
		"",
		"run `trau` via [the hub](https://trau.sh)",
		"",
		"4. step",
		"",
		"```sh",
		"make build",
		"```",
		"",
		"> quoted",
		"",
		"ping @rd",
	}, "\n")
	if got := adfToText(doc); got != want {
		t.Errorf("adfToText = %q, want %q", got, want)
	}
}

// A table survives the round trip as GFM rows, and a nested list stays under
// its parent item with its indentation.
func TestADFToMarkdownTableAndNestedList(t *testing.T) {
	doc := adfBody(`{"type":"table","content":[` +
		`{"type":"tableRow","content":[{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"File"}]}]},{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Role"}]}]}]},` +
		`{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"adf.go"}]}]},{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"writer"}]}]}]}]},` +
		`{"type":"bulletList","content":[{"type":"listItem","content":[` +
		`{"type":"paragraph","content":[{"type":"text","text":"outer"}]},` +
		`{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"inner"}]}]}]}]}]}`)
	want := strings.Join([]string{
		"| File | Role |",
		"| --- | --- |",
		"| adf.go | writer |",
		"",
		"- outer",
		"  - inner",
	}, "\n")
	if got := adfToText(doc); got != want {
		t.Errorf("adfToText = %q, want %q", got, want)
	}
}
