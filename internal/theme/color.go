package theme

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Color is a theme color literal. Themes never carry CSS functions, gradients or
// keywords: a resolved variable is injected verbatim into a stylesheet, so the
// only value that may survive validation is a plain color.
type Color struct {
	R, G, B, A uint8
}

// Hex renders the color the way a theme file and the web resolver both spell it:
// lowercase #rrggbb, widened to #rrggbbaa only when the color is translucent.
func (c Color) Hex() string {
	if c.A == 0xff {
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
}

// ParseColor reads the strict theme-file color syntax: #rgb, #rrggbb or
// #rrggbbaa. Anything else — a named color, rgb(), a bare hex without the hash —
// is rejected, because theme values reach the browser as raw CSS.
func ParseColor(s string) (Color, error) {
	if !strings.HasPrefix(s, "#") {
		return Color{}, fmt.Errorf("%q is not a hex color (want #rgb, #rrggbb or #rrggbbaa)", s)
	}
	digits := s[1:]
	switch len(digits) {
	case 3, 6, 8:
	default:
		return Color{}, fmt.Errorf("%q is not a hex color (want #rgb, #rrggbb or #rrggbbaa)", s)
	}
	for _, r := range digits {
		if !isHexDigit(r) {
			return Color{}, fmt.Errorf("%q is not a hex color (want #rgb, #rrggbb or #rrggbbaa)", s)
		}
	}
	if len(digits) == 3 {
		digits = string([]byte{digits[0], digits[0], digits[1], digits[1], digits[2], digits[2]})
	}
	c := Color{A: 0xff}
	c.R = hexByte(digits[0:2])
	c.G = hexByte(digits[2:4])
	c.B = hexByte(digits[4:6])
	if len(digits) == 8 {
		c.A = hexByte(digits[6:8])
	}
	return c, nil
}

// ParseOverride reads a THEME_<ROLE> config value. Config has always accepted a
// bare hex without the leading hash, so the override syntax stays wider than the
// theme-file syntax ParseColor enforces.
func ParseOverride(s string) (Color, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Color{}, false
	}
	if !strings.HasPrefix(s, "#") {
		s = "#" + s
	}
	c, err := ParseColor(s)
	return c, err == nil
}

// mix blends from toward to by t in sRGB byte space, rounding half away from
// zero. Derivation is a documented contract (docs/themes.md), so the blend has
// to stay this literal — no gamma correction, no perceptual space.
func mix(from, to Color, t float64) Color {
	return Color{
		R: mixChannel(from.R, to.R, t),
		G: mixChannel(from.G, to.G, t),
		B: mixChannel(from.B, to.B, t),
		A: mixChannel(from.A, to.A, t),
	}
}

func mixChannel(from, to uint8, t float64) uint8 {
	v := math.Round(float64(from) + (float64(to)-float64(from))*t)
	return uint8(math.Min(math.Max(v, 0), 255))
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func hexByte(s string) uint8 {
	v, _ := strconv.ParseUint(s, 16, 8)
	return uint8(v)
}
