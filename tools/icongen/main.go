// Draws the SteamKopru icon procedurally (signed distances + supersampling) and
// writes assets/icon.png, assets/icon.ico and rsrc_windows_amd64.syso (the
// .exe icon). Run from the project root:  go run ./tools/icongen
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const N = 1024 // master resolution

type rgba struct{ r, g, b, a float64 }

func hex(h uint32, a float64) rgba {
	return rgba{float64(h>>16&255) / 255, float64(h>>8&255) / 255, float64(h&255) / 255, a}
}

// premultiplied "over"
func over(dst, src rgba) rgba {
	a := src.a + dst.a*(1-src.a)
	if a <= 0 {
		return rgba{}
	}
	m := func(s, d float64) float64 { return (s*src.a + d*dst.a*(1-src.a)) / a }
	return rgba{m(src.r, dst.r), m(src.g, dst.g), m(src.b, dst.b), a}
}

func lerp(a, b rgba, t float64) rgba {
	return rgba{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t, a.a + (b.a-a.a)*t}
}

func clamp(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// coverage from signed distance (negative inside)
func cov(d float64) float64 { px := 1.0 / N; return clamp(0.5 - d/px) }

func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := clamp(((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy))
	ex, ey := px-ax-t*dx, py-ay-t*dy
	return math.Hypot(ex, ey)
}

func roundRect(px, py, x0, y0, x1, y1, r float64) float64 {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hx, hy := (x1-x0)/2-r, (y1-y0)/2-r
	qx, qy := math.Abs(px-cx)-hx, math.Abs(py-cy)-hy
	out := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return out + math.Min(math.Max(qx, qy), 0) - r
}

// bridge geometry
const (
	deckY  = 0.585
	archL  = 0.215
	archR  = 0.785
	archTo = 0.315 // peak
)

func archY(u float64) float64 {
	t := (u - 0.5) / ((archR - archL) / 2)
	return deckY - (deckY-archTo)*(1-t*t)
}

var archPts [][2]float64

func init() {
	for i := 0; i <= 160; i++ {
		u := archL + (archR-archL)*float64(i)/160
		archPts = append(archPts, [2]float64{u, archY(u)})
	}
}

func polyDist(px, py float64, pts [][2]float64) float64 {
	d := 1e9
	for i := 0; i+1 < len(pts); i++ {
		d = math.Min(d, segDist(px, py, pts[i][0], pts[i][1], pts[i+1][0], pts[i+1][1]))
	}
	return d
}

func shade(u, v float64) rgba {
	var c rgba
	// soft drop shadow
	sd := roundRect(u, v-0.012, 0.05, 0.05, 0.95, 0.95, 0.205)
	c = over(c, rgba{0, 0, 0, 0.35 * clamp(1-(sd+0.01)/0.035)})

	// background tile: diagonal gradient + top sheen
	bd := roundRect(u, v, 0.05, 0.04, 0.95, 0.94, 0.2)
	if a := cov(bd); a > 0 {
		t := clamp((u*0.45 + v*0.75) / 1.1)
		bg := lerp(hex(0x14213a, 1), hex(0x1a6bb0, 1), t)
		// glow behind the bridge
		g := math.Exp(-(math.Pow((u-0.5)/0.32, 2) + math.Pow((v-0.52)/0.22, 2)))
		bg = over(bg, hex(0x66c0f4, 0.22*g))
		// sheen on the upper half
		bg = over(bg, rgba{1, 1, 1, 0.07 * clamp((0.5-v)/0.45)})
		// thin inner rim
		bg = over(bg, rgba{1, 1, 1, 0.10 * cov(math.Abs(bd+0.006)-0.004)})
		bg.a *= a
		c = over(c, bg)
	}
	if bd > 0.002 {
		return c
	}

	white := hex(0xffffff, 1)
	cyan := hex(0x66c0f4, 1)

	// water ripples
	for i, wy := range []float64{0.735, 0.80} {
		w := 0.30 - float64(i)*0.08
		pts := [][2]float64{}
		for k := 0; k <= 60; k++ {
			x := 0.5 - w + 2*w*float64(k)/60
			pts = append(pts, [2]float64{x, wy + 0.010*math.Sin((x-0.5)*40)})
		}
		c = over(c, rgba{cyan.r, cyan.g, cyan.b, (0.45 - 0.15*float64(i)) * cov(polyDist(u, v, pts)-0.011)})
	}

	// pillars under the arch feet
	for _, x := range []float64{archL, archR} {
		c = over(c, rgba{0.85, 0.92, 1, cov(segDist(u, v, x, deckY, x, 0.69) - 0.026)})
	}
	// hangers
	for _, x := range []float64{0.315, 0.4075, 0.5, 0.5925, 0.685} {
		c = over(c, rgba{1, 1, 1, 0.9 * cov(segDist(u, v, x, archY(x), x, deckY)-0.011)})
	}
	// arch
	c = over(c, rgba{1, 1, 1, cov(polyDist(u, v, archPts) - 0.03)})
	// deck
	c = over(c, rgba{1, 1, 1, cov(segDist(u, v, 0.15, deckY, 0.85, deckY) - 0.034)})

	// endpoint nodes: glow, white ring, cyan core
	for _, x := range []float64{0.15, 0.85} {
		d := math.Hypot(u-x, v-deckY)
		c = over(c, rgba{cyan.r, cyan.g, cyan.b, 0.55 * math.Exp(-math.Pow(d/0.085, 2))})
		c = over(c, rgba{white.r, white.g, white.b, cov(d - 0.068)})
		c = over(c, rgba{cyan.r, cyan.g, cyan.b, cov(d - 0.046)})
		c = over(c, rgba{1, 1, 1, 0.8 * cov(math.Hypot(u-x+0.013, v-deckY+0.015)-0.013)})
	}
	return c
}

// render draws the icon at size×size, supersampling each pixel.
func render(size int) *image.NRGBA {
	ss := (N + size - 1) / size
	if ss < 2 {
		ss = 2
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for yy := 0; yy < ss; yy++ {
				for xx := 0; xx < ss; xx++ {
					u := (float64(x) + (float64(xx)+0.5)/float64(ss)) / float64(size)
					v := (float64(y) + (float64(yy)+0.5)/float64(ss)) / float64(size)
					p := shade(u, v)
					r += p.r * p.a
					g += p.g * p.a
					b += p.b * p.a
					a += p.a
				}
			}
			n := float64(ss * ss)
			if a > 0 {
				r, g, b = r/a, g/a, b/a
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(r*255 + .5), uint8(g*255 + .5), uint8(b*255 + .5), uint8(a/n*255 + .5)})
		}
	}
	return img
}

