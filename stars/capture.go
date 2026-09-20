// stars/capture.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
)

func (sp *Pane) handleCapture(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	if !sp.capture.enabled {
		return
	}

	readPixels := func() *image.RGBA {
		// Window coords -> fb coords, also accounting for retina 2x
		p0 := math.Add2f(sp.capture.region[0], ctx.PaneExtent.P0)
		p1 := math.Add2f(sp.capture.region[1], ctx.PaneExtent.P0)
		p0, p1 = math.Scale2f(p0, 2), math.Scale2f(p1, 2)

		x := int(min(p0[0], p1[0]))
		y := int(min(p0[1], p1[1]))
		w := int(max(p0[0], p1[0])) - x
		h := int(max(p0[1], p1[1])) - y
		px := ctx.Renderer.ReadPixelRGBAs(x, y, w, h)

		// Flip in y
		for i := range h / 2 {
			for j := range 4 * w {
				a, b := 4*w*i+j, 4*w*(h-1-i)+j
				px[a], px[b] = px[b], px[a]
			}
		}
		// Alpha to 1
		for i := range h {
			for j := range w {
				px[4*w*i+4*j+3] = 255
			}
		}

		return &image.RGBA{
			Pix:    px,
			Stride: 4 * w,
			Rect: image.Rectangle{
				Min: image.Point{X: x, Y: y},
				Max: image.Point{X: x + w, Y: y + h},
			},
		}
	}

	if sp.capture.doStill && sp.capture.haveRegion {
		fn := "capture.png"
		if d, err := os.UserHomeDir(); err == nil {
			fn = d + "/" + fn
		}
		w, err := os.Create(fn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
		} else {
			img := readPixels()
			if err = png.Encode(w, img); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
			}
			w.Close()
		}
		sp.capture.doStill = false
	} else if sp.capture.doVideo && sp.capture.haveRegion {
		if sp.capture.video.frameCh == nil {
			// Starting a new capture
			sp.capture.video.frameCh = make(chan *image.RGBA, 100)
			sp.capture.video.lastFrame = time.Time{}
			go captureEncodeFrames(sp.capture.video.frameCh)
		}
		if time.Since(sp.capture.video.lastFrame) > 95*time.Millisecond {
			sp.capture.video.lastFrame = time.Now()
			sp.capture.video.frameCh <- readPixels()
		}
	} else if !sp.capture.doVideo && sp.capture.video.frameCh != nil {
		// Finish the capture
		close(sp.capture.video.frameCh)
		sp.capture.video.frameCh = nil
	}

	if sp.capture.specifyingRegion || sp.capture.haveRegion {
		p0, p1 := sp.capture.region[0], sp.capture.region[1]
		if sp.capture.specifyingRegion && ctx.Mouse != nil {
			p1 = ctx.Mouse.Pos
		}
		// Offset the outline so it isn't included in the capture
		p0[0], p1[0] = min(p0[0], p1[0])-1, max(p0[0], p1[0])+1
		p0[1], p1[1] = min(p0[1], p1[1])-1, max(p0[1], p1[1])+1

		ld := renderer.GetLinesDrawBuilder()
		defer renderer.ReturnLinesDrawBuilder(ld)

		ld.AddLineLoop([][2]float32{p0, {p0[0], p1[1]}, p1, {p1[0], p0[1]}})
		transforms.LoadWindowViewingMatrices(cb)
		cb.SetRGB(renderer.RGB{R: 0, G: 0.75, B: 0.75})
		ld.GenerateCommands(cb)
		cb.DisableBlend()
	}
}

