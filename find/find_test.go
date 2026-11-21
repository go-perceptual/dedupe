package find

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-perceptual/tmk"
)

// TestFindOnTheCommittedFixtures uses Find as a library on testdata: the four
// encodings of one melody are one recording, the three scales of one clip are
// one video, the unrelated clip stays alone, and the VP9 file that cannot be
// decoded is REPORTED rather than swallowed.
func TestFindOnTheCommittedFixtures(t *testing.T) {
	res, err := Find(context.Background(), []string{"../testdata"}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if res.Roots != 1 || res.Walked != 9 {
		t.Errorf("Roots=%d Walked=%d, want 1 and 9", res.Roots, res.Walked)
	}
	if res.Recordings != 4 || res.Videos != 5 || res.Images != 0 || res.NotMedia != 0 {
		t.Errorf("classified %d image(s), %d recording(s), %d video(s), %d other",
			res.Images, res.Recordings, res.Videos, res.NotMedia)
	}
	if len(res.Exact) != 0 || len(res.Near) != 0 {
		t.Errorf("unexpected exact/near groups: %v %v", res.Exact, res.Near)
	}
	if len(res.Audio) != 1 || len(res.Audio[0].Files) != 4 {
		t.Fatalf("audio groups = %+v, want one group of four", res.Audio)
	}
	if len(res.Video) != 1 || len(res.Video[0].Files) != 3 {
		t.Fatalf("video groups = %+v, want one group of three", res.Video)
	}
	if res.Audio[0].Score < DefaultOptions().AudioThreshold || res.Video[0].Score < DefaultOptions().VideoThreshold {
		t.Errorf("a group's weakest pair is below its threshold: %v %v", res.Audio[0].Score, res.Video[0].Score)
	}
	if len(res.ReadErrors) != 1 {
		t.Errorf("ReadErrors = %v, want exactly the unsupported VP9 file", res.ReadErrors)
	}
	if _, ok := res.Hashed[KindImage]; ok {
		t.Error("the image pass ran with no image in the tree")
	}
	if res.Hashed[KindAudio] != 4 || res.Hashed[KindVideo] != 5-len(res.ReadErrors) {
		t.Errorf("Hashed = %v", res.Hashed)
	}
}

// ExampleFind shows the call a program with a window makes.
func ExampleFind() {
	o := DefaultOptions()
	o.ExactOnly = true
	res, err := Find(context.Background(), []string{"../testdata"}, o)
	if err != nil {
		panic(err)
	}
	var recoverable int64
	for _, g := range res.Exact {
		recoverable += g.Recoverable()
	}
	fmt.Printf("walked %d file(s), recoverable %d\n", res.Walked, recoverable)
	// Output: walked 9 file(s), recoverable 0
}

func TestDefaultOptionsAreValidAndMatchTheCommand(t *testing.T) {
	o := DefaultOptions()
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	want := Options{
		Threshold: 20, MinQuality: 50, MinSize: 1024,
		AudioThreshold: 0.9, VideoThreshold: tmk.DefaultThreshold,
		AudioSeconds: 180, VideoSeconds: 180,
	}
	if o != want {
		t.Errorf("DefaultOptions = %+v, want %+v", o, want)
	}
	if err := (Options{}).Validate(); err == nil {
		t.Error("the zero Options was accepted, but AudioSeconds 0 must be refused")
	}
}

func TestValidateRefusesEachOutOfRangeOption(t *testing.T) {
	for name, mutate := range map[string]func(*Options){
		"threshold low":  func(o *Options) { o.Threshold = -1 },
		"threshold high": func(o *Options) { o.Threshold = 257 },
		"quality low":    func(o *Options) { o.MinQuality = -1 },
		"quality high":   func(o *Options) { o.MinQuality = 101 },
		"audio low":      func(o *Options) { o.AudioThreshold = -0.1 },
		"audio high":     func(o *Options) { o.AudioThreshold = 1.1 },
		"video low":      func(o *Options) { o.VideoThreshold = -0.1 },
		"video high":     func(o *Options) { o.VideoThreshold = 1.1 },
		"audio seconds":  func(o *Options) { o.AudioSeconds = 0 },
		"video seconds":  func(o *Options) { o.VideoSeconds = 0 },
		"only":           func(o *Options) { o.Only = "other" },
	} {
		o := DefaultOptions()
		mutate(&o)
		if err := o.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, o)
		}
	}
}

