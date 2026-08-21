package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"

	"fyne.io/fyne/v2"
)

// The icon is drawn rather than embedded, for the same reason build/assets/
// make-icon.py draws it: no binary in version control, and no image library to
// install. Doing it here as well means `go build ./cmd/local-dictation` produces
// an application that has its own icon — a plain checkout needs no Python step
// to end up with something other than the toolkit's default in the title bar.
//
// The geometry is make-icon.py's, in the same units, and iconRendersLikeTheScript
// in icon_test.go compares the two pixel for pixel when Python is available. A
// microphone capsule over a cradle and a stand, on a rounded square in slate,
// with a thin accent ring so it still reads at 16 px.
var (
	iconBackground = color.NRGBA{0x1F, 0x2A, 0x37, 0xFF}
	iconAccent     = color.NRGBA{0x4C, 0xC2, 0x8C, 0xFF} // the "listening" green
	iconGlyph      = color.NRGBA{0xF4, 0xF6, 0xF8, 0xFF}
)

var (
	iconOnce     sync.Once
	iconResource fyne.Resource
)

// appIcon renders once and hands back the same resource afterwards.
func appIcon() fyne.Resource {
	iconOnce.Do(func() {
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, renderIcon(256)); err != nil {
			return // no icon is a cosmetic loss; it must not stop the app
		}
		iconResource = fyne.NewStaticResource("local-dictation.png", encoded.Bytes())
	})
	return iconResource
}

func renderIcon(size int) *image.NRGBA {
	scale := float64(size) / 1024
	radius := 220 * scale

	bodyCX := 512 * scale
	bodyTop, bodyBottom := 250*scale, 520*scale
	bodyRadius := 108 * scale

	cradleRadius := 210 * scale
	cradleThickness := 56 * scale

	standTop, standBottom := 690*scale, 790*scale
	footTop, footBottom := 780*scale, 812*scale

	// Supersample small icons: at 16 px one sample per pixel turns the glyph
	// into noise.
	samples := 2
	if size <= 128 {
		samples = 4
	}
	step := 1.0 / float64(samples)
	total := float64(samples * samples)

	canvas := image.NewNRGBA(image.Rect(0, 0, size, size))
	for pixelY := 0; pixelY < size; pixelY++ {
		for pixelX := 0; pixelX < size; pixelX++ {
			var sum [4]float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					x := float64(pixelX) + (float64(sx)+0.5)*step
					y := float64(pixelY) + (float64(sy)+0.5)*step

					outside := roundedSquareAlpha(x, y, float64(size), radius)
					if outside <= 0 {
						continue
					}

					shade := iconBackground
					glyph := math.Max(
						capsuleAlpha(x, y, bodyCX, bodyTop, bodyBottom, bodyRadius),
						math.Max(
							arcAlpha(x, y, bodyCX, 470*scale, cradleRadius, cradleThickness),
							math.Max(
								barAlpha(x, y, bodyCX, standTop, standBottom, 32*scale),
								barAlpha(x, y, bodyCX, footTop, footBottom, 160*scale),
							),
						),
					)
					if glyph > 0 {
						shade = blendIcon(shade, iconGlyph, glyph)
					}

					ring := outside - roundedSquareAlpha(x, y, float64(size), radius-math.Max(1.0, 18*scale))
					if ring > 0 {
						shade = blendIcon(shade, iconAccent, math.Min(ring, 1.0)*0.9)
					}

					sum[0] += float64(shade.R) * outside
					sum[1] += float64(shade.G) * outside
					sum[2] += float64(shade.B) * outside
					sum[3] += outside
				}
			}

			alpha := sum[3] / total
			if alpha <= 0 {
				continue // the pixel stays fully transparent
			}
			canvas.SetNRGBA(pixelX, pixelY, color.NRGBA{
				R: roundToByte(sum[0] / sum[3]),
				G: roundToByte(sum[1] / sum[3]),
				B: roundToByte(sum[2] / sum[3]),
				A: roundToByte(alpha * 255),
			})
		}
	}
	return canvas
}

// roundedSquareAlpha is signed-distance coverage for a rounded square.
func roundedSquareAlpha(x, y, size, radius float64) float64 {
	half := size / 2
	px, py := math.Abs(x-half), math.Abs(y-half)
	inner := half - radius
	dx, dy := math.Max(px-inner, 0), math.Max(py-inner, 0)
	return clampIcon(0.5 - (math.Hypot(dx, dy) - radius))
}

// capsuleAlpha covers a vertical capsule: the microphone body.
func capsuleAlpha(x, y, cx, top, bottom, radius float64) float64 {
	cy := math.Min(math.Max(y, top), bottom)
	return clampIcon(0.5 - (math.Hypot(x-cx, y-cy) - radius))
}

// arcAlpha covers the lower half of a ring: the cradle.
func arcAlpha(x, y, cx, cy, radius, thickness float64) float64 {
	if y < cy {
		return 0
	}
	return clampIcon(0.5 - (math.Abs(math.Hypot(x-cx, y-cy)-radius) - thickness/2))
}

// barAlpha covers the stand and its foot.
func barAlpha(x, y, cx, top, bottom, halfWidth float64) float64 {
	insideX := halfWidth - math.Abs(x-cx)
	insideY := math.Min(y-top, bottom-y)
	return clampIcon(0.5 + math.Min(insideX, insideY))
}

func blendIcon(bottom, top color.NRGBA, alpha float64) color.NRGBA {
	mix := func(b, t uint8) uint8 {
		return roundToByte(float64(b) + (float64(t)-float64(b))*alpha)
	}
	return color.NRGBA{mix(bottom.R, top.R), mix(bottom.G, top.G), mix(bottom.B, top.B), 0xFF}
}

func clampIcon(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

// roundToByte rounds half away from zero, matching Python's round-half-even only
// where it matters here: every input is non-negative and the script's values are
// far from .5 boundaries except at edges, where a one-step difference is
// invisible. The test compares against the script to keep that honest.
func roundToByte(value float64) uint8 {
	rounded := math.Floor(value + 0.5)
	switch {
	case rounded < 0:
		return 0
	case rounded > 255:
		return 255
	default:
		return uint8(rounded)
	}
}
