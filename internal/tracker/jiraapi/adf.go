package jiraapi

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// adfNode is one node of an Atlassian Document Format tree. Text carries inline
// content and Marks its formatting; Content holds child nodes. Attrs stays raw
// because its shape varies by node type — decoding it eagerly would let one
// unfamiliar node blank an entire description.
type adfNode struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Attrs   json.RawMessage `json:"attrs"`
	Marks   []adfNodeMark   `json:"marks"`
	Content []adfNode       `json:"content"`
}

// adfNodeMark is an inline mark on a text node; Attrs carries the link href.
type adfNodeMark struct {
	Type  string          `json:"type"`
	Attrs json.RawMessage `json:"attrs"`
}

// adfMediaAttrs are a media node's attributes: id addresses the file in the
// issue's attachment list, url carries an externally hosted image directly, and
// alt is the caption Jira echoes for it.
type adfMediaAttrs struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	Alt string `json:"alt"`
}

// adfMedia resolves a document's media nodes against the issue's attachment list.
type adfMedia []Attachment

func (m adfMedia) byID(id string) (Attachment, bool) {
	for _, att := range m {
		if id != "" && att.ID == id {
			return att, true
		}
	}
	return Attachment{}, false
}

func (m adfMedia) byFilename(name string) (Attachment, bool) {
	for _, att := range m {
		if name != "" && strings.EqualFold(att.Filename, name) {
			return att, true
		}
	}
	return Attachment{}, false
}

// adfToText renders a v3 ADF document with no attachment list to resolve its
// embedded images against, so each one leaves a placeholder rather than a URL.
func adfToText(raw json.RawMessage) string {
	return adfToMarkdown(raw, nil)
}

// adfToMarkdown renders a v3 ADF document back to markdown — the inverse of
// buildADF — so a description trau wrote keeps its structure when sync reads it
// back, and a Jira-authored body arrives as readable text. Embedded media
// become markdown image references resolved against the issue's attachments.
// A missing or null body, or any decode failure, yields "" so callers treat an
// unreadable body as "no description" rather than an error.
func adfToMarkdown(raw json.RawMessage, media adfMedia) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	var doc adfNode
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(collapseBlankLines(renderADFBlocks(doc.Content, media, "\n\n")))
}

// renderADFBlocks renders sibling block nodes joined by sep — a blank line at
// the top level, a bare newline inside list items so lists stay tight.
func renderADFBlocks(nodes []adfNode, media adfMedia, sep string) string {
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if s := renderADFBlock(n, media); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, sep)
}

func renderADFBlock(n adfNode, media adfMedia) string {
	switch n.Type {
	case "paragraph":
		return renderADFInline(n.Content, media)
	case "heading":
		text := renderADFInline(n.Content, media)
		if text == "" {
			return ""
		}
		return strings.Repeat("#", headingLevelAttr(n.Attrs)) + " " + text
	case "codeBlock":
		return renderADFCodeBlock(n)
	case "bulletList":
		return renderADFList(n, media, false)
	case "orderedList":
		return renderADFList(n, media, true)
	case "blockquote":
		return prefixLines(renderADFBlocks(n.Content, media, "\n\n"), "> ")
	case "rule":
		return "---"
	case "table":
		return renderADFTable(n, media)
	case "mediaSingle", "mediaGroup":
		return renderADFInline(n.Content, media)
	case "media", "mediaInline":
		return mediaRef(n, media)
	default:
		return renderADFUnknown(n, media)
	}
}

// renderADFUnknown keeps the content of a node type the renderer has no shape
// for: a stray leaf's text, an attrs-borne payload, or its children rendered
// inline or as blocks — so an unfamiliar node degrades instead of vanishing.
func renderADFUnknown(n adfNode, media adfMedia) string {
	if n.Text != "" {
		return n.Text
	}
	if len(n.Content) == 0 {
		return adfAttrsText(n.Attrs)
	}
	if adfInlineOnly(n.Content) {
		return renderADFInline(n.Content, media)
	}
	return renderADFBlocks(n.Content, media, "\n\n")
}

func adfInlineOnly(nodes []adfNode) bool {
	for _, n := range nodes {
		switch n.Type {
		case "text", "hardBreak", "media", "mediaInline", "mention", "emoji", "status", "inlineCard", "date":
		default:
			return false
		}
	}
	return true
}

