package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-perceptual/dedupe/find"
)

// picture is a deterministic image with enough structure that PDQ scores it
// well and no two coefficients tie.
func picture(w, h, seed int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				uint8((x*7 + seed*31) ^ (y * 3)),
				uint8((y*5 + seed*17) % 251),
				uint8((x ^ y) * 11),
				255,
			})
		}
	}
	return img
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func writeJPEG(t *testing.T, path string, img image.Image, quality int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// tree builds a corpus with one of each case the tool must tell apart.
func tree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	img := picture(200, 150, 1)

	// Byte-identical pair.
	writePNG(t, filepath.Join(dir, "a.png"), img)
	copyFile(t, filepath.Join(dir, "a.png"), filepath.Join(dir, "a-copy.png"))

	// Same picture, different bytes: a JPEG of the same image, in a
	// subdirectory so the walk has to descend.
	sub := filepath.Join(dir, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJPEG(t, filepath.Join(sub, "a.jpg"), img, 85)

	// A different picture entirely.
	writePNG(t, filepath.Join(dir, "other.png"), picture(200, 150, 9))

	return dir
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := RootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestFindsIdenticalBytes(t *testing.T) {
	dir := tree(t)
	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "IDENTICAL BYTES") {
		t.Fatalf("the identical pair was not reported:\n%s", out)
	}
	if !strings.Contains(out, "a-copy.png") {
		t.Fatalf("the copy is not named:\n%s", out)
	}
}

// TestFindsTheSamePictureInDifferentBytes is the whole point of the tool: a
// JPEG re-encoding of a PNG shares no bytes at all, so only the perceptual
// pass can see it.
func TestFindsTheSamePictureInDifferentBytes(t *testing.T) {
	dir := tree(t)
	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "SAME PICTURE, DIFFERENT BYTES") {
		t.Fatalf("the re-encoded copy was not found:\n%s", out)
	}
	if !strings.Contains(out, "a.jpg") {
		t.Fatalf("the jpeg is not named:\n%s", out)
	}
}

// TestLeavesUnrelatedPicturesAlone is the control: a tool that grouped
// everything would pass both tests above.
func TestLeavesUnrelatedPicturesAlone(t *testing.T) {
	dir := tree(t)
	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// other.png must not appear in any group.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "other.png") && (strings.Contains(line, "keep") || strings.Contains(line, "dup")) {
			t.Fatalf("an unrelated picture was grouped:\n%s", out)
		}
	}
}

func TestExactOnlySkipsThePerceptualPass(t *testing.T) {
	dir := tree(t)
	out, err := runCLI(t, dir, "--exact-only")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "SAME PICTURE") {
		t.Fatalf("--exact-only still ran the perceptual pass:\n%s", out)
	}
	if !strings.Contains(out, "IDENTICAL BYTES") {
		t.Fatalf("--exact-only lost the exact pass too:\n%s", out)
	}
}

// TestReportsTheVolumeScanned pins the line that tells "found nothing" apart
// from "read nothing".
func TestReportsTheVolumeScanned(t *testing.T) {
	out, err := runCLI(t, t.TempDir())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "walked 0 file(s)") {
		t.Fatalf("an empty tree did not say how much it read:\n%s", out)
	}
}

// TestRecoverableCountsAllButOne is the arithmetic the whole report rests on.
func TestRecoverableCountsAllButOne(t *testing.T) {
	g := Group{Files: []find.File{{Size: 100}, {Size: 90}, {Size: 80}}}
	if got := g.Recoverable(); got != 170 {
		t.Fatalf("Recoverable = %d, want 170 (90+80, keeping the first)", got)
	}
	one := Group{Files: []find.File{{Size: 100}}}
	if got := one.Recoverable(); got != 0 {
		t.Fatalf("a group of one frees %d, want 0", got)
	}
}

func TestRejectsImpossibleFlags(t *testing.T) {
	dir := t.TempDir()
	if _, err := runCLI(t, dir, "--threshold", "999"); err == nil {
		t.Error("a threshold above 256 was accepted")
	}
	if _, err := runCLI(t, dir, "--min-quality", "-1"); err == nil {
		t.Error("a negative quality was accepted")
	}
}

func TestMissingDirectoryIsAnError(t *testing.T) {
	if _, err := runCLI(t, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing directory was accepted")
	}
}

// TestTheSameFileReachedTwiceIsOneFile: passing a directory and its child
// must not make every file its own duplicate.
func TestTheSameFileReachedTwiceIsOneFile(t *testing.T) {
	dir := tree(t)
	out, err := runCLI(t, dir, filepath.Join(dir, "nested"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "nested/a.jpg (") && strings.Count(out, "nested/a.jpg") > 1 {
		t.Fatalf("a file reached through two roots was counted twice:\n%s", out)
	}
}

// TestMinSizeSkipsSmallFiles guards the flag that keeps a thumbnail cache
// from dominating the report.
func TestMinSizeSkipsSmallFiles(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "tiny.png"), picture(4, 4, 1))
	out, err := runCLI(t, dir, "--min-size", "100000")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "walked 0 file(s)") {
		t.Fatalf("--min-size did not skip a small file:\n%s", out)
	}
}

func TestBytesOf(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{512, "512 B"}, {1024, "1.0 KB"}, {1536, "1.5 KB"}, {1 << 20, "1.0 MB"}, {1 << 30, "1.0 GB"},
	}
	for _, c := range cases {
		if got := bytesOf(c.in); got != c.want {
			t.Errorf("bytesOf(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNoDeletionFlagExists is a guard on the contract, not on behaviour: this
// tool reports, and nothing in its interface may offer to act. If someone
// adds --delete, this test is where the decision gets made rather than
// slipping in.
func TestNoDeletionFlagExists(t *testing.T) {
	out, err := runCLI(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"--delete", "--remove", "--link", "--move", "--purge"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("%s appeared in the interface; dedupe reports and does not act:\n%s", forbidden, out)
		}
	}
}
