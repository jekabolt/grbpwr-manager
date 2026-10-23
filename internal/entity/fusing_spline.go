package entity

import (
	"fmt"
	"math"
)

const (
	fusingSplineChordErrorMm = 0.5
	fusingSplineMaxSteps     = 64
	// TechCardPieceFusingZoneMaxHoles caps the complexity of one generated fusible shape.
	TechCardPieceFusingZoneMaxHoles = 16
)

type fusingSplineFloatPoint struct {
	x float64
	y float64
}

func finiteFusingSplinePoint(p fusingSplineFloatPoint) bool {
	return !math.IsNaN(p.x) && !math.IsInf(p.x, 0) && !math.IsNaN(p.y) && !math.IsInf(p.y, 0)
}

// fusingSplineSteps mirrors the editor's stepsOf function. The segment's Hermite
// tangents are converted to Bezier control points only to calculate the bound.
func fusingSplineSteps(p1, m1, p2, m2 fusingSplineFloatPoint, toleranceMm float64) int {
	b1 := fusingSplineFloatPoint{x: p1.x + m1.x/3, y: p1.y + m1.y/3}
	b2 := fusingSplineFloatPoint{x: p2.x - m2.x/3, y: p2.y - m2.y/3}
	d1 := math.Hypot(p1.x-2*b1.x+b2.x, p1.y-2*b1.y+b2.y)
	d2 := math.Hypot(b1.x-2*b2.x+p2.x, b1.y-2*b2.y+p2.y)
	curvature := 6 * math.Max(d1, d2)
	steps := math.Ceil(math.Sqrt(curvature / (8 * toleranceMm)))
	if math.IsNaN(steps) || math.IsInf(steps, 0) {
		return 1
	}
	if steps < 1 {
		return 1
	}
	if steps > fusingSplineMaxSteps {
		return fusingSplineMaxSteps
	}
	return int(steps)
}

type fusingSplineGeometryError string

func (e fusingSplineGeometryError) Error() string { return string(e) }

const (
	errFusingSplineTooLarge = fusingSplineGeometryError("coordinates are too large")
)

// fusingSplineCentripetalTangents returns the [0,1]-scaled tangents for segment i. Distances and
// their alpha=0.5 intervals are deliberately evaluated before the zero-length check: the editor
// follows the same operation order so its flattened area and DXF vertices match bit for bit.
func fusingSplineCentripetalTangents(
	points []fusingSplineFloatPoint,
	controls []TechCardPieceFusingSplinePoint,
	i int,
) (m1, m2 fusingSplineFloatPoint, zeroLength bool) {
	n := len(points)
	p0 := points[(i-1+n)%n]
	p1 := points[i]
	p2 := points[(i+1)%n]
	p3 := points[(i+2)%n]
	d01 := math.Hypot(p1.x-p0.x, p1.y-p0.y)
	d12 := math.Hypot(p2.x-p1.x, p2.y-p1.y)
	d23 := math.Hypot(p3.x-p2.x, p3.y-p2.y)
	t01 := math.Sqrt(d01)
	t12 := math.Sqrt(d12)
	t23 := math.Sqrt(d23)
	if d12 == 0 {
		return fusingSplineFloatPoint{}, fusingSplineFloatPoint{}, true
	}
	if !controls[i].Corner {
		if d01 == 0 {
			m1 = fusingSplineFloatPoint{x: p2.x - p1.x, y: p2.y - p1.y}
		} else {
			m1 = fusingSplineFloatPoint{
				x: t12 * ((p1.x-p0.x)/t01 - (p2.x-p0.x)/(t01+t12) + (p2.x-p1.x)/t12),
				y: t12 * ((p1.y-p0.y)/t01 - (p2.y-p0.y)/(t01+t12) + (p2.y-p1.y)/t12),
			}
		}
	}
	if !controls[(i+1)%n].Corner {
		if d23 == 0 {
			m2 = fusingSplineFloatPoint{x: p2.x - p1.x, y: p2.y - p1.y}
		} else {
			m2 = fusingSplineFloatPoint{
				x: t12 * ((p2.x-p1.x)/t12 - (p3.x-p1.x)/(t12+t23) + (p3.x-p2.x)/t23),
				y: t12 * ((p2.y-p1.y)/t12 - (p3.y-p1.y)/(t12+t23) + (p3.y-p2.y)/t23),
			}
		}
	}
	return m1, m2, false
}