func renderADFInline(nodes []adfNode, media adfMedia) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(renderADFMarkedText(n))
		case "hardBreak":
			b.WriteByte('\n')
		case "media", "mediaInline":
			b.WriteString(mediaRef(n, media))
		default:
			switch {
			case n.Text != "":
				b.WriteString(n.Text)
			case len(n.Content) > 0:
				b.WriteString(renderADFInline(n.Content, media))
			default:
				b.WriteString(adfAttrsText(n.Attrs))
			}
		}
	}
	return b.String()
}

// renderADFMarkedText renders a text node with its marks as markdown syntax,
// applied in a fixed order — code innermost, then emphasis, strong, and the
// link wrapper outermost — regardless of the order the marks arrived in.
func renderADFMarkedText(n adfNode) string {
	var code, em, strong bool
	var href string
	for _, m := range n.Marks {
		switch m.Type {
		case "code":
			code = true
		case "em":
			em = true
		case "strong":
			strong = true
		case "link":
			var attrs struct {
				Href string `json:"href"`
			}
			_ = json.Unmarshal(m.Attrs, &attrs)
			href = attrs.Href
		}
	}
	text := n.Text
	if code {
		text = "`" + text + "`"
	}
	if em {
		text = "*" + text + "*"
	}
	if strong {
		text = "**" + text + "**"
	}
	if href != "" {
		text = "[" + text + "](" + href + ")"
	}
	return text
}

func headingLevelAttr(raw json.RawMessage) int {
	var attrs struct {
		Level int `json:"level"`
	}
	_ = json.Unmarshal(raw, &attrs)
	if attrs.Level < 1 {
		return 1
	}
	if attrs.Level > 6 {
		return 6
	}
	return attrs.Level
}

func renderADFCodeBlock(n adfNode) string {
	var attrs struct {
		Language string `json:"language"`
	}
	_ = json.Unmarshal(n.Attrs, &attrs)
	var body strings.Builder
	for _, c := range n.Content {
		body.WriteString(c.Text)
	}
	if body.Len() == 0 {
		return "```" + attrs.Language + "\n```"
	}
	return "```" + attrs.Language + "\n" + body.String() + "\n```"
}

func renderADFList(n adfNode, media adfMedia, ordered bool) string {
	start := orderedListStart(n.Attrs)
	items := make([]string, 0, len(n.Content))
	for i, item := range n.Content {
		marker := "- "
		if ordered {
			marker = strconv.Itoa(start+i) + ". "
		}
		body := renderADFBlocks(item.Content, media, "\n")
		items = append(items, marker+indentContinuation(body, len(marker)))
	}
	return strings.Join(items, "\n")
}

func orderedListStart(raw json.RawMessage) int {
	attrs := struct {
		Order int `json:"order"`
	}{Order: 1}
	_ = json.Unmarshal(raw, &attrs)
	if attrs.Order < 0 {
		return 1
	}
	return attrs.Order
}

// indentContinuation indents every line after the first by width spaces, so a
// list item's extra paragraphs and nested lists sit under its marker.
func indentContinuation(s string, width int) string {
	lines := strings.Split(s, "\n")
	if len(lines) == 1 {
		return s
	}
	pad := strings.Repeat(" ", width)
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func prefixLines(s, prefix string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(prefix+l, " ")
	}
	return strings.Join(lines, "\n")
}

// renderADFTable renders a table as GFM rows, collapsing each cell to one line
// and inserting the separator after the first row so the text re-parses as a
// table on the way back in.
func renderADFTable(n adfNode, media adfMedia) string {
	rows := make([]string, 0, len(n.Content)+1)
	for i, row := range n.Content {
		cells := make([]string, 0, len(row.Content))
		for _, cell := range row.Content {
			text := strings.Join(strings.Fields(renderADFBlocks(cell.Content, media, "\n")), " ")
			cells = append(cells, strings.ReplaceAll(text, "|", "\\|"))
		}
		rows = append(rows, "| "+strings.Join(cells, " | ")+" |")
		if i == 0 {
			seps := make([]string, len(cells))
			for j := range seps {
				seps[j] = "---"
			}
			rows = append(rows, "| "+strings.Join(seps, " | ")+" |")
		}
	}
	return strings.Join(rows, "\n")
}