func TestFindRefusesWhatItCannotScan(t *testing.T) {
	bad := DefaultOptions()
	bad.Only = "pictures"
	if _, err := Find(context.Background(), []string{"../testdata"}, bad); err == nil {
		t.Error("Only=pictures was accepted")
	}
	if _, err := Find(context.Background(), []string{filepath.Join(t.TempDir(), "absent")}, DefaultOptions()); err == nil {
		t.Error("a missing root was accepted")
	}
}

// TestFindHonoursACancelledContext: a cancelled scan is an ERROR, not a short
// list — a half-scanned tree would read as "fewer duplicates".
func TestFindHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Find(ctx, []string{"../testdata"}, DefaultOptions())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Walked != 0 || len(res.Audio) != 0 || len(res.Video) != 0 {
		t.Errorf("a cancelled scan returned a partial result: %+v", res)
	}
}

// TestCancellationStopsBetweenFiles drives each worker pool with a cancelled
// context and a long list: none of the files may be read.
func TestCancellationStopsBetweenFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var files []File
	for i := 0; i < 500; i++ {
		files = append(files, File{Path: fixtures + "melody.mp3", Media: KindAudio})
	}
	media, other, errs := classifyAll(ctx, files, 4)
	if n := len(media) + other + len(errs); n != 0 {
		t.Errorf("classifyAll read %d file(s) after cancellation", n)
	}
	if hs, skipped, errs := hashAudio(ctx, files, 10, 4); len(hs)+skipped+len(errs) != 0 {
		t.Errorf("hashAudio read %d file(s) after cancellation", len(hs)+skipped+len(errs))
	}
	if hs, skipped, errs := hashVideos(ctx, files, 10, 4); len(hs)+skipped+len(errs) != 0 {
		t.Errorf("hashVideos read %d file(s) after cancellation", len(hs)+skipped+len(errs))
	}
	if hs, skipped, errs := hashAll(ctx, files, 0, 4); len(hs)+skipped+len(errs) != 0 {
		t.Errorf("hashAll read %d file(s) after cancellation", len(hs)+skipped+len(errs))
	}
}

// TestACancelledScanStopsMidway cancels while files are being read and checks
// the scan ends early instead of finishing the tree.
func TestACancelledScanStopsMidway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var files []File
	for i := 0; i < 300; i++ {
		files = append(files, File{Path: fixtures + "melody.mp3", Media: KindAudio})
	}
	done := make(chan int)
	go func() {
		hs, s, e := hashAudio(ctx, files, 10, 1)
		done <- len(hs) + s + len(e)
	}()
	cancel()
	if n := <-done; n >= len(files) {
		t.Errorf("hashAudio read all %d files despite cancellation", n)
	}
}

// TestATruncatedImageCannotBeHashed is the premise the command's INCOMPLETE
// test stands on: the header classifies as an image, the pixels do not decode.
func TestATruncatedImageCannotBeHashed(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{uint8(x*7 ^ y*3), uint8(y * 5), uint8((x ^ y) * 11), 255})
		}
	}
	whole := filepath.Join(dir, "whole.png")
	f, err := os.Create(whole)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	data, err := os.ReadFile(whole)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(dir, "half.png")
	if err := os.WriteFile(truncated, data[:len(data)/3], 0o644); err != nil {
		t.Fatal(err)
	}
	if k, err := classify(truncated); err != nil || k != KindImage {
		t.Fatalf("classify = %q, %v; want an image with a readable header", k, err)
	}
	if _, err := perceptual(truncated, 0); err == nil {
		t.Fatal("the truncated file decoded fully")
	}
}
