// Command mosaic renders the site's hero image as a mosaic of characters.
//
//	go run ./tools/mosaic > hero.svg
//	go run ./tools/mosaic -cols 320 -bytes capture.pcap > hero.svg
//
// The picture is a harbour at low sun, and it is drawn entirely out of bytes
// taken from a real TLS session -- by default one this program conducts with
// itself, so the glyphs are genuine ciphertext rather than decorative digits.
//
// That is the whole argument, made in one image. Up close there is nothing to
// read: the characters are exactly what a relay carries, and a relay carrying
// them learns as much as you do staring at them. Step back and the harbour is
// obvious. Only distance -- which is to say, only the endpoints -- resolves it.
//
// The output is SVG rather than a raster, because a hero image that can be
// regenerated at any size and re-coloured in a text editor stays maintainable,
// and because the character grid is the point and should not be resampled.
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"math/big"
	"net"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mosaic:", err)
		os.Exit(1)
	}
}

func run() error {
	cols := flag.Int("cols", 300, "character columns; the whole resolution of the image")
	aspect := flag.Float64("aspect", 16.0/9.0, "width over height")
	cellW := flag.Float64("cell", 7.2, "character cell width in SVG units")
	bytesPath := flag.String("bytes", "", "file whose bytes become the glyphs (default: a live TLS handshake)")
	imagePath := flag.String("image", "", "PNG or JPEG to render (default: the built-in harbour)")
	gain := flag.Float64("contrast", 1.55, "tonal contrast before quantising; 1 leaves the source alone")
	background := flag.String("bg", "#faf8f3", "page colour behind the characters")
	flag.Parse()

	if *cols < 40 || *cols > 2000 {
		return fmt.Errorf("-cols %d is outside the useful range 40..2000", *cols)
	}

	// A character cell is about twice as tall as it is wide, so the row count
	// has to account for that or the image comes out stretched.
	const cellAspect = 1.85
	rows := int(math.Round(float64(*cols) / *aspect * (1 / cellAspect) * 1.0))

	pool, source, err := glyphBytes(*bytesPath)
	if err != nil {
		return err
	}

	// A photograph, when there is one, otherwise the drawn scene. The renderer
	// does not care which: both are just a function from (u,v) to a colour.
	scene := sample
	if *imagePath != "" {
		scene, err = loadImage(*imagePath)
		if err != nil {
			return err
		}
	}

	out := bufio.NewWriterSize(os.Stdout, 1<<20)
	defer out.Flush()
	return render(out, scene, *cols, rows, *cellW, cellAspect, *gain, *background, pool, source)
}

// ramp orders characters by how much ink they put on the page. Choosing by
// density rather than at random is what lets the mosaic carry tone: dark parts
// of the scene get crowded glyphs, light parts get sparse ones, and the image
// survives being converted to a single colour.
var ramp = []string{
	" ", "'", ".", ",", ":", "-", "~", "+", "/", "7", "?", "3", "5", "s", "z",
	"e", "a", "o", "9", "6", "8", "0", "%", "#", "$", "@",
}

// glyphBytes returns the byte pool the glyphs are drawn from, and a description
// of where it came from for the SVG's own metadata.
func glyphBytes(path string) ([]byte, string, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
		if len(b) == 0 {
			return nil, "", fmt.Errorf("%s is empty", path)
		}
		return b, path, nil
	}
	b, err := handshakeBytes()
	if err != nil {
		return nil, "", fmt.Errorf("recording a TLS session: %w", err)
	}
	return b, "a TLS session recorded while generating this image", nil
}