// adfAttrsText recovers the readable payload of a leaf that keeps its content
// in attrs — a mention's "@name", an emoji's character, an inlineCard's URL —
// so those survive rendering instead of vanishing.
func adfAttrsText(raw json.RawMessage) string {
	var attrs struct {
		Text string `json:"text"`
		URL  string `json:"url"`
	}
	_ = json.Unmarshal(raw, &attrs)
	return firstNonEmpty(attrs.Text, attrs.URL)
}

// mediaRef renders a media node — the leaf inside a mediaSingle or mediaGroup —
// as a markdown image, so an embedded screenshot survives the flattening instead
// of vanishing from the stored body. An external node carries its URL directly; a
// file node resolves through the issue's attachments, by media id and then by the
// filename Jira echoes into alt. A node nothing resolves still leaves a trace, so
// a reader knows an image was there.
func mediaRef(n adfNode, media adfMedia) string {
	var attrs adfMediaAttrs
	if err := json.Unmarshal(n.Attrs, &attrs); err != nil {
		return "[image]"
	}
	if attrs.URL != "" {
		return "![" + attrs.Alt + "](" + attrs.URL + ")"
	}
	att, found := media.byID(attrs.ID)
	if !found {
		att, found = media.byFilename(attrs.Alt)
	}
	if found && att.Content != "" {
		return "![" + attrs.Alt + "](" + att.Content + ")"
	}
	if name := firstNonEmpty(attrs.Alt, attrs.ID); name != "" {
		return "[image: " + name + "]"
	}
	return "[image]"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// collapseBlankLines squeezes runs of blank lines down to a single one so
// nested block nodes don't stack extra spacing; fenced code bodies keep their
// blank runs verbatim.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inFence = !inFence
		} else if !inFence && l == "" && len(out) > 0 && out[len(out)-1] == "" {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// adfDoc is a v3 ADF document assembled for write bodies (transition comments,
// descriptions) — the counterpart of adfToMarkdown.
type adfDoc struct {
	Type    string     `json:"type"`
	Version int        `json:"version"`
	Content []adfBlock `json:"content"`
}

// adfBlock is one node of an assembled write tree; blocks, inline text and
// nested children share the shape, like adfNode on the read side.
type adfBlock struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []adfBlock     `json:"content,omitempty"`
	Text    string         `json:"text,omitempty"`
	Marks   []adfMark      `json:"marks,omitempty"`
}

type adfMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// buildADF converts markdown into the ADF document Jira v3 requires for
// comment and description bodies, upgrading the constructs the pipeline
// actually writes — headings, emphasis, inline code, links, fenced code
// blocks, lists, rules and tables — to real nodes so Jira renders them instead
// of showing the syntax. Anything it cannot parse stays a plain paragraph, so
// broken syntax degrades to unformatted text rather than losing content.
// adfToMarkdown recovers the markdown from what this emits.
func buildADF(text string) adfDoc {
	blocks := markdownBlocks(strings.Split(text, "\n"))
	if len(blocks) == 0 {
		blocks = append(blocks, adfBlock{Type: "paragraph"})
	}
	return adfDoc{Type: "doc", Version: 1, Content: blocks}
}

