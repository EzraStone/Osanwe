package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"strconv"
	"strings"
)

// Dithering is the other way to say the same thing the character mosaic says,
// and it says it better at small sizes.
//
// A palette of four colours cannot represent a photograph. Error diffusion
// spends that shortfall deliberately: each pixel takes the nearest colour it
// can, and the difference is pushed into neighbours that have not been decided
// yet, so the error is spread rather than accumulated. Stand back and the eye
// integrates it into tones that are not in the palette at all. Up close there
// is nothing but four flat colours in an agitated pattern.
//
// That is the same claim as the mosaic -- detail that exists only at a distance
// -- in a form that survives being a favicon.

// Palettes are named so an operator can change the whole image with one flag
// rather than editing hex by hand.
var palettes = map[string][]rgb{
	// The interface's own colours. Warm, light-grounded, and deliberately not
	// the saturated blue of a screenshot: the site and the product should not
	// look like two different projects.
	"osanwe": {
		{0.976, 0.945, 0.878}, // cream, the page
		{0.847, 0.702, 0.404}, // muted gold, the accent
		{0.278, 0.412, 0.573}, // dusty blue, sky and water
		{0.180, 0.208, 0.184}, // deep olive, foliage and shadow
		{0.075, 0.082, 0.094}, // near black, the darkest note
	},
	// Three colours only. Harsher, more graphic, and the one to reach for if the
	// image has to read at 64 pixels wide.
	"three": {
		{0.976, 0.945, 0.878},
		{0.267, 0.427, 0.639},
		{0.145, 0.180, 0.153},
	},
	// Ink on paper, for a print or a single-colour context.
	"mono": {
		{0.976, 0.945, 0.878},
		{0.110, 0.114, 0.118},
	},
}

// parsePalette accepts a name or a comma-separated list of hex colours.
func parsePalette(spec string) ([]rgb, error) {
	if p, ok := palettes[spec]; ok {
		return p, nil
	}
	var out []rgb
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "#"))
		if len(part) != 6 {
			return nil, fmt.Errorf("palette entry %q is not a 6-digit hex colour or a known palette name", part)
		}
		var c rgb
		for i, dst := range []*float64{&c.r, &c.g, &c.b} {
			v, err := strconv.ParseUint(part[i*2:i*2+2], 16, 8)
			if err != nil {
				return nil, fmt.Errorf("palette entry %q: %w", part, err)
			}
			*dst = float64(v) / 255
		}
		out = append(out, c)
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("a palette needs at least two colours, got %d", len(out))
	}
	return out, nil
}

// nearest finds the palette entry closest to c.
//
// Distance is weighted toward green because that is where the eye's acuity is;
// an unweighted comparison picks colours that measure close and look wrong.
func nearest(c rgb, palette []rgb) (rgb, rgb) {
	best, bestD := palette[0], math.Inf(1)
	for _, p := range palette {
		dr, dg, db := c.r-p.r, c.g-p.g, c.b-p.b
		d := 2*dr*dr + 4*dg*dg + 3*db*db
		if d < bestD {
			best, bestD = p, d
		}
	}
	return best, rgb{c.r - best.r, c.g - best.g, c.b - best.b}
}

// dither renders the scene to a paletted PNG using Floyd-Steinberg diffusion.
//
// The error is carried in a two-row window rather than the whole image, which is
// all the algorithm ever needs and keeps memory flat regardless of size.
func dither(w io.Writer, scene func(u, v float64) rgb, width, height int, palette []rgb, gain, spread float64) error {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Two rows of accumulated error: the one being drawn and the one below.
	this := make([]rgb, width+2)
	next := make([]rgb, width+2)

	for y := 0; y < height; y++ {
		v := (float64(y) + 0.5) / float64(height)
		for x := 0; x < width; x++ {
			u := (float64(x) + 0.5) / float64(width)

			c := scene(u, v)
			c = applyGain(c, gain)

			// Add this pixel's share of the error diffused into it.
			e := this[x+1]
			c = rgb{c.r + e.r, c.g + e.g, c.b + e.b}

			chosen, err := nearest(c, palette)
			img.Set(x, y, color.RGBA{
				R: uint8(clamp(chosen.r, 0, 1) * 255),
				G: uint8(clamp(chosen.g, 0, 1) * 255),
				B: uint8(clamp(chosen.b, 0, 1) * 255),
				A: 255,
			})

			// Floyd-Steinberg weights: 7/16 right, 3/16 below-left, 5/16 below,
			// 1/16 below-right. Scaling them down below 1 tightens the texture,
			// which matters when the output will be viewed small.
			err = rgb{err.r * spread, err.g * spread, err.b * spread}
			add := func(row []rgb, i int, k float64) {
				row[i].r += err.r * k
				row[i].g += err.g * k
				row[i].b += err.b * k
			}
			add(this, x+2, 7.0/16)
			add(next, x, 3.0/16)
			add(next, x+1, 5.0/16)
			add(next, x+2, 1.0/16)
		}
		this, next = next, this
		for i := range next {
			next[i] = rgb{}
		}
	}

	return png.Encode(w, img)
}

// applyGain stretches contrast about mid-grey while holding hue, so the palette
// is used across its whole range instead of clustering in the middle.
func applyGain(c rgb, gain float64) rgb {
	l := luma(c)
	if l <= 0 {
		return c
	}
	scaled := contrast(l, gain)
	k := scaled / l
	return rgb{clamp(c.r*k, 0, 1), clamp(c.g*k, 0, 1), clamp(c.b*k, 0, 1)}
}