// handshakeBytes performs a real TLS handshake over a recording pipe and
// returns the bytes that crossed it.
//
// This is not a stand-in for encrypted data; it is encrypted data. The records
// here are the same shape as the ones a ranger relays, and the relay's view of
// a conversation is precisely this: bytes it moved and cannot interpret.
func handshakeBytes() ([]byte, error) {
	cert, err := selfSigned()
	if err != nil {
		return nil, err
	}

	client, server := net.Pipe()
	var wire bytes.Buffer
	done := make(chan error, 1)

	go func() {
		conn := tls.Server(&recorder{Conn: server, into: &wire}, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
		})
		if err := conn.Handshake(); err != nil {
			done <- err
			return
		}
		// Some application data, so the pool is not only handshake records.
		_, _ = io.WriteString(conn, "the relay carries this and cannot read it. "+
			"neither can you, which is the entire point of the picture it is drawn in.")
		done <- conn.Close()
	}()

	conn := tls.Client(client, &tls.Config{
		InsecureSkipVerify: true, // a throwaway certificate this process just made
		MinVersion:         tls.VersionTLS13,
		ServerName:         "osanwe.invalid",
	})
	if err := conn.Handshake(); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Read(buf)
	_ = conn.Close()
	<-done

	if wire.Len() < 512 {
		return nil, fmt.Errorf("only %d bytes crossed the wire; too few to draw with", wire.Len())
	}
	return wire.Bytes(), nil
}

// recorder copies everything crossing a connection into a buffer.
type recorder struct {
	net.Conn
	into *bytes.Buffer
}

func (r *recorder) Read(p []byte) (int, error) {
	n, err := r.Conn.Read(p)
	r.into.Write(p[:n])
	return n, err
}

func (r *recorder) Write(p []byte) (int, error) {
	r.into.Write(p)
	return r.Conn.Write(p)
}

func selfSigned() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "osanwe.invalid"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"osanwe.invalid"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// luma is perceptual brightness. The mosaic's density ramp is driven by this
// and not by the average of the channels, because a saturated blue and a pale
// cream can average alike and look nothing alike.
func luma(c rgb) float64 { return 0.2126*c.r + 0.7152*c.g + 0.0722*c.b }

// contrast is an S-curve about mid-grey. It pushes the scene toward the ends of
// the density ramp so that the four masses it was composed from stay four
// distinguishable masses after quantisation.
func contrast(l, strength float64) float64 {
	if strength <= 0 {
		strength = 1
	}
	if l < 0.5 {
		return 0.5 * math.Pow(l*2, strength)
	}
	return 1 - 0.5*math.Pow((1-l)*2, strength)
}