func fusingSplineHermitePoint(p1, m1, p2, m2 fusingSplineFloatPoint, t float64) fusingSplineFloatPoint {
	t2 := t * t
	t3 := t2 * t
	h00 := 2*t3 - 3*t2 + 1
	h10 := t3 - 2*t2 + t
	h01 := -2*t3 + 3*t2
	h11 := t3 - t2
	return fusingSplineFloatPoint{
		x: h00*p1.x + h10*m1.x + h01*p2.x + h11*m2.x,
		y: h00*p1.y + h10*m1.y + h01*p2.y + h11*m2.y,
	}
}

// flattenPieceFusingSpline copies the editor's centripetal Catmull-Rom rule exactly. Each Hermite
// segment emits t=0 through t=(steps-1)/steps at equal intervals, so control points appear once and
// closure is implicit. A zero-length segment emits only its start point.
func flattenPieceFusingSpline(spline *TechCardPieceFusingSpline) ([]fusingSplineFloatPoint, error) {
	points := make([]fusingSplineFloatPoint, len(spline.Points))
	for i, point := range spline.Points {
		points[i] = fusingSplineFloatPoint{x: point.X.InexactFloat64(), y: point.Y.InexactFloat64()}
		if !finiteFusingSplinePoint(points[i]) {
			return nil, errFusingSplineTooLarge
		}
	}
	if len(points) < 3 {
		return points, nil
	}
	out := make([]fusingSplineFloatPoint, 0, len(points)*4)
	for i, p1 := range points {
		p2 := points[(i+1)%len(points)]
		m1, m2, zeroLength := fusingSplineCentripetalTangents(points, spline.Points, i)
		if zeroLength {
			out = append(out, p1)
			continue
		}
		steps := fusingSplineSteps(p1, m1, p2, m2, fusingSplineChordErrorMm)
		for k := 0; k < steps; k++ {
			t := float64(k) / float64(steps)
			out = append(out, fusingSplineHermitePoint(p1, m1, p2, m2, t))
		}
	}
	return out, nil
}

func normalizeFusingPolygon(points []fusingSplineFloatPoint) ([]fusingSplineFloatPoint, float64) {
	minX, maxX, minY, maxY := points[0].x, points[0].x, points[0].y, points[0].y
	for _, point := range points[1:] {
		minX, maxX = math.Min(minX, point.x), math.Max(maxX, point.x)
		minY, maxY = math.Min(minY, point.y), math.Max(maxY, point.y)
	}
	scale := math.Max(maxX-minX, maxY-minY)
	if scale == 0 || math.IsInf(scale, 0) {
		return nil, scale
	}
	out := make([]fusingSplineFloatPoint, len(points))
	for i, point := range points {
		out[i] = fusingSplineFloatPoint{x: (point.x - minX) / scale, y: (point.y - minY) / scale}
	}
	return out, scale
}

func fusingOrientation(a, b, c fusingSplineFloatPoint) float64 {
	return (b.x-a.x)*(c.y-a.y) - (b.y-a.y)*(c.x-a.x)
}

func fusingPointOnSegment(a, b, p fusingSplineFloatPoint) bool {
	const epsilon = 1e-12
	return math.Abs(fusingOrientation(a, b, p)) <= epsilon &&
		p.x >= math.Min(a.x, b.x)-epsilon && p.x <= math.Max(a.x, b.x)+epsilon &&
		p.y >= math.Min(a.y, b.y)-epsilon && p.y <= math.Max(a.y, b.y)+epsilon
}

func fusingSegmentsIntersect(a, b, c, d fusingSplineFloatPoint) bool {
	const epsilon = 1e-12
	o1, o2 := fusingOrientation(a, b, c), fusingOrientation(a, b, d)
	o3, o4 := fusingOrientation(c, d, a), fusingOrientation(c, d, b)
	if ((o1 > epsilon && o2 < -epsilon) || (o1 < -epsilon && o2 > epsilon)) &&
		((o3 > epsilon && o4 < -epsilon) || (o3 < -epsilon && o4 > epsilon)) {
		return true
	}
	return (math.Abs(o1) <= epsilon && fusingPointOnSegment(a, b, c)) ||
		(math.Abs(o2) <= epsilon && fusingPointOnSegment(a, b, d)) ||
		(math.Abs(o3) <= epsilon && fusingPointOnSegment(c, d, a)) ||
		(math.Abs(o4) <= epsilon && fusingPointOnSegment(c, d, b))
}

