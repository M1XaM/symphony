package report

import (
	"fmt"
	"math"

	"symphony/engine/spec"
)

// rgb is a colour.
type rgb struct{ r, g, b int }

// Anchors of a text relative to its x.
const (
	left = iota
	middle
	right
)

// canvas is what the charts and the topology are drawn on: a region of a
// page of the PDF (the tests draw on one that records what it is given).
// Coordinates are points from the top left corner; a text's y is its
// baseline.
type canvas interface {
	Rect(x, y, w, h float64, fill, stroke *rgb, lw float64)
	Line(x1, y1, x2, y2 float64, c rgb, lw float64, dashed bool)
	Polyline(pts [][2]float64, c rgb, lw float64)
	Polygon(pts [][2]float64, fill rgb)
	Text(x, y float64, s string, size float64, c rgb, anchor int, bold bool)
}

var (
	ink    = rgb{34, 38, 46}
	muted  = rgb{110, 116, 128}
	grid   = rgb{226, 229, 235}
	band   = rgb{245, 247, 250}
	frame  = rgb{190, 195, 204}
	marker = rgb{214, 69, 65}
	white  = rgb{255, 255, 255}
	// one colour per series, in order
	palette = []rgb{{38, 110, 201}, {214, 69, 65}, {46, 160, 96}, {201, 138, 20}, {128, 82, 180}, {22, 150, 170}, {190, 80, 140}, {110, 116, 128}}
	// node kinds, as on the map
	kindColor = map[spec.NodeKind]rgb{
		spec.KindService: {58, 90, 153}, spec.KindBroker: {122, 82, 153}, spec.KindDB: {62, 122, 98},
		spec.KindProxy: {140, 98, 48}, spec.KindInfra: {85, 90, 100},
	}
	kindLabel = map[spec.NodeKind]string{
		spec.KindService: "SERVICE", spec.KindBroker: "BROKER", spec.KindDB: "DATABASE", spec.KindProxy: "PROXY", spec.KindInfra: "GATEWAY",
	}
)

// niceStep is a round step (1, 2 or 5 times a power of ten) that divides a
// range into about n parts.
func niceStep(span float64, n int) float64 {
	if span <= 0 {
		return 1
	}
	raw := span / float64(n)
	pow := math.Pow(10, math.Floor(math.Log10(raw)))
	switch f := raw / pow; {
	case f <= 1:
		return pow
	case f <= 2:
		return 2 * pow
	case f <= 5:
		return 5 * pow
	}
	return 10 * pow
}

// legendWidth estimates how wide a legend entry is.
func legendWidth(name string) float64 { return 20 + float64(len(name))*4.3 }

// chartHeight is how tall a chart of this width is drawn, legend included.
func chartHeight(c Chart, w float64) float64 {
	return 150 + legendRows(c, w)*11
}

func legendRows(c Chart, w float64) float64 {
	rows, x := 1.0, 0.0
	for _, s := range c.Series {
		lw := legendWidth(s.Name)
		if x > 0 && x+lw > w-56 {
			rows++
			x = 0
		}
		x += lw
	}
	return rows
}

