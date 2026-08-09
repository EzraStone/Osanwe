package main

import "math"

// The scene is drawn as a function rather than loaded from a photograph, so the
// site's hero image can be regenerated at any size and adjusted in the open
// instead of being a JPEG somebody made once and nobody can change.
//
// It is built for what happens to it afterwards. A character mosaic throws away
// almost everything: at one glyph per cell there is no room for fine detail, and
// an image that looks rich at full resolution turns to noise. What survives is
// large masses separated by clear steps in value. So the scene is composed as
// four of them -- bright sky, mid stone, darker water, near-black verticals --
// and detail exists only where it lands on a boundary between two.

type rgb struct{ r, g, b float64 }

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func mix(a, b rgb, t float64) rgb {
	return rgb{lerp(a.r, b.r, t), lerp(a.g, b.g, t), lerp(a.b, b.b, t)}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func smoothstep(edge0, edge1, x float64) float64 {
	t := clamp((x-edge0)/(edge1-edge0), 0, 1)
	return t * t * (3 - 2*t)
}

// The palette is the interface's own: warm cream, limestone, muted ochre and a
// dusty blue. Anything neon would fight the product it is advertising.
var (
	skyHigh   = rgb{0.26, 0.36, 0.52}
	skyLow    = rgb{0.80, 0.71, 0.56}
	sunCore   = rgb{0.99, 0.90, 0.70}
	cloudLit  = rgb{0.90, 0.83, 0.72}
	cloudDark = rgb{0.44, 0.46, 0.52}
	hillFar   = rgb{0.34, 0.36, 0.42}
	hillNear  = rgb{0.20, 0.21, 0.24}
	seaFar    = rgb{0.40, 0.43, 0.49}
	seaNear   = rgb{0.11, 0.15, 0.21}
	stoneLit  = rgb{0.88, 0.82, 0.70}
	stoneShad = rgb{0.31, 0.28, 0.25}
	cypress   = rgb{0.07, 0.09, 0.08}
	hullDark  = rgb{0.06, 0.06, 0.07}
)

// hash and value noise. A tiny deterministic generator keeps the image
// reproducible: the same seed gives the same picture on any machine.
func hash2(x, y, seed int) float64 {
	h := uint32(x)*374761393 + uint32(y)*668265263 + uint32(seed)*2246822519
	h = (h ^ (h >> 13)) * 1274126177
	return float64((h^(h>>16))&0xffff) / 65535.0
}

func noise(x, y float64, seed int) float64 {
	ix, iy := math.Floor(x), math.Floor(y)
	fx, fy := x-ix, y-iy
	sx := fx * fx * (3 - 2*fx)
	sy := fy * fy * (3 - 2*fy)
	a := hash2(int(ix), int(iy), seed)
	b := hash2(int(ix)+1, int(iy), seed)
	c := hash2(int(ix), int(iy)+1, seed)
	d := hash2(int(ix)+1, int(iy)+1, seed)
	return lerp(lerp(a, b, sx), lerp(c, d, sx), sy)
}

func fbm(x, y float64, octaves int, seed int) float64 {
	sum, amp, freq, norm := 0.0, 1.0, 1.0, 0.0
	for i := 0; i < octaves; i++ {
		sum += amp * noise(x*freq, y*freq, seed+i*17)
		norm += amp
		amp *= 0.5
		freq *= 2.05
	}
	return sum / norm
}

// horizon and sun position, in normalised scene coordinates.
const (
	horizonY = 0.545
	sunX     = 0.665
	sunY     = 0.505
)

// sample returns the colour of the scene at (u,v), both in [0,1), with v
// measured downward from the top.
func sample(u, v float64) rgb {
	if col, ok := cypresses(u, v); ok {
		return col
	}
	if col, ok := ships(u, v); ok {
		return col
	}
	if v < horizonY {
		return sky(u, v)
	}
	return ground(u, v)
}

func sky(u, v float64) rgb {
	t := smoothstep(0.0, horizonY, v)
	c := mix(skyHigh, skyLow, math.Pow(t, 1.35))

	// A low sun, close enough to the horizon that everything is raked rather
	// than lit from above. The glow is wide and weak; a hard disc would read as
	// a bright dot in the mosaic and nothing else.
	d := math.Hypot((u-sunX)*1.0, (v-sunY)*2.2)
	c = mix(c, sunCore, math.Pow(smoothstep(0.22, 0.0, d), 1.5)*0.9)

	// Cloud bands. Stretched hard in x so they read as horizontal masses; round
	// clouds break into speckle once they are characters.
	n := fbm(u*3.1, v*9.0, 4, 11)
	band := smoothstep(0.44, 0.72, n) * smoothstep(0.02, 0.22, v) * smoothstep(horizonY, 0.30, v)
	lit := smoothstep(0.30, 0.0, math.Hypot(u-sunX, (v-sunY)*2.0))
	c = mix(c, mix(cloudDark, cloudLit, 0.35+0.65*lit), band*0.75)

	return c
}

func ground(u, v float64) rgb {
	// Distance from the horizon, 0 at the waterline and 1 at the bottom edge.
	depth := (v - horizonY) / (1 - horizonY)

	c := mix(seaFar, seaNear, math.Pow(depth, 0.62))

	// Sun glitter: a widening path from the horizon to the viewer. This is the
	// one piece of fine detail in the water, and it survives because it sits on
	// the boundary between the two brightest masses.
	width := 0.012 + depth*0.16
	inPath := smoothstep(width, 0.0, math.Abs(u-sunX))
	sparkle := fbm(u*90, v*220, 2, 3)
	glit := inPath * smoothstep(0.46, 0.78, sparkle) * (1 - depth*0.35)
	c = mix(c, sunCore, glit*0.85)

	// Swell. Long, flat, and only where it will not fight the glitter.
	swell := math.Sin((v-horizonY)*140+math.Sin(u*7)*1.4) * 0.5
	c = mix(c, seaNear, clamp(swell, 0, 1)*0.10*depth)

	// Far headland on the left, and a lower one on the right, so the water is
	// enclosed rather than an open edge.
	if hy := horizonY - 0.055*math.Exp(-math.Pow((u-0.10)/0.20, 2)) -
		0.022*math.Exp(-math.Pow((u-0.93)/0.16, 2)); v < horizonY && v > hy {
		return mix(hillFar, hillNear, smoothstep(hy, horizonY, v))
	}

	if col, ok := breakwater(u, v, depth); ok {
		return col
	}
	return c
}

// breakwater is the reason the picture exists. It runs from the near-left edge
// out toward the sun, so the eye travels along it into the distance -- a route,
// not a monument. Everything else in the scene is arranged to keep it legible.
func breakwater(u, v, depth float64) (rgb, bool) {
	// The mole recedes: as v decreases toward the horizon it narrows and rises.
	t := smoothstep(0.56, 1.02, v) // 0 at the far end, 1 at the near edge
	centre := lerp(0.60, -0.14, t*t)
	half := lerp(0.006, 0.30, math.Pow(t, 2.4))
	top := lerp(horizonY+0.004, 0.845, math.Pow(t, 1.5))

	dx := math.Abs(u - centre)
	if dx > half || v < top {
		return rgb{}, false
	}

	// Sunward face lit, near face in shadow, with a hard edge between them.
	face := smoothstep(-0.4, 0.5, (u-centre)/math.Max(half, 1e-6))
	c := mix(stoneShad, stoneLit, 0.30+0.70*face)

	// Block courses. Coarse on purpose: at mosaic resolution anything finer
	// dissolves, and this is the texture that says "cut stone" rather than "wall".
	course := fbm(u*38, v*26, 2, 7)
	c = mix(c, stoneShad, smoothstep(0.55, 0.85, course)*0.28)

	// A dark waterline where the stone meets the sea.
	c = mix(c, seaNear, smoothstep(top+0.055*t, top, v)*0.45)
	return c, true
}

// ships are silhouettes. Two of them, small, moored against the mole. They give
// the harbour a purpose and the composition its darkest value.
func ships(u, v float64) (rgb, bool) {
	type ship struct{ x, y, w, h, mast float64 }
	for _, s := range []ship{
		{0.470, 0.612, 0.040, 0.012, 0.070},
		{0.388, 0.640, 0.055, 0.017, 0.098},
	} {
		// Hull: a shallow lens, wider at the top than the keel.
		dx := (u - s.x) / s.w
		if math.Abs(dx) <= 1 {
			hull := s.y + s.h*(1-dx*dx)
			if v >= s.y && v <= hull {
				return hullDark, true
			}
			// Mast and yard, thin verticals that read as one dark stroke.
			if math.Abs(u-s.x) < 0.0035 && v < s.y && v > s.y-s.mast {
				return hullDark, true
			}
			if math.Abs(v-(s.y-s.mast*0.62)) < 0.0035 && math.Abs(u-s.x) < s.w*0.42 {
				return hullDark, true
			}
		}
	}
	return rgb{}, false
}

// cypresses are the darkest verticals and the only tall shapes. They frame the
// left edge and stop the eye leaving the picture.
func cypresses(u, v float64) (rgb, bool) {
	type tree struct{ x, base, h, w float64 }
	for _, t := range []tree{
		{0.052, 0.995, 0.62, 0.040},
		{0.118, 0.965, 0.44, 0.028},
		{0.958, 0.930, 0.36, 0.024},
	} {
		if v > t.base || v < t.base-t.h {
			continue
		}
		// A flame shape: widest a third of the way up, tapering to a point.
		p := (t.base - v) / t.h
		w := t.w * math.Sin(math.Pow(clamp(p, 0, 1), 0.42)*math.Pi) * 1.2
		wob := (fbm(v*40, t.x*100, 2, 41) - 0.5) * t.w * 0.5
		if math.Abs(u-t.x-wob) < w {
			// Barely modelled. A silhouette that stays a silhouette.
			shade := 0.75 + 0.25*fbm(u*70, v*70, 2, 53)
			return rgb{cypress.r * shade, cypress.g * shade, cypress.b * shade}, true
		}
	}
	return rgb{}, false
}
