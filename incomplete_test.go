package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-perceptual/media"
)

// TestATruncatedImageMakesTheReportSayIncomplete is the guard on the warning
// that matters most. A scan that could not read part of what it was given and
// prints a clean list is indistinguishable from a scan that found nothing —
// and the second reading is the one a person makes.
//
// ⛔ The fixture is a TRUNCATED but valid PNG, and the reason is a regression
// this test caught. It used to be a file called `broken.png` holding no PNG at
// all, which worked while the walk trusted extensions: the name got it in, and
// the decoder found out. Classifying by CONTENT made that file genuinely "not
// media" — correctly — so the old fixture stopped exercising the path and the
// warning went untested.
//
// A truncated PNG is the case that still reaches it: the header is real, so
// image.DecodeConfig accepts it during classification and the file is an
// image; the pixel data is missing, so the hash pass fails. That is what a
// half-finished download actually is.
func TestATruncatedImageMakesTheReportSayIncomplete(t *testing.T) {
	dir := t.TempDir()
	whole := filepath.Join(dir, "whole.png")
	writePNG(t, whole, picture(200, 200, 7))
	data, err := os.ReadFile(whole)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(whole); err != nil {
		t.Fatal(err)
	}

	// Keep the signature and IHDR, drop the image data. Checked rather than
	// assumed below: a cut in the wrong place gives a file that either
	// decodes fine or is not an image at all, and both would make this test
	// pass for the wrong reason.
	cut := len(data) / 3
	truncated := filepath.Join(dir, "half-downloaded.png")
	if err := os.WriteFile(truncated, data[:cut], 0o644); err != nil {
		t.Fatal(err)
	}

	kind, err := media.Classify(truncated)
	if err != nil {
		t.Fatalf("classifying the truncated file: %v", err)
	}
	if kind != media.KindImage {
		t.Fatalf("the truncated file classified as %q, so this test is not staging a readable header", kind)
	}
	// (That its pixels cannot be hashed is asserted in find, beside perceptual;
	// here the report's INCOMPLETE line is the proof.)

	// A second image, so the pass has something to do besides fail.
	writePNG(t, filepath.Join(dir, "fine.png"), picture(200, 200, 9))

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

// TestFilesThatAreNotMediaAreCountedNotSwallowed guards the bucket that
// classifying by content created.
//
// ⛔ Dropping the extension filter means every file in the tree is now opened
// and most are discarded. That discard pile is the new place for a silent
// failure to hide: a classifier that broke and called everything "not media"
// would print an empty report, which reads exactly like a clean tree. The
// count is therefore printed, and this test is what keeps it printed.
func TestFilesThatAreNotMediaAreCountedNotSwallowed(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"notes.txt", "archive.zip", "script.sh"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", 4096)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writePNG(t, filepath.Join(dir, "real.png"), picture(200, 200, 4))

	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "walked 4 file(s)") {
		t.Fatalf("the walk did not report all four files:\n%s", out)
	}
	if !strings.Contains(out, "3 not media") {
		t.Fatalf("the three non-media files were not counted:\n%s", out)
	}
	if !strings.Contains(out, "1 image(s)") {
		t.Fatalf("the one real image was not classified:\n%s", out)
	}
	// ⛔ And they produce no error: a text file is not a failure to read, and
	// reporting it as one would cry INCOMPLETE over every tree.
	if strings.Contains(out, "INCOMPLETE") {
		t.Fatalf("ordinary non-media files were reported as unreadable:\n%s", out)
	}
}

// TestAMediaFileWithALyingExtensionIsStillFound is the whole point of
// classifying by content.
func TestAMediaFileWithALyingExtensionIsStillFound(t *testing.T) {
	dir := t.TempDir()
	img := picture(300, 300, 11)
	writePNG(t, filepath.Join(dir, "honest.png"), img)

	// The same picture, byte for byte, called something else entirely.
	data, err := os.ReadFile(filepath.Join(dir, "honest.png"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"backup.dat", "no-extension", "holiday.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "4 image(s)") {
		t.Fatalf("renamed images were not classified as images:\n%s", out)
	}
	for _, name := range []string{"backup.dat", "no-extension", "holiday.txt"} {
		if !strings.Contains(out, name) {
			t.Errorf("%s was not reported as a duplicate:\n%s", name, out)
		}
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
//
// ⛔ It CHECKS its own premise instead of predicting which platforms honour
// it. `chmod 000` makes a directory unenterable on Unix as an ordinary user;
// it does nothing of the sort for root, and on Windows os.Chmod only toggles
// the read-only attribute, which does not block traversal at all. Guarding on
// os.Geteuid() missed Windows entirely — it answers -1 there — and the test
// went red on one platform out of three.
//
// So the mechanism is applied and then TESTED: if the directory is still
// readable afterwards, this platform cannot stage the condition and there is
// nothing to assert. A test that declares what it needs and measures whether
// it got it cannot pass for the wrong reason on a platform nobody listed.
func TestADirectoryItCannotEnterIsReported(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(locked, "x.png"), picture(120, 90, 3))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("cannot change the mode of a directory here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("this platform still reads a directory after chmod 000, so an unreadable directory cannot be staged here")
	}

	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "INCOMPLETE") {
		t.Fatalf("a directory that could not be entered was not reported:\n%s", out)
	}
}