// drawChart draws a chart w wide over [0, duration] seconds: the phases as
// bands, the faults as dashed lines, one line per series.
func drawChart(cv canvas, w float64, c Chart, phases []Phase, events []Event, duration float64) {
	h := chartHeight(c, w)
	top := 8 + legendRows(c, w)*11 + 10
	const lm, rm, bm = 46.0, 10.0, 22.0
	pw, ph := w-lm-rm, h-top-bm
	if duration <= 0 {
		duration = 1
	}
	hi := 0.0
	for _, s := range c.Series {
		hi = math.Max(hi, peak(s.Points))
	}
	ystep := niceStep(hi, 4)
	if hi <= 0 {
		ystep = 1
	}
	ymax := math.Max(ystep, math.Ceil(hi/ystep)*ystep)
	X := func(t float64) float64 { return lm + math.Min(math.Max(t, 0), duration)/duration*pw }
	Y := func(v float64) float64 { return top + ph - math.Min(math.Max(v, 0), ymax)/ymax*ph }

	// legend
	lx, ly := lm, 10.0
	for i, s := range c.Series {
		lw := legendWidth(s.Name)
		if lx > lm && lx+lw > w-rm {
			lx, ly = lm, ly+11
		}
		col := palette[i%len(palette)]
		cv.Line(lx, ly-2.5, lx+12, ly-2.5, col, 1.6, false)
		cv.Text(lx+15, ly, s.Name, 7, ink, left, false)
		lx += lw
	}
	cv.Text(w-rm, 10, c.Unit, 7, muted, right, false)

	// phases: every other one shaded, each named at the top
	for i, p := range phases {
		x1, x2 := X(p.Start), X(p.End)
		if x2 <= x1 {
			continue
		}
		if i%2 == 1 {
			cv.Rect(x1, top, x2-x1, ph, &band, nil, 0)
		}
		if x2-x1 > float64(len(p.Name))*3.6+4 {
			cv.Text((x1+x2)/2, top+8, p.Name, 6.5, muted, middle, false)
		}
	}
	// grid and axes
	for v := 0.0; v <= ymax+ystep/2; v += ystep {
		cv.Line(lm, Y(v), lm+pw, Y(v), grid, 0.5, false)
		cv.Text(lm-4, Y(v)+2.5, num(v), 7, muted, right, false)
	}
	xstep := niceStep(duration, 8)
	for t := 0.0; t <= duration+xstep/1000; t += xstep {
		cv.Line(X(t), top+ph, X(t), top+ph+3, frame, 0.5, false)
		cv.Text(X(t), top+ph+12, num(t)+"s", 7, muted, middle, false)
	}
	cv.Rect(lm, top, pw, ph, nil, &frame, 0.6)
	for _, e := range events {
		if e.Fault {
			cv.Line(X(e.T), top, X(e.T), top+ph, marker, 0.6, true)
		}
	}
	// A series is drawn as one line per stretch of samples: where it has
	// none for a while (no saga finished, so there is no latency) the line
	// stops, rather than ramp across the gap to the next value.
	gap := math.Max(2.5, duration/40)
	for i, s := range c.Series {
		var pts [][2]float64
		flush := func() {
			if len(pts) == 1 {
				pts = append(pts, [2]float64{pts[0][0] + 1, pts[0][1]})
			}
			cv.Polyline(pts, palette[i%len(palette)], 1.1)
			pts = nil
		}
		for j, p := range s.Points {
			if j > 0 && p.T-s.Points[j-1].T > gap {
				flush()
			}
			pts = append(pts, [2]float64{X(p.T), Y(p.V)})
		}
		flush()
	}
}

// The size of a node on the map, in the map's own units, and how many
// points one of those units is drawn as when the topology fits the width.
const (
	nodeW, nodeH = 170.0, 58.0
	mapScale     = 0.62
)

// positions returns where each node sits. A topology that was never laid
// out on the map (every node at the origin) is laid out in columns by kind.
func positions(t spec.Topology) map[string][2]float64 {
	out := map[string][2]float64{}
	laid := false
	for _, n := range t.Nodes {
		out[n.ID] = [2]float64{n.Position.X, n.Position.Y}
		if n.Position != t.Nodes[0].Position {
			laid = true
		}
	}
	if laid || len(t.Nodes) < 2 {
		return out
	}
	column := map[spec.NodeKind]float64{spec.KindInfra: 0, spec.KindService: 1, spec.KindProxy: 2, spec.KindBroker: 2, spec.KindDB: 3}
	rowsIn := map[float64]float64{}
	for _, n := range t.Nodes {
		c := column[n.Kind]
		out[n.ID] = [2]float64{c * (nodeW + 70), rowsIn[c] * (nodeH + 40)}
		rowsIn[c]++
	}
	return out
}

// topologySize is the size the topology is drawn at when given maxW of
// width: scaled down to fit, never up.
func topologySize(t spec.Topology, maxW float64) (w, h, scale float64) {
	if len(t.Nodes) == 0 {
		return maxW, 0, 1
	}
	minX, minY, maxX, maxY := bounds(positions(t))
	bw, bh := maxX-minX+nodeW, maxY-minY+nodeH
	scale = mapScale
	if bw*scale > maxW {
		scale = maxW / bw
	}
	return maxW, bh*scale + 8, scale
}

func bounds(pos map[string][2]float64) (minX, minY, maxX, maxY float64) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, p := range pos {
		minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
		minY, maxY = math.Min(minY, p[1]), math.Max(maxY, p[1])
	}
	return
}

