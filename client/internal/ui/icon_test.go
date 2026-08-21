package ui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTheIconRenders(t *testing.T) {
	icon := appIcon()
	if icon == nil {
		t.Fatal("no icon; the title bar would fall back to the toolkit default")
	}

	decoded, format, err := image.Decode(bytes.NewReader(icon.Content()))
	if err != nil {
		t.Fatalf("the icon is not a readable image: %v", err)
	}
	if format != "png" {
		t.Errorf("icon encoded as %q, want png", format)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != 256 || bounds.Dy() != 256 {
		t.Errorf("icon is %dx%d, want 256x256", bounds.Dx(), bounds.Dy())
	}
}

// The corners are outside the rounded square and the middle is inside it. A
// glyph that renders as a blank or a full square passes every other check here,
// so this is the one that says something was actually drawn.
func TestTheIconIsARoundedSquareWithAGlyph(t *testing.T) {
	drawn := renderIcon(256)

	if _, _, _, alpha := drawn.At(1, 1).RGBA(); alpha != 0 {
		t.Errorf("the top-left corner is opaque (alpha %d); the square is not rounded", alpha)
	}
	if _, _, _, alpha := drawn.At(128, 128).RGBA(); alpha == 0 {
		t.Error("the centre is transparent; nothing was drawn")
	}

	// The capsule sits above the middle and is near-white; the plate is slate.
	capsule := drawn.NRGBAAt(128, 96)
	plate := drawn.NRGBAAt(40, 128)
	if capsule.R < 0xC0 || capsule.G < 0xC0 || capsule.B < 0xC0 {
		t.Errorf("the microphone body is %v, expected near-white", capsule)
	}
	if plate.R > 0x60 || plate.G > 0x60 || plate.B > 0x70 {
		t.Errorf("the background plate is %v, expected slate", plate)
	}
}

// Two implementations of one picture drift. This is what keeps them honest:
// the installers use make-icon.py for the .icns and .ico, the app draws its own,
// and they have to agree.
func TestTheIconMatchesTheScriptTheInstallersUse(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not available to compare against")
	}
	script, err := filepath.Abs("../../../build/assets/make-icon.py")
	if err != nil {
		t.Fatalf("locate make-icon.py: %v", err)
	}

	out := t.TempDir()
	if output, err := exec.Command(python, script, out).CombinedOutput(); err != nil {
		t.Skipf("make-icon.py did not run: %v\n%s", err, output)
	}

	const size = 64
	file, err := filepath.Glob(filepath.Join(out, "icon-64.png"))
	if err != nil || len(file) != 1 {
		t.Fatalf("make-icon.py wrote no icon-64.png (%v)", err)
	}
	// NRGBA on both sides: At().RGBA() would premultiply the script's pixels and
	// leave the Go ones alone, so every antialiased edge would look like a
	// mismatch that is not one.
	fromScript, ok := decodePNG(t, file[0]).(*image.NRGBA)
	if !ok {
		t.Fatal("make-icon.py did not produce a non-premultiplied RGBA png")
	}
	fromGo := renderIcon(size)

	differing := 0
	worst := 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			a := fromGo.NRGBAAt(x, y)
			b2 := fromScript.NRGBAAt(x, y)
			for _, pair := range [][2]int{
				{int(a.R), int(b2.R)}, {int(a.G), int(b2.G)},
				{int(a.B), int(b2.B)}, {int(a.A), int(b2.A)},
			} {
				delta := pair[0] - pair[1]
				if delta < 0 {
					delta = -delta
				}
				if delta > 0 {
					differing++
					if delta > worst {
						worst = delta
					}
				}
			}
		}
	}

	// Rounding differs by at most one step between Python's round-half-even and
	// Go's round-half-away-from-zero. Anything larger means the drawing changed.
	if worst > 1 {
		t.Errorf("the Go icon and make-icon.py disagree by up to %d per channel "+
			"(%d channels differ); one of them was changed without the other",
			worst, differing)
	}
}

func decodePNG(t *testing.T, path string) image.Image {
	t.Helper()
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	decoded, err := png.Decode(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return decoded
}
