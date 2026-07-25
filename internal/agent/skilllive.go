package agent

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// skillTailKeep bounds the rolling window a skillCapture rescans, so a marker
// split across two writes still matches on the next one without unbounded growth.
const skillTailKeep = 8192

// maxUnmatchedSightings caps the debug list a capture keeps, so a redraw-heavy
// session cannot grow the event payload without bound.
const maxUnmatchedSightings = 16

// snapMinLen is the shortest sighting worth matching loosely: below it a handful
// of characters is a subsequence of half the inventory.
const snapMinLen = 4

// snapMaxEdits bounds the edit-distance match for a sighting whose characters
// were not merely dropped.
const snapMaxEdits = 2

// skillCapture collects Skill invocations from an agent's live terminal output as
// they are drawn, so a phase's loaded skills are known without waiting for the
// session transcript to flush. It is an io.Writer teed into the transcript drain;
// every sighting is snapped to the repo's own skill inventory, and what snaps to
// nothing is kept aside for debugging rather than reported as loaded.
type skillCapture struct {
	extract func(string) []string
	snap    skillSnapper

	mu        sync.Mutex
	sighted   map[string]bool
	loaded    []string
	unmatched []string
	tail      strings.Builder
}

func newSkillCapture(extract func(string) []string, snap skillSnapper) *skillCapture {
	return &skillCapture{extract: extract, snap: snap, sighted: map[string]bool{}}
}

func (c *skillCapture) Write(p []byte) (int, error) {
	if c == nil || c.extract == nil {
		return len(p), nil
	}
	c.mu.Lock()
	c.tail.Write(p)
	text := c.tail.String()
	for _, sighting := range c.extract(text) {
		if c.sighted[sighting] {
			continue
		}
		c.sighted[sighting] = true
		if name, ok := c.snap.snap(sighting); ok {
			c.loaded = appendSkills(c.loaded, name)
		} else if len(c.unmatched) < maxUnmatchedSightings {
			c.unmatched = appendSkills(c.unmatched, sighting)
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

// skills returns the canonical names captured so far, oldest first. Safe to call
// while the drain goroutine is still writing.
func (c *skillCapture) skills() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.loaded...)
}

// unmatchedSightings returns the names read off the terminal that resolved to no
// installed skill.
func (c *skillCapture) unmatchedSightings() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.unmatched...)
}

// skillSnapper resolves a skill name read off a terminal onto the inventory the
// repo can actually load: a redrawn or wrapped line loses characters, so
// "wb-eature" is the terminal's account of "web-feature". A sighting that
// resolves to nothing, or to two entries equally well, resolves to nothing at
// all — the loaded set never carries a guess.
type skillSnapper struct{ inventory []string }

// newSkillSnapper de-duplicates the inventory: a repo that installs a known
// external skill names it twice, and two copies of one name would otherwise read
// as an ambiguous tie.
func newSkillSnapper(inventory []string) skillSnapper {
	return skillSnapper{inventory: appendSkills(nil, inventory...)}
}

func (s skillSnapper) snap(sighting string) (string, bool) {
	if slices.Contains(s.inventory, sighting) {
		return sighting, true
	}
	if len(sighting) < snapMinLen {
		return "", false
	}
	best, bestCost, ambiguous := "", 0, false
	for _, name := range s.inventory {
		cost, ok := snapCost(sighting, name)
		switch {
		case !ok:
		case best == "" || cost < bestCost:
			best, bestCost, ambiguous = name, cost, false
		case cost == bestCost:
			ambiguous = true
		}
	}
	if best == "" || ambiguous {
		return "", false
	}
	return best, true
}

// snapAll splits names into the ones that resolve to an inventory entry and the
// ones that resolve to nothing, each de-duplicated in first-seen order.
func (s skillSnapper) snapAll(names []string) (loaded, unmatched []string) {
	for _, name := range names {
		if snapped, ok := s.snap(name); ok {
			loaded = appendSkills(loaded, snapped)
		} else {
			unmatched = appendSkills(unmatched, name)
		}
	}
	return loaded, unmatched
}

// snapCost scores a sighting against one inventory name. Dropped characters are
// the observed damage, so a subsequence counts as long as it keeps at least half
// the name — which rejects a half-drawn prefix — and anything else has to land
// within snapMaxEdits.
func snapCost(sighting, name string) (int, bool) {
	dist := editDistance(sighting, name)
	if dist <= snapMaxEdits {
		return dist, true
	}
	if len(sighting)*2 >= len(name) && isSubsequence(sighting, name) {
		return dist, true
	}
	return 0, false
}

func isSubsequence(short, long string) bool {
	i := 0
	for j := 0; i < len(short) && j < len(long); j++ {
		if short[i] == long[j] {
			i++
		}
	}
	return i == len(short)
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			sub := prev[j-1]
			if a[i-1] != b[j-1] {
				sub++
			}
			curr[j] = min(sub, prev[j]+1, curr[j-1]+1)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
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