// drawTopology draws the template as it is on the map: every node where it
// sits, coloured by kind, and every edge as an arrow with its topic.
func drawTopology(cv canvas, t spec.Topology, maxW float64) {
	if len(t.Nodes) == 0 {
		return
	}
	pos := positions(t)
	w, _, k := topologySize(t, maxW)
	minX, minY, maxX, _ := bounds(pos)
	ox := (w-(maxX-minX+nodeW)*k)/2 - minX*k
	oy := 4 - minY*k
	box := func(id string) (x, y, bw, bh float64) {
		p := pos[id]
		return ox + p[0]*k, oy + p[1]*k, nodeW * k, nodeH * k
	}
	// edges first, so the nodes sit on top of them
	for _, e := range t.Edges {
		if _, ok := pos[e.From]; !ok {
			continue
		}
		if _, ok := pos[e.To]; !ok {
			continue
		}
		fx, fy, bw, bh := box(e.From)
		tx, ty, _, _ := box(e.To)
		x1, y1 := fx+bw/2, fy+bh/2
		x2, y2 := tx+bw/2, ty+bh/2
		sx, sy := clip(x1, y1, x2, y2, bw, bh)
		ex, ey := clip(x2, y2, x1, y1, bw, bh)
		// Shifted to the left of its own direction, so that the two edges
		// of a pair that goes both ways (a service and its broker) lie side
		// by side; each is labelled nearer its start, for the same reason.
		if d := math.Hypot(ex-sx, ey-sy); d > 0 {
			nx, ny := (ey-sy)/d*3, -(ex-sx)/d*3
			sx, sy, ex, ey = sx+nx, sy+ny, ex+nx, ey+ny
		}
		cv.Line(sx, sy, ex, ey, muted, 0.8, e.Kind.Messaging())
		arrow(cv, sx, sy, ex, ey, 5*math.Max(k/mapScale, 0.7))
		label := e.Topic
		if label == "" {
			label = string(e.Kind)
		}
		lx, ly := sx+(ex-sx)*0.38, sy+(ey-sy)*0.38
		size := math.Max(5, 9*k)
		tw := float64(len(label))*size*0.5 + 4
		// an edge too short for its label goes without: the table of edges
		// under the picture names every topic
		if math.Hypot(ex-sx, ey-sy) < tw+12 {
			continue
		}
		cv.Rect(lx-tw/2, ly-size*0.8, tw, size*1.15, &white, nil, 0)
		cv.Text(lx, ly, label, size, muted, middle, false)
	}
	for _, n := range t.Nodes {
		x, y, bw, bh := box(n.ID)
		col := kindColor[n.Kind]
		head := bh * 0.34
		cv.Rect(x, y, bw, bh, &white, &col, 0.8)
		cv.Rect(x, y, bw, head, &col, nil, 0)
		cv.Text(x+5*k, y+head*0.72, kindLabel[n.Kind], math.Max(4.5, 9*k), white, left, true)
		cv.Text(x+5*k, y+head+bh*0.3, n.ID, math.Max(5.5, 13*k), ink, left, true)
		sub := fmt.Sprintf("%d replica", n.Replicas)
		if n.Replicas != 1 {
			sub += "s"
		}
		if n.Resources.CPUs > 0 {
			sub += " · " + num(n.Resources.CPUs) + " cpu"
		}
		if n.Resources.MemoryMB > 0 {
			sub += fmt.Sprintf(" · %d MB", n.Resources.MemoryMB)
		}
		cv.Text(x+5*k, y+head+bh*0.55, sub, math.Max(4.5, 9.5*k), muted, left, false)
	}
}

// clip moves the start of the segment (x1,y1)-(x2,y2) from the centre of a
// bw x bh box to the box's border.
func clip(x1, y1, x2, y2, bw, bh float64) (float64, float64) {
	dx, dy := x2-x1, y2-y1
	if dx == 0 && dy == 0 {
		return x1, y1
	}
	t := math.Inf(1)
	if dx != 0 {
		t = math.Min(t, (bw/2)/math.Abs(dx))
	}
	if dy != 0 {
		t = math.Min(t, (bh/2)/math.Abs(dy))
	}
	return x1 + dx*t, y1 + dy*t
}

func arrow(cv canvas, x1, y1, x2, y2, size float64) {
	a := math.Atan2(y2-y1, x2-x1)
	p := func(da float64) [2]float64 {
		return [2]float64{x2 - size*math.Cos(a+da), y2 - size*math.Sin(a+da)}
	}
	cv.Polygon([][2]float64{{x2, y2}, p(0.42), p(-0.42)}, muted)
}