// loadImage turns a photograph into the same (u,v) sampling function the drawn
// scene provides, box-filtering each cell rather than point-sampling it.
//
// Point sampling a photograph at one glyph per cell is how ASCII conversions
// come out looking like static: a single pixel from a busy region is noise,
// while the average of the whole cell is tone, and tone is the only thing the
// mosaic can carry.
func loadImage(path string) (func(u, v float64) rgb, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, format, err := image.Decode(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	_ = format
	b := src.Bounds()
	if b.Dx() < 8 || b.Dy() < 8 {
		return nil, fmt.Errorf("%s is %dx%d, too small to render", path, b.Dx(), b.Dy())
	}

	return func(u, v float64) rgb {
		// The caller hands cell centres, so the cell spans half a step either
		// side. Recovering that from u alone is not possible, so a fixed small
		// window is used; it is wide enough to average away sensor noise and
		// narrow enough to keep edges.
		const window = 0.004
		var sum rgb
		var n float64
		for du := -window; du <= window; du += window {
			for dv := -window; dv <= window; dv += window {
				x := b.Min.X + int(clamp(u+du, 0, 0.9999)*float64(b.Dx()))
				y := b.Min.Y + int(clamp(v+dv, 0, 0.9999)*float64(b.Dy()))
				r, g, bl, _ := src.At(x, y).RGBA()
				// RGBA returns 16-bit alpha-premultiplied values.
				sum.r += float64(r) / 65535
				sum.g += float64(g) / 65535
				sum.b += float64(bl) / 65535
				n++
			}
		}
		return rgb{sum.r / n, sum.g / n, sum.b / n}
	}, nil
}

// deepen darkens and slightly saturates a colour for use as glyph ink.
func deepen(c rgb) rgb {
	l := luma(c)
	// Pull each channel away from the scene's own luminance: a little more
	// colour survives the character grid than if the ink were simply darkened.
	sat := func(v float64) float64 { return clamp(l+(v-l)*1.25, 0, 1) }
	// Light areas can be darkened hard without closing up; dark areas cannot.
	k := lerp(0.74, 0.60, smoothstep(0.25, 1.0, l))
	return rgb{sat(c.r) * k, sat(c.g) * k, sat(c.b) * k}
}

func hex(c rgb) string {
	to := func(v float64) int { return int(math.Round(clamp(v, 0, 1) * 255)) }
	return fmt.Sprintf("#%02x%02x%02x", to(c.r), to(c.g), to(c.b))
}

// quantise rounds a colour to a coarse grid. Adjacent cells then share a colour
// far more often, which lets whole runs collapse into one SVG element -- the
// difference between a file of a few hundred kilobytes and one of many
// megabytes, with no visible change at this glyph size.
func quantise(c rgb) rgb {
	const steps = 22
	q := func(v float64) float64 { return math.Round(clamp(v, 0, 1)*steps) / steps }
	return rgb{q(c.r), q(c.g), q(c.b)}
}

func escape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func render(w io.Writer, scene func(u, v float64) rgb, cols, rows int, cellW, cellAspect, gain float64, bg string, pool []byte, source string) error {
	cellH := cellW * cellAspect
	width := float64(cols) * cellW
	height := float64(rows) * cellH

	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" role="img" aria-label="A stone harbour at low sun, drawn entirely in characters">`+"\n",
		width, height, width, height)
	fmt.Fprintf(w, "<title>Osanwë</title>\n<desc>%s. Every glyph is a byte from %s.</desc>\n",
		escape("A harbour at low sun. The breakwater runs out toward the light"), escape(source))
	fmt.Fprintf(w, `<rect width="100%%" height="100%%" fill="%s"/>`+"\n", bg)
	fmt.Fprintf(w, `<g font-family="ui-monospace,SFMono-Regular,Menlo,Consolas,monospace" font-size="%.2f" xml:space="preserve">`+"\n", cellH*0.98)

	at := 0
	next := func() byte {
		b := pool[at%len(pool)]
		at++
		return b
	}

	for row := 0; row < rows; row++ {
		v := (float64(row) + 0.5) / float64(rows)
		y := float64(row+1) * cellH

		// Build the row, then emit it as runs of one colour.
		glyphs := make([]string, cols)
		colours := make([]string, cols)
		for col := 0; col < cols; col++ {
			u := (float64(col) + 0.5) / float64(cols)
			c := scene(u, v)

			// Density from tone: dark areas get the crowded end of the ramp.
			//
			// The scene's own tonal range is deliberately stretched first. A
			// mosaic has roughly two dozen density steps to work with, and an
			// image using the middle third of them reads as uniform grey haze
			// -- which is what the first version of this did. Pushing contrast
			// before quantising is what puts a picture in the characters.
			b := next()
			l := contrast(luma(c), gain)
			idx := (1-l)*float64(len(ramp)-1) + (float64(b%16)/16-0.5)*1.4
			glyphs[col] = ramp[int(clamp(math.Round(idx), 0, float64(len(ramp)-1)))]

			// Characters are drawn darker and slightly more saturated than the
			// scene, so they hold against the page instead of dissolving into
			// it. Glyphs cover perhaps a third of their cell; without this the
			// whole image reads two stops lighter than it was drawn.
			colours[col] = hex(quantise(deepen(c)))
		}

		fmt.Fprintf(w, `<text y="%.1f">`, y)
		for start := 0; start < cols; {
			end := start + 1
			for end < cols && colours[end] == colours[start] {
				end++
			}
			var run bytes.Buffer
			for i := start; i < end; i++ {
				run.WriteString(glyphs[i])
			}
			fmt.Fprintf(w, `<tspan x="%.1f" fill="%s">%s</tspan>`,
				float64(start)*cellW, colours[start], escape(run.String()))
			start = end
		}
		fmt.Fprint(w, "</text>\n")
	}

	fmt.Fprint(w, "</g>\n</svg>\n")
	return nil
}
