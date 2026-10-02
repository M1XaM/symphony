package report

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
)

// Page geometry, in points (A4, portrait).
const (
	margin   = 40.0
	pageW    = 595.28
	pageH    = 841.89
	contentW = pageW - 2*margin
	bottom   = pageH - 46
)

// doc is a PDF being written: fpdf plus the few layout helpers the report
// needs. Text goes through tr, which maps it to the built-in fonts'
// encoding (Latin; a character outside it prints as a dot).
type doc struct {
	*fpdf.Fpdf
	tr func(string) string
}

// PDF renders the report as a PDF document, charts and topology drawn as
// vector graphics.
func (r Report) PDF() ([]byte, error) {
	pdf := fpdf.New("P", "pt", "A4", "")
	d := &doc{Fpdf: pdf, tr: pdf.UnicodeTranslatorFromDescriptor("")}
	pdf.SetMargins(margin, margin, margin)
	pdf.SetAutoPageBreak(true, pageH-bottom)
	pdf.SetTitle(d.tr(fmt.Sprintf("%s on %s", r.ScenarioName, r.Template)), false)
	pdf.SetCreator("Symphony", false)
	pdf.AliasNbPages("")
	pdf.SetFooterFunc(func() {
		pdf.SetY(pageH - 30)
		pdf.SetFont("Helvetica", "", 7.5)
		pdf.SetTextColor(muted.r, muted.g, muted.b)
		pdf.CellFormat(contentW/2, 10, d.tr("Symphony · run "+r.RunID), "", 0, "L", false, 0, "")
		pdf.CellFormat(contentW/2, 10, fmt.Sprintf("%d / {nb}", pdf.PageNo()), "", 0, "R", false, 0, "")
	})
	pdf.AddPage()

	// title and verdict
	d.font("B", 17, ink)
	pdf.MultiCell(contentW, 21, d.tr(fmt.Sprintf("%s on \"%s\"", r.ScenarioName, r.Template)), "", "L", false)
	col := rgb{46, 160, 96}
	switch r.Verdict {
	case "FAILED":
		col = marker
	case "ABORTED":
		col = muted
	}
	pdf.SetFillColor(col.r, col.g, col.b)
	d.font("B", 10, white)
	pdf.CellFormat(pdf.GetStringWidth(r.Verdict)+16, 17, r.Verdict, "", 1, "C", true, 0, "")
	pdf.Ln(6)
	facts := r.facts()
	d.pairs(facts[:len(facts)-1], 70)
	if r.Error != "" {
		d.font("B", 9, marker)
		pdf.MultiCell(contentW, 12, d.tr("Error: "+r.Error), "", "L", false)
	}

	if len(r.Assertions) > 0 {
		d.heading("Assertions")
		rows := make([][]string, len(r.Assertions))
		for i, a := range r.Assertions {
			mark := "ok"
			if !a.Passed {
				mark = "FAIL"
			}
			rows[i] = []string{mark, a.Metric, a.Expected, dash(a.Phase), a.Actual, a.Note}
		}
		d.table([]string{"", "metric", "expected", "phase", "actual", "note"}, rows, []float64{0.07, 0.24, 0.12, 0.11, 0.1, 0.36},
			func(row, colIdx int) *rgb {
				if colIdx == 0 && rows[row][0] == "FAIL" {
					return &marker
				}
				return nil
			})
	}

	d.heading("Scenario")
	if r.Description != "" {
		d.para(r.Description)
	}
	if r.Reveals != "" {
		d.para("What it shows: " + r.Reveals)
	}
	if len(r.Params) > 0 {
		parts := make([]string, len(r.Params))
		for i, kv := range r.Params {
			parts[i] = kv.Key + "=" + kv.Value
		}
		d.para("Parameters: " + strings.Join(parts, "   "))
	}

	d.heading("Topology")
	if _, h, _ := topologySize(r.Topology, contentW); h > 0 {
		d.room(h + 6)
		y := pdf.GetY() // drawing moves the cursor: put it back below the picture
		drawTopology(d.canvas(), r.Topology, contentW)
		pdf.SetY(y + h + 8)
	}
	if len(r.Nodes) > 0 {
		rows := make([][]string, len(r.Nodes))
		for i, n := range r.Nodes {
			cpu, mem := "-", "-"
			if n.Measured {
				cpu, mem = num(n.PeakCPU)+"%", num(n.PeakMem)+" MB"
			}
			rows[i] = []string{n.ID, n.Kind, n.Image, fmt.Sprint(n.Replicas), n.Limits, cpu, mem}
		}
		d.table([]string{"node", "kind", "image", "replicas", "limits", "peak CPU", "peak memory"}, rows, []float64{0.16, 0.09, 0.24, 0.08, 0.21, 0.1, 0.12}, nil)
		rows = rows[:0]
		for _, n := range r.Nodes {
			rows = append(rows, []string{n.ID, dash(n.Hot), dash(n.Cold)})
		}
		d.table([]string{"node", "hot config", "cold config"}, rows, []float64{0.16, 0.56, 0.28}, nil)
	}
	if len(r.Edges) > 0 {
		rows := make([][]string, len(r.Edges))
		for i, e := range r.Edges {
			fail, rate := e.peaks()
			rows[i] = []string{e.ID, e.From + " -> " + e.To, e.Kind, dash(e.Topic), orDefault(e.TimeoutMs, " ms"), orDefault(e.Retries, ""), fail, rate}
		}
		d.table([]string{"edge", "link", "kind", "topic", "timeout", "retries", "peak failure", "peak rate"}, rows,
			[]float64{0.15, 0.22, 0.1, 0.16, 0.08, 0.08, 0.11, 0.1}, nil)
	}

	if len(r.KeyNumbers) > 0 {
		d.heading("Results")
		d.pairs(r.KeyNumbers, 130)
	}
	if len(r.PhaseRows) > 0 {
		d.subheading("Per phase (mean)")
		d.table(r.PhaseColumns, r.PhaseRows, firstWide(len(r.PhaseColumns), 0.26), nil)
	}

	if len(r.Charts) > 0 {
		d.heading("Charts")
		d.para("The shaded bands are the phases; a dashed red line is a fault being injected.")
		for _, c := range r.Charts {
			h := chartHeight(c, contentW)
			d.room(h + 22)
			d.subheading(c.Title)
			y := pdf.GetY()
			drawChart(d.canvas(), contentW, c, r.Phases, r.Timeline, r.Duration)
			pdf.SetY(y + h + 6)
		}
	}

	if len(r.Timeline) > 0 {
		d.heading("Timeline")
		rows := make([][]string, len(r.Timeline))
		for i, e := range r.Timeline {
			rows[i] = []string{num(e.T) + "s", e.Text}
		}
		d.table([]string{"at", "what the runner did"}, rows, []float64{0.1, 0.9}, nil)
	}
	if len(r.DataRows) > 0 {
		d.heading("Data")
		d.para("The headline series, averaged over steps of the run.")
		d.table(r.DataColumns, r.DataRows, firstWide(len(r.DataColumns), 0.1), nil)
	}
	if len(r.Summary) > 0 {
		d.heading("Every summary value")
		rows := make([][]string, len(r.Summary))
		for i, kv := range r.Summary {
			rows[i] = []string{kv.Key, kv.Value}
		}
		d.table([]string{"key", "value"}, rows, []float64{0.6, 0.4}, nil)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("report: pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// firstWide shares the width among n columns, the first taking `first` of it.
func firstWide(n int, first float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = (1 - first) / float64(max(n-1, 1))
	}
	out[0] = first
	return out
}

func (d *doc) font(style string, size float64, c rgb) {
	d.SetFont("Helvetica", style, size)
	d.SetTextColor(c.r, c.g, c.b)
}

// room starts a new page unless h more points fit on this one.
func (d *doc) room(h float64) {
	if d.GetY()+h > bottom {
		d.AddPage()
	}
}

func (d *doc) heading(s string) {
	d.room(60)
	d.Ln(10)
	d.font("B", 13, ink)
	d.CellFormat(contentW, 18, d.tr(s), "", 1, "L", false, 0, "")
	d.SetDrawColor(grid.r, grid.g, grid.b)
	d.SetLineWidth(0.8)
	d.Line(margin, d.GetY(), margin+contentW, d.GetY())
	d.Ln(6)
}

func (d *doc) subheading(s string) {
	d.room(30)
	d.font("B", 10, ink)
	d.CellFormat(contentW, 15, d.tr(s), "", 1, "L", false, 0, "")
}

func (d *doc) para(s string) {
	d.font("", 9, ink)
	d.MultiCell(contentW, 12, d.tr(s), "", "L", false)
	d.Ln(3)
}

// pairs prints label/value lines, the labels in a column keyW wide.
func (d *doc) pairs(kvs []KV, keyW float64) {
	for _, kv := range kvs {
		d.room(13)
		d.font("", 9, muted)
		d.CellFormat(keyW, 13, d.tr(kv.Key), "", 0, "L", false, 0, "")
		d.font("", 9, ink)
		d.CellFormat(contentW-keyW, 13, d.tr(strings.ReplaceAll(kv.Value, "`", "")), "", 1, "L", false, 0, "")
	}
	d.Ln(3)
}

// table prints rows under a header that is repeated on every page the table
// runs onto. widths are shares of the content width; a cell's text wraps
// within its column. colour may give a cell its own text colour.
func (d *doc) table(cols []string, rows [][]string, widths []float64, colour func(row, col int) *rgb) {
	const lineH, pad = 10.0, 3.0
	w := make([]float64, len(cols))
	for i := range cols {
		w[i] = widths[i] * contentW
	}
	header := func() {
		d.font("B", 7.5, muted)
		d.SetFillColor(band.r, band.g, band.b)
		for i, c := range cols {
			d.CellFormat(w[i], 14, d.tr(c), "", 0, "L", true, 0, "")
		}
		d.Ln(14)
	}
	d.room(40)
	header()
	d.SetDrawColor(grid.r, grid.g, grid.b)
	d.SetLineWidth(0.4)
	for ri, row := range rows {
		d.font("", 7.5, ink)
		lines := make([][]string, len(cols))
		tall := 1
		for i := range cols {
			text := ""
			if i < len(row) {
				text = d.tr(row[i])
			}
			lines[i] = d.SplitText(text, w[i]-2*pad)
			tall = max(tall, len(lines[i]))
		}
		h := float64(tall)*lineH + 4
		if d.GetY()+h > bottom {
			d.AddPage()
			header()
		}
		x, y := margin, d.GetY()
		for i := range cols {
			c := ink
			if colour != nil {
				if own := colour(ri, i); own != nil {
					c = *own
				}
			}
			d.font("", 7.5, c)
			for li, line := range lines[i] {
				d.Text(x+pad, y+9+float64(li)*lineH, line)
			}
			x += w[i]
		}
		d.Line(margin, y+h, margin+contentW, y+h)
		d.SetY(y + h)
	}
	d.Ln(8)
}

// canvas draws at the current position, across the content width.
func (d *doc) canvas() canvas { return &pdfCanvas{d: d, ox: margin, oy: d.GetY()} }

// pdfCanvas is the PDF side of the canvas: a region of the page.
type pdfCanvas struct {
	d      *doc
	ox, oy float64
}

func (p *pdfCanvas) Rect(x, y, w, h float64, fill, stroke *rgb, lw float64) {
	style := ""
	if fill != nil {
		p.d.SetFillColor(fill.r, fill.g, fill.b)
		style += "F"
	}
	if stroke != nil {
		p.d.SetDrawColor(stroke.r, stroke.g, stroke.b)
		p.d.SetLineWidth(lw)
		style += "D"
	}
	if style != "" {
		p.d.Rect(p.ox+x, p.oy+y, w, h, style)
	}
}

func (p *pdfCanvas) Line(x1, y1, x2, y2 float64, c rgb, lw float64, dashed bool) {
	p.d.SetDrawColor(c.r, c.g, c.b)
	p.d.SetLineWidth(lw)
	if dashed {
		p.d.SetDashPattern([]float64{3, 2}, 0)
	}
	p.d.Line(p.ox+x1, p.oy+y1, p.ox+x2, p.oy+y2)
	if dashed {
		p.d.SetDashPattern([]float64{}, 0)
	}
}

func (p *pdfCanvas) Polyline(pts [][2]float64, c rgb, lw float64) {
	if len(pts) < 2 {
		return
	}
	p.d.SetDrawColor(c.r, c.g, c.b)
	p.d.SetLineWidth(lw)
	p.d.SetLineJoinStyle("round")
	p.d.MoveTo(p.ox+pts[0][0], p.oy+pts[0][1])
	for _, pt := range pts[1:] {
		p.d.LineTo(p.ox+pt[0], p.oy+pt[1])
	}
	p.d.DrawPath("D")
}

func (p *pdfCanvas) Polygon(pts [][2]float64, fill rgb) {
	out := make([]fpdf.PointType, len(pts))
	for i, pt := range pts {
		out[i] = fpdf.PointType{X: p.ox + pt[0], Y: p.oy + pt[1]}
	}
	p.d.SetFillColor(fill.r, fill.g, fill.b)
	p.d.Polygon(out, "F")
}

func (p *pdfCanvas) Text(x, y float64, s string, size float64, c rgb, anchor int, bold bool) {
	style := ""
	if bold {
		style = "B"
	}
	p.d.font(style, size, c)
	s = p.d.tr(s)
	switch w := p.d.GetStringWidth(s); anchor {
	case middle:
		x -= w / 2
	case right:
		x -= w
	}
	p.d.Text(p.ox+x, p.oy+y, s)
}