// captureEncodeFrames runs in a goroutine that is launched when a video
// capture is initiated.  It reads images from the given chan and writes
// out an animated GIF when the chan is closed.
func captureEncodeFrames(ch chan *image.RGBA) {
	// Store regular and 2x resolution for retina displays.
	gifs := [2]*gif.GIF{{}, {}}
	// Though we could have a unique palette per frame, we only need a
	// handful of colors and having a shared one allows us to check for
	// image equivalence by just comparing the pixels' palette index
	// values.
	var palette []color.RGBA

	for {
		if img := <-ch; img != nil {
			nx, ny := img.Bounds().Max.X-img.Bounds().Min.X, img.Bounds().Max.Y-img.Bounds().Min.Y
			pal := [2]*image.Paletted{
				{
					Pix:    make([]uint8, nx/2*ny/2),
					Stride: nx / 2,
					Rect:   image.Rectangle{Max: image.Point{X: nx / 2, Y: ny / 2}},
				},
				{
					Pix:    make([]uint8, nx*ny),
					Stride: nx,
					Rect:   image.Rectangle{Max: image.Point{X: nx, Y: ny}},
				},
			}

			for y := range ny {
				for x := range nx {
					offset := 4 * (x + y*nx)
					r, g, b, a := img.Pix[offset], img.Pix[offset+1], img.Pix[offset+2], img.Pix[offset+3]

					// Find the pixel's color in the palette or add it to
					// the palette if it's not there.
					idx := -1
					// Simple linear search; we only have a few colors in
					// practice so this should be fine.
					for i, c := range palette {
						if c.R == r && c.G == g && c.B == b && c.A == a {
							idx = i
							break
						}
					}
					if idx == -1 {
						idx = len(palette)
						palette = append(palette, color.RGBA{R: r, G: g, B: b, A: a})
					}
					if idx > 255 {
						panic("too many colors")
					}

					pal[1].Pix[x+y*nx] = uint8(idx)

					if x&1 == 0 && y&1 == 0 {
						// The downsampled image is done via simple point
						// sampling. Since MSAA is disabled anyway, this
						// should be fine.
						pal[0].Pix[x/2+y/2*nx/2] = uint8(idx)
					}
				}
			}

			for i := range 2 {
				if n := len(gifs[i].Image); n > 0 && slices.Equal(pal[i].Pix, gifs[i].Image[n-1].Pix) {
					// If the new frame matches the last one added, just
					// increase the last frame's display time by another
					// 100ms rather than duplicating it.
					gifs[i].Delay[n-1] += 10
				} else {
					// The image has changed, so add it to the GIF.
					for _, c := range palette {
						pal[i].Palette = append(pal[i].Palette, c)
					}
					gifs[i].Image = append(gifs[i].Image, pal[i])
					gifs[i].Delay = append(gifs[i].Delay, 10 /* 100ths of seconds */)
				}
			}
		} else {
			// No more images; save the animated GIFs.
			for i := range 2 {
				fn := [2]string{"capture.gif", "capture-2x.gif"}[i]
				if d, err := os.UserHomeDir(); err == nil {
					fn = d + "/" + fn
				}
				w, err := os.Create(fn)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
				} else {
					if n := len(gifs[i].Image); n > 3 {
						// Drop the first and last image so that all of the
						// ones we keep have been visible for their full
						// time-slice.
						gifs[i].Image = gifs[i].Image[1 : n-1]
						gifs[i].Delay = gifs[i].Delay[1 : n-1]
					}

					if err := gif.EncodeAll(w, gifs[i]); err != nil {
						fmt.Fprintf(os.Stderr, "%v\n", err)
					}
					w.Close()
				}
				fmt.Printf("saved %s; %d frames\n", fn, len(gifs[i].Image))
			}
			return
		}
	}
}

func (sp *Pane) qlPositionsString() string {
	ps := sp.currentPrefs()
	tcps := slices.Collect(maps.Keys(ps.QuickLookTCPs))
	sort.Slice(tcps, func(a, b int) bool {
		aplus, bplus := ps.QuickLookTCPs[tcps[a]], ps.QuickLookTCPs[tcps[b]]
		if aplus && !bplus {
			return true
		} else if bplus && !aplus {
			return false
		}
		return tcps[a] < tcps[b]
	})
	for i := range tcps {
		if ps.QuickLookTCPs[tcps[i]] {
			tcps[i] += "+"
		}
	}

	s := strings.Join(tcps, " ")
	if len(s) > 32 {
		// Split if the first line is too long
		idx := strings.LastIndexByte(s[:32], ' ')
		if idx != -1 {
			s = s[:idx] + "\n" + s[idx+1:]
		}
		// If it can't fit into two lines, truncate and end with a +
		if len(s) > 64 {
			idx := strings.LastIndexByte(s[:64], ' ')
			if idx != -1 {
				s = s[:idx] + "+"
			}
		}
	}
	return s
}