func markdownBlocks(lines []string) []adfBlock {
	blocks := []adfBlock{}
	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		var block adfBlock
		switch {
		case trimmed == "":
			i++
			continue
		case isFenceLine(trimmed):
			block, i = parseFence(lines, i)
		case isRuleLine(trimmed):
			block, i = adfBlock{Type: "rule"}, i+1
		case headingLevel(trimmed) > 0:
			block, i = headingBlock(trimmed), i+1
		case parseListLine(line).ok:
			block, i = parseList(lines, i)
		case isTableStart(lines, i):
			block, i = parseTable(lines, i)
		default:
			block, i = parseParagraph(lines, i)
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// startsNewBlock reports whether the line at i opens a construct that ends the
// paragraph being collected.
func startsNewBlock(lines []string, i int) bool {
	trimmed := strings.TrimSpace(lines[i])
	return trimmed == "" ||
		isFenceLine(trimmed) ||
		isRuleLine(trimmed) ||
		headingLevel(trimmed) > 0 ||
		parseListLine(lines[i]).ok ||
		isTableStart(lines, i)
}

// parseParagraph merges consecutive plain lines into one paragraph split by
// hard breaks, so multi-line prose reads back with its line breaks intact.
func parseParagraph(lines []string, i int) (adfBlock, int) {
	content := parseInline(lines[i], nil)
	i++
	for i < len(lines) && !startsNewBlock(lines, i) {
		content = append(content, adfBlock{Type: "hardBreak"})
		content = append(content, parseInline(lines[i], nil)...)
		i++
	}
	return adfBlock{Type: "paragraph", Content: content}, i
}

func isFenceLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// parseFence consumes a fenced code block. A fence left unclosed swallows the
// rest of the input as code, matching how the text visibly reads.
func parseFence(lines []string, i int) (adfBlock, int) {
	open := strings.TrimSpace(lines[i])
	ch := open[0]
	run := delimRun(open, 0, ch)
	lang := ""
	if fields := strings.Fields(open[run:]); len(fields) > 0 {
		lang = fields[0]
	}
	i++
	start := i
	for i < len(lines) && !isFenceClose(strings.TrimSpace(lines[i]), ch, run) {
		i++
	}
	body := strings.Join(lines[start:i], "\n")
	if i < len(lines) {
		i++
	}
	block := adfBlock{Type: "codeBlock"}
	if lang != "" {
		block.Attrs = map[string]any{"language": lang}
	}
	if body != "" {
		block.Content = []adfBlock{{Type: "text", Text: body}}
	}
	return block, i
}

func isFenceClose(trimmed string, ch byte, run int) bool {
	if len(trimmed) < run {
		return false
	}
	return delimRun(trimmed, 0, ch) == len(trimmed)
}

func isRuleLine(trimmed string) bool {
	if len(trimmed) < 3 {
		return false
	}
	ch := trimmed[0]
	if ch != '-' && ch != '*' && ch != '_' {
		return false
	}
	return delimRun(trimmed, 0, ch) == len(trimmed)
}

// headingLevel returns the ATX heading level of a line, or 0 when it is not a
// heading — the hashes must be 1–6, followed by a space and some text.
func headingLevel(trimmed string) int {
	level := delimRun(trimmed, 0, '#')
	if level < 1 || level > 6 || level == len(trimmed) || trimmed[level] != ' ' {
		return 0
	}
	if strings.TrimSpace(trimmed[level:]) == "" {
		return 0
	}
	return level
}

func headingBlock(trimmed string) adfBlock {
	level := headingLevel(trimmed)
	return adfBlock{
		Type:    "heading",
		Attrs:   map[string]any{"level": level},
		Content: parseInline(strings.TrimSpace(trimmed[level:]), nil),
	}
}

// listLine is one parsed list-item line: its marker kind, indent width, number
// (for ordered items) and remaining text.
type listLine struct {
	ok      bool
	ordered bool
	num     int
	indent  int
	text    string
}

func parseListLine(line string) listLine {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	rest := line[indent:]
	if rest == "" {
		return listLine{}
	}
	if c := rest[0]; c == '-' || c == '*' || c == '+' {
		if len(rest) == 1 {
			return listLine{ok: true, indent: indent}
		}
		if rest[1] == ' ' {
			return listLine{ok: true, indent: indent, text: strings.TrimLeft(rest[2:], " ")}
		}
		return listLine{}
	}
	digits := 0
	for digits < len(rest) && digits < 9 && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits == len(rest) || (rest[digits] != '.' && rest[digits] != ')') {
		return listLine{}
	}
	if digits+1 < len(rest) && rest[digits+1] != ' ' {
		return listLine{}
	}
	num, _ := strconv.Atoi(rest[:digits])
	return listLine{ok: true, ordered: true, num: num, indent: indent, text: strings.TrimLeft(rest[digits+1:], " ")}
}

// parseList collects consecutive items at the first line's indent into one
// list; a deeper-indented item opens a nested list inside the previous item,
// and a marker-kind switch or any non-item line ends the run.
func parseList(lines []string, i int) (adfBlock, int) {
	first := parseListLine(lines[i])
	indent := first.indent
	list := adfBlock{Type: "bulletList"}
	if first.ordered {
		list.Type = "orderedList"
		if first.num != 1 {
			list.Attrs = map[string]any{"order": first.num}
		}
	}
	for i < len(lines) {
		item := parseListLine(lines[i])
		if !item.ok || item.indent < indent {
			break
		}
		if item.indent >= indent+2 && len(list.Content) > 0 {
			last := &list.Content[len(list.Content)-1]
			var nested adfBlock
			nested, i = parseList(lines, i)
			last.Content = append(last.Content, nested)
			continue
		}
		if item.ordered != first.ordered {
			break
		}
		list.Content = append(list.Content, adfBlock{Type: "listItem", Content: []adfBlock{paragraphOf(item.text)}})
		i++
	}
	return list, i
}

func paragraphOf(text string) adfBlock {
	return adfBlock{Type: "paragraph", Content: parseInline(text, nil)}
}

// isTableStart reports whether the line at i begins a GFM table: a row with at
// least one pipe followed by a separator row of dashes.
func isTableStart(lines []string, i int) bool {
	if !strings.Contains(lines[i], "|") || i+1 >= len(lines) {
		return false
	}
	return isTableSeparator(lines[i+1])
}

func isTableSeparator(line string) bool {
	if !strings.Contains(line, "|") {
		return false
	}
	for _, c := range splitTableCells(line) {
		c = strings.TrimSuffix(strings.TrimPrefix(c, ":"), ":")
		if c == "" || strings.Trim(c, "-") != "" {
			return false
		}
	}
	return true
}

func parseTable(lines []string, i int) (adfBlock, int) {
	header := splitTableCells(lines[i])
	table := adfBlock{Type: "table", Content: []adfBlock{tableRow(header, len(header), "tableHeader")}}
	i += 2
	for i < len(lines) && strings.TrimSpace(lines[i]) != "" && strings.Contains(lines[i], "|") {
		table.Content = append(table.Content, tableRow(splitTableCells(lines[i]), len(header), "tableCell"))
		i++
	}
	return table, i
}

// tableRow builds one ADF row, padding short rows to the header width but
// keeping any extra cells rather than truncating them away.
func tableRow(cells []string, width int, cellType string) adfBlock {
	for len(cells) < width {
		cells = append(cells, "")
	}
	row := adfBlock{Type: "tableRow", Content: make([]adfBlock, 0, len(cells))}
	for _, c := range cells {
		row.Content = append(row.Content, adfBlock{Type: cellType, Content: []adfBlock{paragraphOf(c)}})
	}
	return row
}

// splitTableCells splits a table row on unescaped pipes, dropping the optional
// leading and trailing pipe and trimming each cell.
func splitTableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	cells := []string{}
	var cur strings.Builder
	for k := 0; k < len(line); k++ {
		switch {
		case line[k] == '\\' && k+1 < len(line) && line[k+1] == '|':
			cur.WriteByte('|')
			k++
		case line[k] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(line[k])
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// parseInline scans one line of markdown into ADF inline nodes, carrying the
// marks active in the enclosing span. A delimiter that never finds a valid
// closer is emitted as literal text, so broken syntax degrades to plain
// characters instead of dropping content.
func parseInline(s string, marks []adfMark) []adfBlock {
	nodes := []adfBlock{}
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			nodes = append(nodes, textNode(plain.String(), marks))
			plain.Reset()
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isMarkdownPunct(s[i+1]):
			plain.WriteByte(s[i+1])
			i += 2
		case c == '`':
			run := delimRun(s, i, '`')
			delim := s[i : i+run]
			if end := strings.Index(s[i+run:], delim); end > 0 {
				flush()
				nodes = append(nodes, textNode(s[i+run:i+run+end], codeSpanMarks(marks)))
				i += run + end + run
			} else {
				plain.WriteString(delim)
				i += run
			}
		case c == '*' || c == '_':
			run := delimRun(s, i, c)
			if inner, next, ok := matchEmphasis(s, i, run, c); ok {
				flush()
				nodes = append(nodes, parseInline(inner, appendEmphasisMarks(marks, run))...)
				i = next
			} else {
				plain.WriteString(s[i : i+run])
				i += run
			}
		case c == '[':
			if label, href, next, ok := matchLink(s, i); ok {
				flush()
				nodes = append(nodes, parseInline(label, appendMark(marks, linkMark(href)))...)
				i = next
			} else {
				plain.WriteByte(c)
				i++
			}
		case c == '!' && i+1 < len(s) && s[i+1] == '[':
			// Image references stay literal text: ADF media nodes need an
			// uploaded attachment, which markdown syntax cannot supply.
			if _, _, next, ok := matchLink(s, i+1); ok {
				plain.WriteString(s[i:next])
				i = next
			} else {
				plain.WriteByte(c)
				i++
			}
		default:
			plain.WriteByte(c)
			i++
		}
	}
	flush()
	return nodes
}

// matchEmphasis finds the closing delimiter for an emphasis run opened at i,
// returning the inner text and the index just past the closer. The opener must
// touch the following word and, for underscores, must not sit inside one —
// otherwise snake_case identifiers would sprout italics.
func matchEmphasis(s string, i, run int, ch byte) (inner string, next int, ok bool) {
	if run > 3 {
		return "", 0, false
	}
	if i+run >= len(s) || isSpaceByte(s[i+run]) {
		return "", 0, false
	}
	if ch == '_' && i > 0 && isWordByte(s[i-1]) {
		return "", 0, false
	}
	delim := s[i : i+run]
	for from := i + run; ; {
		idx := strings.Index(s[from:], delim)
		if idx < 0 {
			return "", 0, false
		}
		j := from + idx
		invalid := j == i+run ||
			isSpaceByte(s[j-1]) ||
			s[j-1] == '\\' ||
			(ch == '_' && j+run < len(s) && isWordByte(s[j+run]))
		if invalid {
			from = j + 1
			continue
		}
		return s[i+run : j], j + run, true
	}
}

func matchLink(s string, i int) (label, href string, next int, ok bool) {
	end := strings.IndexByte(s[i:], ']')
	if end < 0 || i+end+1 >= len(s) || s[i+end+1] != '(' {
		return "", "", 0, false
	}
	closeParen := strings.IndexByte(s[i+end+2:], ')')
	if closeParen < 0 {
		return "", "", 0, false
	}
	label = s[i+1 : i+end]
	href = s[i+end+2 : i+end+2+closeParen]
	if label == "" || href == "" || strings.ContainsAny(href, " \t") {
		return "", "", 0, false
	}
	return label, href, i + end + 2 + closeParen + 1, true
}

func textNode(text string, marks []adfMark) adfBlock {
	n := adfBlock{Type: "text", Text: text}
	if len(marks) > 0 {
		n.Marks = append([]adfMark{}, marks...)
	}
	return n
}

func linkMark(href string) adfMark {
	return adfMark{Type: "link", Attrs: map[string]any{"href": href}}
}

func appendEmphasisMarks(marks []adfMark, run int) []adfMark {
	switch run {
	case 1:
		return appendMark(marks, adfMark{Type: "em"})
	case 2:
		return appendMark(marks, adfMark{Type: "strong"})
	default:
		return appendMark(appendMark(marks, adfMark{Type: "strong"}), adfMark{Type: "em"})
	}
}

// codeSpanMarks builds a code span's marks from the enclosing span's: ADF lets
// code combine only with link, so enclosing emphasis is dropped rather than
// emitting a combination Jira rejects outright.
func codeSpanMarks(marks []adfMark) []adfMark {
	out := []adfMark{}
	for _, m := range marks {
		if m.Type == "link" {
			out = append(out, m)
		}
	}
	return append(out, adfMark{Type: "code"})
}

// appendMark returns a fresh slice with m appended, replacing any mark of the
// same type — nested identical delimiters must not stack duplicate marks,
// which ADF rejects.
func appendMark(marks []adfMark, m adfMark) []adfMark {
	out := make([]adfMark, 0, len(marks)+1)
	for _, existing := range marks {
		if existing.Type != m.Type {
			out = append(out, existing)
		}
	}
	return append(out, m)
}

func delimRun(s string, i int, ch byte) int {
	run := 0
	for i+run < len(s) && s[i+run] == ch {
		run++
	}
	return run
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' }

// isWordByte treats any non-ASCII byte as a word byte, keeping underscore
// emphasis conservative around multibyte text.
func isWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

func isMarkdownPunct(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}
