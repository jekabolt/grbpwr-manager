package entity

import "math"

const (
	fusingSplineChordErrorMm = 0.5
	fusingSplineMaxSteps     = 64
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

// flattenPieceFusingSpline copies the editor's rule exactly: a non-corner tangent is half the span
// between neighbouring points, while a corner tangent is zero. Each Hermite segment emits t=0
// through t=(steps-1)/steps at equal intervals, so control points appear once and closure is implicit.
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
	tangent := func(i int) fusingSplineFloatPoint {
		if spline.Points[i].Corner {
			return fusingSplineFloatPoint{}
		}
		prev := points[(i-1+len(points))%len(points)]
		next := points[(i+1)%len(points)]
		return fusingSplineFloatPoint{x: (next.x - prev.x) / 2, y: (next.y - prev.y) / 2}
	}
	out := make([]fusingSplineFloatPoint, 0, len(points)*4)
	for i, p1 := range points {
		p2 := points[(i+1)%len(points)]
		m0, m1 := tangent(i), tangent((i+1)%len(points))
		steps := fusingSplineSteps(p1, m0, p2, m1, fusingSplineChordErrorMm)
		for k := 0; k < steps; k++ {
			t := float64(k) / float64(steps)
			t2 := t * t
			t3 := t2 * t
			h00 := 2*t3 - 3*t2 + 1
			h10 := t3 - 2*t2 + t
			h01 := -2*t3 + 3*t2
			h11 := t3 - t2
			out = append(out, fusingSplineFloatPoint{
				x: h00*p1.x + h10*m0.x + h01*p2.x + h11*m1.x,
				y: h00*p1.y + h10*m0.y + h01*p2.y + h11*m1.y,
			})
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
	area2 := 0.0
	for i, point := range points {
		next := points[(i+1)%len(points)]
		area2 += point.x*next.y - next.x*point.y
	}
	return false, math.Abs(area2) > epsilon
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
