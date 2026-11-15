package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAFileItCannotDecodeMakesTheReportSayIncomplete is the guard on the
// warning that matters most. A scan that could not read part of what it was
// given and prints a clean list is indistinguishable from a scan that found
// nothing — and the second reading is the one a person makes.
func TestAFileItCannotDecodeMakesTheReportSayIncomplete(t *testing.T) {
	dir := t.TempDir()
	// A file with an image extension and no image in it. The walk accepts it
	// on its name; only the decoder finds out.
	if err := os.WriteFile(filepath.Join(dir, "broken.png"), []byte(strings.Repeat("not a png", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "INCOMPLETE") {
		t.Fatalf("an unreadable file did not make the report say so:\n%s", out)
	}
	if !strings.Contains(out, "1 path(s) could not be read") {
		t.Fatalf("the count of unread paths is missing:\n%s", out)
	}
}

// TestFeaturelessImagesAreSkippedAndCounted: PDQ reports quality precisely
// because a blank picture hashes like every other blank picture. Letting
// those through would turn a cache of empty thumbnails into one enormous
// false duplicate group, so they are skipped — and the count is printed,
// because a silent skip is a lie by omission.
func TestFeaturelessImagesAreSkippedAndCounted(t *testing.T) {
	dir := t.TempDir()
	blank := image.NewRGBA(image.Rect(0, 0, 300, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 300; x++ {
			blank.Set(x, y, color.RGBA{128, 128, 128, 255})
		}
	}
	writePNG(t, filepath.Join(dir, "blank-1.png"), blank)
	writePNG(t, filepath.Join(dir, "blank-2.png"), blank)
	// Not byte-identical: same flat colour, different size, so the exact pass
	// cannot claim them and only the quality floor can keep them apart.
	big := image.NewRGBA(image.Rect(0, 0, 301, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 301; x++ {
			big.Set(x, y, color.RGBA{128, 128, 128, 255})
		}
	}
	writePNG(t, filepath.Join(dir, "blank-3.png"), big)

	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "skipped as too featureless") {
		t.Fatalf("featureless images were not reported as skipped:\n%s", out)
	}
	if strings.Contains(out, "SAME PICTURE") {
		t.Fatalf("blank images were grouped as duplicates of one another:\n%s", out)
	}
}

// TestLoweringTheQualityFloorLetsThemThrough is the positive control for the
// test above: it proves the floor is what skipped them, not some other
// accident of the corpus.
func TestLoweringTheQualityFloorLetsThemThrough(t *testing.T) {
	dir := t.TempDir()
	blank := image.NewRGBA(image.Rect(0, 0, 300, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 300; x++ {
			blank.Set(x, y, color.RGBA{128, 128, 128, 255})
		}
	}
	writePNG(t, filepath.Join(dir, "blank-1.png"), blank)
	big := image.NewRGBA(image.Rect(0, 0, 301, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 301; x++ {
			big.Set(x, y, color.RGBA{128, 128, 128, 255})
		}
	}
	writePNG(t, filepath.Join(dir, "blank-2.png"), big)

	out, err := runCLI(t, dir, "--min-quality", "0")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "skipped as too featureless") {
		t.Fatalf("--min-quality 0 still skipped images:\n%s", out)
	}
}

// TestADirectoryItCannotEnterIsReported covers the walk's own error path.
// It is skipped when the process can read anything regardless of mode, which
// is the case for root: asserting an EACCES that cannot happen would be a
// test that passes for the wrong reason.
func TestADirectoryItCannotEnterIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable directory is still readable, so this asserts nothing")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(locked, "x.png"), picture(120, 90, 3))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("cannot make a directory unreadable here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "INCOMPLETE") {
		t.Fatalf("a directory that could not be entered was not reported:\n%s", out)
	}
}