func pngBytes(img image.Image) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// dib = icon image as a 32-bit BITMAPINFOHEADER + BGRA (bottom-up) + empty AND mask.
func dib(img *image.NRGBA) []byte {
	s := img.Rect.Dx()
	var buf bytes.Buffer
	le := func(v any) { binary.Write(&buf, binary.LittleEndian, v) }
	le(uint32(40))
	le(int32(s))
	le(int32(2 * s))
	le(uint16(1))
	le(uint16(32))
	le(uint32(0))
	le(uint32(0))
	le(int32(0))
	le(int32(0))
	le(uint32(0))
	le(uint32(0))
	for y := s - 1; y >= 0; y-- {
		for x := 0; x < s; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	maskRow := ((s + 31) / 32) * 4
	buf.Write(make([]byte, maskRow*s))
	return buf.Bytes()
}

type iconImg struct {
	size int
	data []byte
}

// writeICO writes a standard .ico file.
func writeICO(path string, imgs []iconImg) {
	var buf bytes.Buffer
	le := func(v any) { binary.Write(&buf, binary.LittleEndian, v) }
	le([]uint16{0, 1, uint16(len(imgs))})
	off := 6 + 16*len(imgs)
	for _, im := range imgs {
		w := byte(im.size)
		if im.size >= 256 {
			w = 0
		}
		buf.Write([]byte{w, w, 0, 0})
		le([]uint16{1, 32})
		le([]uint32{uint32(len(im.data)), uint32(off)})
		off += len(im.data)
	}
	for _, im := range imgs {
		buf.Write(im.data)
	}
	os.WriteFile(path, buf.Bytes(), 0644)
}

// writeSyso writes a COFF object holding RT_ICON + RT_GROUP_ICON resources,
// which the Go linker embeds into the .exe (it picks up *.syso automatically).
func writeSyso(path string, imgs []iconImg) {
	const (
		rtIcon      = 3
		rtGroupIcon = 14
		lang        = 0x0409
	)
	// group icon directory
	var grp bytes.Buffer
	gle := func(v any) { binary.Write(&grp, binary.LittleEndian, v) }
	gle([]uint16{0, 1, uint16(len(imgs))})
	for i, im := range imgs {
		w := byte(im.size)
		if im.size >= 256 {
			w = 0
		}
		grp.Write([]byte{w, w, 0, 0})
		gle([]uint16{1, 32})
		gle(uint32(len(im.data)))
		gle(uint16(i + 1))
	}

	type leaf struct {
		typ, id int
		data    []byte
	}
	var leaves []leaf
	for i, im := range imgs {
		leaves = append(leaves, leaf{rtIcon, i + 1, im.data})
	}
	leaves = append(leaves, leaf{rtGroupIcon, 1, grp.Bytes()})
	types := []int{rtIcon, rtGroupIcon}
	byType := map[int][]leaf{}
	for _, l := range leaves {
		byType[l.typ] = append(byType[l.typ], l)
	}

	// layout: root dir, type dirs (name level), lang dirs, data entries, data
	dirSize := func(n int) int { return 16 + 8*n }
	off := dirSize(len(types))
	nameDirOff := map[int]int{}
	for _, t := range types {
		nameDirOff[t] = off
		off += dirSize(len(byType[t]))
	}
	langDirOff := make([]int, len(leaves))
	for i := range leaves {
		langDirOff[i] = off
		off += dirSize(1)
	}
	dataEntOff := make([]int, len(leaves))
	for i := range leaves {
		dataEntOff[i] = off
		off += 16
	}
	dataOff := make([]int, len(leaves))
	for i, l := range leaves {
		off = (off + 7) &^ 7
		dataOff[i] = off
		off += len(l.data)
	}
	sec := make([]byte, (off+7)&^7)
	put32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(sec[at:], v) }
	put16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(sec[at:], v) }
	dirHdr := func(at, n int) { put16(at+14, uint16(n)) } // all ID entries
	const isDir = 0x80000000

	dirHdr(0, len(types))
	for i, t := range types {
		put32(16+8*i, uint32(t))
		put32(16+8*i+4, isDir|uint32(nameDirOff[t]))
	}
	idx := map[*leaf]int{}
	for i := range leaves {
		idx[&leaves[i]] = i
	}
	for _, t := range types {
		at := nameDirOff[t]
		n := 0
		for i := range leaves {
			if leaves[i].typ != t {
				continue
			}
			if n == 0 {
				dirHdr(at, len(byType[t]))
			}
			put32(at+16+8*n, uint32(leaves[i].id))
			put32(at+16+8*n+4, isDir|uint32(langDirOff[i]))
			n++
		}
	}
	var relocs []int
	for i, l := range leaves {
		dirHdr(langDirOff[i], 1)
		put32(langDirOff[i]+16, lang)
		put32(langDirOff[i]+20, uint32(dataEntOff[i]))
		put32(dataEntOff[i], uint32(dataOff[i])) // RVA, fixed up by relocation
		put32(dataEntOff[i]+4, uint32(len(l.data)))
		relocs = append(relocs, dataEntOff[i])
		copy(sec[dataOff[i]:], l.data)
	}

	// COFF object
	var o bytes.Buffer
	le := func(v any) { binary.Write(&o, binary.LittleEndian, v) }
	rawPtr := 20 + 40
	relPtr := rawPtr + len(sec)
	symPtr := relPtr + 10*len(relocs)
	le(uint16(0x8664)) // AMD64
	le(uint16(1))
	le(uint32(0))
	le(uint32(symPtr))
	le(uint32(1))
	le(uint16(0))
	le(uint16(0))
	o.Write([]byte(".rsrc\x00\x00\x00"))
	le([]uint32{0, 0, uint32(len(sec)), uint32(rawPtr), uint32(relPtr), 0})
	le([]uint16{uint16(len(relocs)), 0})
	le(uint32(0x40000040)) // initialized data, readable
	o.Write(sec)
	for _, r := range relocs {
		le(uint32(r))
		le(uint32(0)) // symbol 0 (.rsrc)
		le(uint16(3)) // IMAGE_REL_AMD64_ADDR32NB
	}
	o.Write([]byte(".rsrc\x00\x00\x00"))
	le(uint32(0))
	le(int16(1))
	le(uint16(0))
	o.Write([]byte{3, 0}) // static, no aux
	le(uint32(4))         // empty string table
	os.WriteFile(path, o.Bytes(), 0644)
}

// go run ./tools/icongen   (from the project root)
func main() {
	os.MkdirAll("assets", 0755)
	os.WriteFile(filepath.Join("assets", "icon.png"), pngBytes(render(512)), 0644)
	var imgs []iconImg
	for _, s := range []int{16, 20, 24, 32, 40, 48, 64, 96, 128, 256} {
		img := render(s)
		if s >= 256 {
			imgs = append(imgs, iconImg{s, pngBytes(img)})
		} else {
			imgs = append(imgs, iconImg{s, dib(img)})
		}
	}
	writeICO(filepath.Join("assets", "icon.ico"), imgs)
	writeSyso("rsrc_windows_amd64.syso", imgs)
}