func fusingPolygonSignedArea(points []fusingSplineFloatPoint) float64 {
	area2 := 0.0
	for i, point := range points {
		next := points[(i+1)%len(points)]
		area2 += point.x*next.y - next.x*point.y
	}
	return area2 / 2
}

func validateFlattenedFusingPolygon(points []fusingSplineFloatPoint) (selfIntersects bool, hasArea bool) {
	points, scale := normalizeFusingPolygon(points)
	if len(points) < 3 || scale == 0 || math.IsInf(scale, 0) {
		return false, false
	}
	const epsilon = 1e-12
	for i := range points {
		a, b := points[i], points[(i+1)%len(points)]
		if math.Hypot(b.x-a.x, b.y-a.y) <= epsilon {
			return true, false
		}
		// Adjacent edges may share only their vertex; a reversal overlaps the preceding edge.
		prev := points[(i-1+len(points))%len(points)]
		if math.Abs(fusingOrientation(prev, a, b)) <= epsilon &&
			(prev.x-a.x)*(b.x-a.x)+(prev.y-a.y)*(b.y-a.y) > epsilon {
			return true, false
		}
		for j := i + 1; j < len(points); j++ {
			if j == i+1 || (i == 0 && j == len(points)-1) {
				continue
			}
			if fusingSegmentsIntersect(a, b, points[j], points[(j+1)%len(points)]) {
				return true, false
			}
		}
	}
	return false, math.Abs(fusingPolygonSignedArea(points)) > epsilon
}

func normalizeFusingRingPair(a, b []fusingSplineFloatPoint) ([]fusingSplineFloatPoint, []fusingSplineFloatPoint, bool) {
	if len(a) == 0 || len(b) == 0 {
		return nil, nil, false
	}
	minX, maxX, minY, maxY := a[0].x, a[0].x, a[0].y, a[0].y
	for _, ring := range [][]fusingSplineFloatPoint{a, b} {
		for _, point := range ring {
			minX, maxX = math.Min(minX, point.x), math.Max(maxX, point.x)
			minY, maxY = math.Min(minY, point.y), math.Max(maxY, point.y)
		}
	}
	scale := math.Max(maxX-minX, maxY-minY)
	if scale == 0 || math.IsInf(scale, 0) {
		return nil, nil, false
	}
	normalize := func(ring []fusingSplineFloatPoint) []fusingSplineFloatPoint {
		out := make([]fusingSplineFloatPoint, len(ring))
		for i, point := range ring {
			out[i] = fusingSplineFloatPoint{x: (point.x - minX) / scale, y: (point.y - minY) / scale}
		}
		return out
	}
	return normalize(a), normalize(b), true
}

func fusingPointStrictlyInsideRing(point fusingSplineFloatPoint, ring []fusingSplineFloatPoint) bool {
	inside := false
	for i, a := range ring {
		b := ring[(i+1)%len(ring)]
		if fusingPointOnSegment(a, b, point) {
			return false
		}
		if (a.y > point.y) != (b.y > point.y) &&
			point.x < (b.x-a.x)*(point.y-a.y)/(b.y-a.y)+a.x {
			inside = !inside
		}
	}
	return inside
}

func fusingRingStrictlyInside(inner, outer []fusingSplineFloatPoint) bool {
	inner, outer, ok := normalizeFusingRingPair(inner, outer)
	if !ok {
		return false
	}
	for i := range inner {
		for j := range outer {
			if fusingSegmentsIntersect(inner[i], inner[(i+1)%len(inner)], outer[j], outer[(j+1)%len(outer)]) {
				return false
			}
		}
	}
	for _, point := range inner {
		if !fusingPointStrictlyInsideRing(point, outer) {
			return false
		}
	}
	return true
}

func fusingRingsOverlap(a, b []fusingSplineFloatPoint) bool {
	a, b, ok := normalizeFusingRingPair(a, b)
	if !ok {
		return true
	}
	for i := range a {
		for j := range b {
			if fusingSegmentsIntersect(a[i], a[(i+1)%len(a)], b[j], b[(j+1)%len(b)]) {
				return true
			}
		}
	}
	return fusingPointStrictlyInsideRing(a[0], b) || fusingPointStrictlyInsideRing(b[0], a)
}

