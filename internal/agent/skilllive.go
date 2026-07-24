package agent

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// skillTailKeep bounds the rolling window a skillCapture rescans, so a marker
// split across two writes still matches on the next one without unbounded growth.
const skillTailKeep = 8192

// skillCapture collects Skill invocations from an agent's live terminal output as
// they are drawn, so a phase's loaded skills are known without waiting for the
// session transcript to flush. It is an io.Writer teed into the transcript drain;
// matches are deduplicated in first-seen order.
type skillCapture struct {
	extract func(string) []string

	mu    sync.Mutex
	seen  map[string]bool
	order []string
	tail  strings.Builder
}

func newSkillCapture(extract func(string) []string) *skillCapture {
	return &skillCapture{extract: extract, seen: map[string]bool{}}
}

func (c *skillCapture) Write(p []byte) (int, error) {
	if c == nil || c.extract == nil {
		return len(p), nil
	}
	c.mu.Lock()
	c.tail.Write(p)
	text := c.tail.String()
	for _, name := range c.extract(text) {
		if !c.seen[name] {
			c.seen[name] = true
			c.order = append(c.order, name)
		}
	}
	if len(text) > skillTailKeep {
		trimmed := text[len(text)-skillTailKeep:]
		c.tail.Reset()
		c.tail.WriteString(trimmed)
	}
	c.mu.Unlock()
	return len(p), nil
}

// skills returns the captured names so far, oldest first. Safe to call while the
// drain goroutine is still writing.
func (c *skillCapture) skills() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.order...)
}

var (
	skillCallRe   = regexp.MustCompile(`Skill\(([a-z0-9][a-z0-9._:-]*)\)`)
	skillLaunchRe = regexp.MustCompile(`(?i)Launching skill:[ \t]+([a-z0-9][a-z0-9._:-]*)`)
)

// claudeSkills extracts skill names from Claude Code's terminal output: the
// Skill(<name>) tool-call header and the "Launching skill: <name>" line. ANSI
// styling is stripped first so a name drawn with interleaved color or cursor codes
// still matches. Names are returned in order of appearance.
func claudeSkills(s string) []string {
	s = StripANSI(s)
	type hit struct {
		idx  int
		name string
	}
	var hits []hit
	for _, re := range []*regexp.Regexp{skillCallRe, skillLaunchRe} {
		for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
			hits = append(hits, hit{loc[0], s[loc[2]:loc[3]]})
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].idx < hits[j].idx })
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.name)
	}
	return out
}

// mergeSkills unions the live-captured skills with the transcript-parsed ones,
// live first so its order wins, then any the transcript adds. Reconciliation only
// ever adds — a skill seen live is never dropped because the file omits it.
func mergeSkills(live, transcript []string) []string {
	if len(live) == 0 && len(transcript) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(live)+len(transcript))
	out := make([]string, 0, len(live)+len(transcript))
	for _, group := range [][]string{live, transcript} {
		for _, name := range group {
			if name != "" && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}
