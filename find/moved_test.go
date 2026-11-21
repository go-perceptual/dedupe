package find

import (
	"context"
	"errors"
	"image"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-perceptual/chroma"
	"github.com/go-perceptual/media"
	"github.com/go-perceptual/pdq"
	"github.com/go-perceptual/tmk"
)

// TestAnUnreadableRecordingIsAnErrorNotASkip: a file that cannot be read must
// say so, not turn into a quiet "too short".
func TestAnUnreadableRecordingIsAnErrorNotASkip(t *testing.T) {
	_, err := audioFeatures(filepath.Join(t.TempDir(), "absent.mp3"), 10)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want the file's own not-exist error", err)
	}
}

// TestAShortRecordingReportsChromaTooShort: below one window the features are
// refused with chroma's own error, which is not a decode failure.
func TestAShortRecordingReportsChromaTooShort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "short.wav")
	writeWAV(t, p, make([]int16, 100), chroma.SampleRate)
	if _, err := audioFeatures(p, 10); !errors.Is(err, chroma.ErrTooShort) {
		t.Errorf("err = %v, want chroma.ErrTooShort", err)
	}
}

// TestAnUnreadableVideoIsAnErrorNotAnEmptyOne: the decoder's error must come
// back as itself, not as the "no frames" the hasher would report next.
func TestAnUnreadableVideoIsAnErrorNotAnEmptyOne(t *testing.T) {
	_, err := hashVideoFile(filepath.Join(t.TempDir(), "absent.mp4"), 10, false)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want the file's own not-exist error", err)
	}
	if errors.Is(err, media.ErrNoFrames) {
		t.Errorf("a missing file was reported as having no frames: %v", err)
	}
}

func clipHashes(t *testing.T, dir string) []videoHash {
	t.Helper()
	var files []File
	for _, n := range []string{"clip-90p.mp4", "clip-180p.mp4", "clip-360p.mp4"} {
		p := filepath.Join(dir, n)
		copyFixture(t, fixtures+n, p)
		files = append(files, File{Path: p, Media: KindVideo})
	}
	hs, _, errs := hashVideos(context.Background(), files, 10, 2)
	if len(hs) != 3 || len(errs) != 0 {
		t.Fatalf("hashed %d of 3: %v", len(hs), errs)
	}
	return hs
}

// TestAVideoThatVanishesIsReportedOncePerFile: level 2 re-reads each file
// that is in a surviving pair; the re-read, and its failure, are cached per
// file, so the three pairs that touch the missing file report it once.
func TestAVideoThatVanishesIsReportedOncePerFile(t *testing.T) {
	dir := t.TempDir()
	hs := clipHashes(t, dir)
	if err := os.Remove(hs[0].Path); err != nil {
		t.Fatal(err)
	}
	groups, errs := videoGroups(context.Background(), hs, 0.5, 10, nil)
	if len(errs) != 1 {
		t.Errorf("errors = %v, want exactly one for the vanished file", errs)
	}
	for _, g := range groups {
		for _, f := range g.Files {
			if f.Path == hs[0].Path {
				t.Errorf("the vanished file is in a group: %+v", g)
			}
		}
	}
}

// TestVideoGroupsStopOnACancelledContext: no groups from a half-compared set.
func TestVideoGroupsStopOnACancelledContext(t *testing.T) {
	hs := clipHashes(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if g, _ := videoGroups(ctx, hs, 0.5, 10, nil); g != nil {
		t.Errorf("groups from a cancelled comparison: %+v", g)
	}
}

type brokenEntry struct{ fs.DirEntry }

func (brokenEntry) Type() fs.FileMode          { return 0 }
func (brokenEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }

// TestAFileThatVanishesMidWalkIsReported: the entry was listed, then its
// metadata could not be read; that is a reported failure, not a nil dereference.
func TestAFileThatVanishesMidWalkIsReported(t *testing.T) {
	w := &walker{seen: map[string]bool{}}
	if err := w.visit("gone.bin", brokenEntry{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(w.errs) != 1 || !errors.Is(w.errs[0], fs.ErrNotExist) || len(w.out) != 0 {
		t.Errorf("errs=%v out=%v", w.errs, w.out)
	}
}

func TestContentHashReportsWhatItCannotRead(t *testing.T) {
	if _, err := contentHash(filepath.Join(t.TempDir(), "absent")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: err = %v", err)
	}
	if _, err := contentHash(t.TempDir()); err == nil {
		t.Error("a directory was hashed as if it were a file")
	}
}

func TestPerceptualReportsWhatItCannotOpenOrHash(t *testing.T) {
	if _, err := perceptual(filepath.Join(t.TempDir(), "absent.png"), 0); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: err = %v", err)
	}
	if _, err := hashImage(image.NewRGBA(image.Rect(0, 0, 0, 0)), 0); !errors.Is(err, pdq.ErrEmpty) {
		t.Errorf("empty image: err = %v, want pdq.ErrEmpty", err)
	}
}

func TestAHasherFedNothingIsRefused(t *testing.T) {
	if _, err := finishHash(tmk.New(), false); !errors.Is(err, tmk.ErrNoFrames) {
		t.Errorf("err = %v, want tmk.ErrNoFrames", err)
	}
	if err := addFrame(tmk.New(), 0, image.NewRGBA(image.Rect(0, 0, 0, 0))); !errors.Is(err, pdq.ErrEmpty) {
		t.Errorf("empty frame: err = %v, want pdq.ErrEmpty", err)
	}
}