// pieceFusingSplineZoneAreaMm2 is the drawn ring area before clipping/insetting: outer minus every
// cut-out. Consumption deliberately does not use it; generated per-size fusing sheets are measured
// after contour-following, clipping and inset rules have been applied.
func pieceFusingSplineZoneAreaMm2(zone *TechCardPieceFusingZone) (float64, error) {
	outer, err := flattenPieceFusingSpline(zone.Spline)
	if err != nil {
		return 0, err
	}
	area := math.Abs(fusingPolygonSignedArea(outer))
	for i := range zone.Holes {
		hole, err := flattenPieceFusingSpline(&zone.Holes[i])
		if err != nil {
			return 0, err
		}
		area -= math.Abs(fusingPolygonSignedArea(hole))
	}
	if math.IsNaN(area) || math.IsInf(area, 0) {
		return 0, errFusingSplineTooLarge
	}
	return area, nil
}

func validatePieceFusingSpline(field string, spline *TechCardPieceFusingSpline) error {
	if len(spline.Points) < 3 || len(spline.Points) > 64 {
		return NewFieldViolation(field+".points", "a fusing spline needs 3 to 64 points", "",
			"add or remove points until the closed outline has between 3 and 64")
	}
	flat, err := flattenPieceFusingSpline(spline)
	if err != nil {
		return NewFieldViolation(field+".points", "the fusing spline cannot be flattened safely", err.Error(),
			"keep the outline within practical millimetre coordinates and simplify it")
	}
	selfIntersects, hasArea := validateFlattenedFusingPolygon(flat)
	if selfIntersects {
		return NewFieldViolation(field+".points", "the fusing spline crosses itself", "",
			"move the points until the closed outline has no crossings or overlaps")
	}
	if !hasArea {
		return NewFieldViolation(field+".points", "the fusing spline must have an area", "",
			"move the points so the closed outline encloses material")
	}
	return nil
}

func validatePieceFusingSplineZone(field string, zone *TechCardPieceFusingZone) error {
	if err := validatePieceFusingSpline(field+".spline", zone.Spline); err != nil {
		return err
	}
	if len(zone.Holes) > TechCardPieceFusingZoneMaxHoles {
		return NewFieldViolation(field+".holes", "a fusing zone has too many holes",
			fmt.Sprintf("%d", len(zone.Holes)), fmt.Sprintf("keep at most %d cut-outs", TechCardPieceFusingZoneMaxHoles))
	}
	outer, err := flattenPieceFusingSpline(zone.Spline)
	if err != nil {
		return NewFieldViolation(field+".spline.points", "the fusing spline cannot be flattened safely", err.Error(),
			"keep the outline within practical millimetre coordinates and simplify it")
	}
	flatHoles := make([][]fusingSplineFloatPoint, 0, len(zone.Holes))
	for i := range zone.Holes {
		holeField := fmt.Sprintf("%s.holes[%d]", field, i)
		if err := validatePieceFusingSpline(holeField, &zone.Holes[i]); err != nil {
			return err
		}
		hole, err := flattenPieceFusingSpline(&zone.Holes[i])
		if err != nil {
			return NewFieldViolation(holeField+".points", "the fusing hole cannot be flattened safely", err.Error(),
				"keep the cut-out within practical millimetre coordinates and simplify it")
		}
		if !fusingRingStrictlyInside(hole, outer) {
			return NewFieldViolation(holeField+".points", "a fusing hole must stay strictly inside the spline zone", "",
				"move the cut-out away from the outer edge")
		}
		for j, other := range flatHoles {
			if fusingRingsOverlap(hole, other) {
				return NewFieldViolation(holeField+".points", "fusing holes cannot overlap or touch",
					fmt.Sprintf("overlaps holes[%d]", j), "move or merge the cut-outs")
			}
		}
		flatHoles = append(flatHoles, hole)
	}
	area, err := pieceFusingSplineZoneAreaMm2(zone)
	if err != nil || !(area > 0) {
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		return NewFieldViolation(field+".holes", "the holes remove the whole fusing zone", detail,
			"make the cut-outs smaller")
	}
	return nil
}
