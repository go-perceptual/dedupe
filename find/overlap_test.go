package find

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// rings is a picture with structure at several scales, so PDQ has something to
// match on and a JPEG of it lands near the original (measured: 18 at quality
// 85 for 320×320).
func rings(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	cx, cy := float64(w)/2, float64(h)/2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := math.Hypot(float64(x)-cx, float64(y)-cy)
			v := (math.Sin(r/5) + 1) / 2 * 220
			img.Set(x, y, color.RGBA{R: uint8(v), G: uint8(x * 255 / w), B: uint8(y*255/h + 40), A: 255})
		}
	}
	return img
}

func save(t *testing.T, path string, enc func(*os.File) error) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := enc(f); err != nil {
		t.Fatal(err)
	}
}

// TestTheKeeperOfAnExactGroupIsAlsoInANearGroup pins what Result.Exact's doc
// says. A file can be in two groups, and a host counting copies per group
// gets 2 or 3 for a picture held four times. Found by consuming Find from a
// file tree that marks duplicates. It also pins exactGroups' reason for
// keeping the first file: without it, the JPEG would have no partner left.
func TestTheKeeperOfAnExactGroupIsAlsoInANearGroup(t *testing.T) {
	dir := t.TempDir()
	img := rings(320, 320)
	for _, n := range []string{"a.png", "b.png"} {
		save(t, filepath.Join(dir, n), func(f *os.File) error { return png.Encode(f, img) })
	}
	save(t, filepath.Join(dir, "c.jpg"), func(f *os.File) error {
		return jpeg.Encode(f, img, &jpeg.Options{Quality: 85})
	})

	o := DefaultOptions()
	o.Only = KindImage
	res, err := Find(context.Background(), []string{dir}, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Exact) != 1 || len(res.Near) != 1 {
		t.Fatalf("Exact=%v Near=%v, want one group each", res.Exact, res.Near)
	}
	names := func(g Group) []string {
		var out []string
		for _, f := range g.Files {
			out = append(out, filepath.Base(f.Path))
		}
		slices.Sort(out)
		return out
	}
	exact, near := names(res.Exact[0]), names(res.Near[0])
	if !slices.Equal(exact, []string{"a.png", "b.png"}) {
		t.Errorf("Exact = %v, want the two identical PNGs", exact)
	}
	keeper := filepath.Base(res.Exact[0].Files[0].Path)
	if !slices.Contains(near, keeper) || !slices.Contains(near, "c.jpg") || len(near) != 2 {
		t.Errorf("Near = %v, want the keeper %s and c.jpg: the passes share the keeper", near, keeper)
	}
}
