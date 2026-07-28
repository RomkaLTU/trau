package azureapi

import (
	"html"
	"strconv"
	"strings"
)

// Azure DevOps stores every rich-text work-item field (System.Description,
// ReproSteps, AcceptanceCriteria, comment bodies) as an HTML fragment, so the
// tracker converts in both directions: to markdown for the build prompt, and back
// to minimal HTML for the bodies it writes.

// nbsp is the non-breaking space the Azure DevOps editor emits between words,
// normalized to an ordinary space so the markdown stays greppable.
const nbsp = "\u00a0"

// listLevel is one open list while converting. ordered numbers its items;
// unordered bullets them.
type listLevel struct {
	ordered bool
	n       int
}

// converter accumulates markdown while walking an HTML fragment.
type converter struct {
	b     strings.Builder
	lists []listLevel
	// href holds the link target of the anchor currently open, so the closing tag
	// can emit the markdown suffix after the text it wraps.
	href string
	// pending is how many newlines are owed before the next content lands. Holding
	// them back rather than writing them is what keeps the <div> the Azure DevOps
	// editor wraps each <li> body in from orphaning the bullet on its own line.
	pending int
	// afterMarker suppresses the block break that wrapper would otherwise insert
	// between a bullet and its text.
	afterMarker bool
}

// htmlToMarkdown renders an Azure DevOps rich-text fragment as markdown. Unknown
// tags are dropped and their text kept, so a body from a customized editor
// degrades to prose rather than leaking markup into the prompt. A field holding
// plain text (which Azure DevOps also permits) passes through unchanged beyond
// entity decoding, and a stray "<" in prose stays a stray "<".
func htmlToMarkdown(fragment string) string {
	if strings.TrimSpace(fragment) == "" {
		return ""
	}
	var c converter
	for i := 0; i < len(fragment); {
		if fragment[i] != '<' {
			next := strings.IndexByte(fragment[i:], '<')
			if next < 0 {
				next = len(fragment) - i
			}
			c.text(fragment[i : i+next])
			i += next
			continue
		}
		end := strings.IndexByte(fragment[i:], '>')
		if end < 0 {
			c.text(fragment[i:])
			break
		}
		c.tag(fragment[i+1 : i+end])
		i += end + 1
	}
	return tidy(c.b.String())
}

// text writes decoded body text, dropping the whitespace that only separates
// block elements.
func (c *converter) text(s string) {
	s = strings.ReplaceAll(html.UnescapeString(s), nbsp, " ")
	if c.pending > 0 && strings.TrimSpace(s) == "" {
		return
	}
	c.write(s)
}

// write flushes the owed newlines and appends s.
func (c *converter) write(s string) {
	if s == "" {
		return
	}
	if c.b.Len() > 0 {
		c.b.WriteString(strings.Repeat("\n", c.pending))
	}
	c.pending = 0
	if strings.TrimSpace(s) != "" {
		c.afterMarker = false
	}
	c.b.WriteString(s)
}

// breakLine records that n newlines are owed before whatever comes next. The
// largest request wins, so a line break never downgrades a paragraph break.
func (c *converter) breakLine(n int) {
	if c.afterMarker || c.b.Len() == 0 {
		return
	}
	if n > c.pending {
		c.pending = n
	}
}

func (c *converter) tag(raw string) {
	name, attrs, closing := parseTag(raw)
	switch name {
	case "br":
		c.breakLine(1)
	case "p", "div", "tr":
		c.breakLine(2)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		if closing {
			c.breakLine(2)
			return
		}
		level, _ := strconv.Atoi(name[1:])
		c.breakLine(2)
		c.write(strings.Repeat("#", level) + " ")
	case "b", "strong":
		c.write("**")
	case "i", "em":
		c.write("*")
	case "code":
		c.write("`")
	case "pre":
		if closing {
			c.breakLine(1)
			c.write("```")
			c.breakLine(2)
			return
		}
		c.breakLine(2)
		c.write("```")
		c.breakLine(1)
	case "ul", "ol":
		if closing {
			if len(c.lists) > 0 {
				c.lists = c.lists[:len(c.lists)-1]
			}
			c.breakLine(2)
			return
		}
		c.lists = append(c.lists, listLevel{ordered: name == "ol"})
		c.breakLine(1)
	case "li":
		if closing {
			return
		}
		// One newline exactly: a list stays tight however its items are wrapped.
		c.pending = 1
		c.write(c.marker())
		c.afterMarker = true
	case "td", "th":
		if !closing {
			c.write(" | ")
		}
	case "a":
		if closing {
			c.write("](" + c.href + ")")
			c.href = ""
			return
		}
		c.href = attrs["href"]
		c.write("[")
	case "img":
		if src := attrs["src"]; src != "" {
			c.write("![" + attrs["alt"] + "](" + src + ")")
		}
	}
}

// marker renders the list bullet or number for the innermost open list, indented
// two spaces per nesting level.
func (c *converter) marker() string {
	if len(c.lists) == 0 {
		return "- "
	}
	level := &c.lists[len(c.lists)-1]
	level.n++
	indent := strings.Repeat("  ", len(c.lists)-1)
	if level.ordered {
		return indent + strconv.Itoa(level.n) + ". "
	}
	return indent + "- "
}

// parseTag splits a raw tag body ("a href=\"x\"", "/ul", "br /") into its
// lower-cased name, its attributes, and whether it closes an element.
func parseTag(raw string) (name string, attrs map[string]string, closing bool) {
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "/"))
	if closing = strings.HasPrefix(raw, "/"); closing {
		raw = raw[1:]
	}
	name, rest, _ := strings.Cut(strings.TrimSpace(raw), " ")
	return strings.ToLower(name), parseAttrs(rest), closing
}

// parseAttrs reads the quoted attributes off a tag body. Azure DevOps emits
// double-quoted values, but single quotes are accepted too; unquoted values are
// ignored, since none of the attributes read here appear unquoted.
func parseAttrs(rest string) map[string]string {
	attrs := map[string]string{}
	for {
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			return attrs
		}
		key := strings.ToLower(strings.TrimSpace(rest[:eq]))
		rest = strings.TrimSpace(rest[eq+1:])
		if rest == "" || (rest[0] != '"' && rest[0] != '\'') {
			return attrs
		}
		quote := rest[0]
		end := strings.IndexByte(rest[1:], quote)
		if end < 0 {
			return attrs
		}
		attrs[key] = html.UnescapeString(rest[1 : end+1])
		rest = rest[end+2:]
	}
}

// tidy strips trailing whitespace from every line and collapses runs of blank
// lines to one, so a fragment whose tags nest more deeply than its content still
// reads as prose.
func tidy(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// textToHTML renders plain text (or markdown) as the minimal HTML an Azure
// DevOps rich-text field stores: blank-line-separated blocks become divs and
// single newlines become breaks. The text is escaped, so a body carrying angle
// brackets cannot inject markup into the work item.
func textToHTML(text string) string {
	var b strings.Builder
	for _, block := range strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\n\n") {
		if block = strings.TrimSpace(block); block == "" {
			continue
		}
		b.WriteString("<div>" + strings.ReplaceAll(html.EscapeString(block), "\n", "<br>") + "</div>")
	}
	return b.String()
}
