package report

import (
	"fmt"
	"strings"
)

// svg is the canvas the tests draw on: it collects what is drawn as the
// elements of an SVG document, which can then be looked at as text.
type svg struct {
	w, h float64
	b    strings.Builder
}

func newSVG(w, h float64) *svg { return &svg{w: w, h: h} }

func (c rgb) css() string { return fmt.Sprintf("rgb(%d,%d,%d)", c.r, c.g, c.b) }

func f(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

func (s *svg) Rect(x, y, w, h float64, fill, stroke *rgb, lw float64) {
	fl, st := "none", "none"
	if fill != nil {
		fl = fill.css()
	}
	if stroke != nil {
		st = stroke.css()
	}
	fmt.Fprintf(&s.b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s" stroke="%s" stroke-width="%s"/>`, f(x), f(y), f(w), f(h), fl, st, f(lw))
}

func (s *svg) Line(x1, y1, x2, y2 float64, c rgb, lw float64, dashed bool) {
	dash := ""
	if dashed {
		dash = ` stroke-dasharray="3 2"`
	}
	fmt.Fprintf(&s.b, `<line x1="%s" y1="%s" x2="%s" y2="%s" stroke="%s" stroke-width="%s"%s/>`, f(x1), f(y1), f(x2), f(y2), c.css(), f(lw), dash)
}

func points(pts [][2]float64) string {
	var b strings.Builder
	for i, p := range pts {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(f(p[0]) + "," + f(p[1]))
	}
	return b.String()
}

func (s *svg) Polyline(pts [][2]float64, c rgb, lw float64) {
	if len(pts) == 0 {
		return
	}
	fmt.Fprintf(&s.b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="%s" stroke-linejoin="round"/>`, points(pts), c.css(), f(lw))
}

func (s *svg) Polygon(pts [][2]float64, fill rgb) {
	fmt.Fprintf(&s.b, `<polygon points="%s" fill="%s"/>`, points(pts), fill.css())
}

var xmlEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func (s *svg) Text(x, y float64, text string, size float64, c rgb, anchor int, bold bool) {
	a := [...]string{"start", "middle", "end"}[anchor]
	weight := ""
	if bold {
		weight = ` font-weight="bold"`
	}
	fmt.Fprintf(&s.b, `<text x="%s" y="%s" font-size="%s" fill="%s" text-anchor="%s"%s>%s</text>`, f(x), f(y), f(size), c.css(), a, weight, xmlEscape.Replace(text))
}

// Bytes is the finished document.
func (s *svg) Bytes() []byte {
	return []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" viewBox="0 0 %s %s" font-family="Helvetica, Arial, sans-serif"><rect width="100%%" height="100%%" fill="white"/>%s</svg>`,
		f(s.w), f(s.h), f(s.w), f(s.h), s.b.String()))
}
