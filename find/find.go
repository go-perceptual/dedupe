// Package find finds duplicate and near-duplicate media in a directory tree.
//
// It is the grouping behind the dedupe command, importable so a program with
// a window can mark duplicate files without running the command.
//
// Every file is classified by READING it, never by its extension. Then four
// passes run: byte-identical files, and, for content that is the same although
// the bytes are not, pictures (PDQ), recordings (chroma) and videos
// (TMK+PDQF).
//
// ⛔ It reports. It does not delete, move or link anything, and no option
// makes it: the perceptual passes are HEURISTICS with documented
// false-positive modes, and a list for a person to act on is the contract.
package find

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/go-perceptual/pdq"
	"github.com/go-perceptual/tmk"
)

// Options configures Find. DefaultOptions returns the values the dedupe
// command uses when no flag is given.
//
// ⛔ The zero Options is NOT the default: a zero MinSize, Threshold or
// AudioSeconds means exactly that (and AudioSeconds 0 is refused). Start from
// DefaultOptions.
type Options struct {
	// Threshold is the largest PDQ distance still called the same picture
	// (0-256).
	Threshold int
	// MinQuality ignores images whose PDQ quality is below it (0-100).
	MinQuality pdq.Quality
	// MinSize ignores files smaller than this many bytes.
	MinSize int64
	// Workers is the number of parallel hashers; 0 means one per CPU.
	Workers int
	// ExactOnly reports only byte-identical files, skipping every perceptual
	// pass.
	ExactOnly bool
	// AudioThreshold is the chroma score at or above which two recordings are
	// the same (0-1).
	AudioThreshold float64
	// VideoThreshold is the TMK score at or above which two videos are the
	// same (0-1).
	VideoThreshold float64
	// AudioSeconds bounds how many seconds of each recording are compared
	// (at least 1).
	AudioSeconds int
	// VideoSeconds bounds how many seconds of each video are compared (at
	// least 1).
	VideoSeconds int
	// Only runs one perceptual pass: KindImage, KindAudio or KindVideo. Empty
	// runs all three.
	Only Kind
}

// DefaultOptions returns the options the dedupe command runs with when given
// no flag.
func DefaultOptions() Options {
	return Options{
		Threshold:      20,
		MinQuality:     50,
		MinSize:        1024,
		AudioThreshold: AudioThreshold,
		VideoThreshold: tmk.DefaultThreshold,
		AudioSeconds:   AudioSecondsDefault,
		VideoSeconds:   VideoSecondsDefault,
	}
}

// Validate reports an Options value Find would refuse.
func (o Options) Validate() error {
	switch {
	case o.Threshold < 0 || o.Threshold > 256:
		return fmt.Errorf("Threshold must be between 0 and 256, got %d", o.Threshold)
	case o.MinQuality < 0 || o.MinQuality > 100:
		return fmt.Errorf("MinQuality must be between 0 and 100, got %d", o.MinQuality)
	case o.AudioThreshold < 0 || o.AudioThreshold > 1:
		return fmt.Errorf("AudioThreshold must be between 0 and 1, got %v", o.AudioThreshold)
	case o.VideoThreshold < 0 || o.VideoThreshold > 1:
		return fmt.Errorf("VideoThreshold must be between 0 and 1, got %v", o.VideoThreshold)
	case o.AudioSeconds < 1:
		return fmt.Errorf("AudioSeconds must be at least 1, got %d", o.AudioSeconds)
	case o.VideoSeconds < 1:
		return fmt.Errorf("VideoSeconds must be at least 1, got %d", o.VideoSeconds)
	}
	switch o.Only {
	case "", KindImage, KindAudio, KindVideo:
	default:
		return fmt.Errorf("Only must be image, audio or video, got %q", o.Only)
	}
	return nil
}

func (o Options) wants(k Kind) bool { return o.Only == "" || o.Only == k }

// Result is what Find found, with the volume it read beside it.
//
// ⛔ The counts and the errors are part of the answer. A scan that found
// nothing and a scan that read nothing both have empty groups; only Walked,
// the classified counts and the errors tell them apart, and a non-empty
// WalkErrors or ReadErrors means the groups are INCOMPLETE.
type Result struct {
	// Roots is the number of roots scanned.
	Roots int
	// Walked is the number of candidate files found under the roots.
	Walked int
	// Images, Recordings and Videos count the files classified by content;
	// NotMedia counts those that were none of the three.
	Images, Recordings, Videos, NotMedia int

	// Exact holds files with identical bytes; Near, Audio and Video the three
	// perceptual passes. Each is sorted by recoverable bytes, largest first.
	Exact, Near, Audio, Video []Group

	// Hashed has an entry for every perceptual pass that ran (KindImage,
	// KindAudio, KindVideo): the number of files it hashed.
	Hashed map[Kind]int
	// Skipped counts images too featureless to match on (below MinQuality).
	Skipped int

	// WalkErrors are directories or files the walk could not read; ReadErrors
	// are files that could not be classified or hashed.
	WalkErrors, ReadErrors []error
}

// Find scans roots and groups the duplicates.
//
// It returns an error when a root does not exist, when o is invalid, or when
// ctx is cancelled: cancellation is honoured between files, and the partial
// result is discarded rather than returned, since a half-scanned tree reads as
// "fewer duplicates". Files that cannot be read are NOT an error: they are
// listed in the Result.
func Find(ctx context.Context, roots []string, o Options) (Result, error) {
	var res Result
	if err := o.Validate(); err != nil {
		return res, err
	}
	for _, r := range roots {
		if _, err := os.Stat(r); err != nil {
			return res, err
		}
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}

	res.Roots = len(roots)
	found, walkErrs := walk(roots, o.MinSize)
	res.Walked = len(found)
	res.WalkErrors = walkErrs
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if len(found) == 0 {
		return res, nil
	}

	files, other, classErrs := classifyAll(ctx, found, o.Workers)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	images, audio, video := byKind(files)
	res.Images, res.Recordings, res.Videos, res.NotMedia = len(images), len(audio), len(video), other
	res.ReadErrors = classErrs
	if len(files) == 0 {
		return res, nil
	}

	exact, claimed, exactErrs := exactGroups(files)
	for i := range exact {
		sortGroup(&exact[i])
	}
	sort.Slice(exact, func(i, j int) bool {
		return exact[i].Recoverable() > exact[j].Recoverable()
	})
	res.Exact = exact
	res.ReadErrors = append(append([]error{}, classErrs...), exactErrs...)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if o.ExactOnly {
		return res, nil
	}
	res.Hashed = map[Kind]int{}
	if o.wants(KindImage) && len(images) > 0 {
		hs, s, errs := hashAll(ctx, images, o.MinQuality, o.Workers)
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		res.Skipped += s
		res.ReadErrors = append(res.ReadErrors, errs...)
		res.Hashed[KindImage] = len(hs)
		res.Near = nearGroups(hs, o.Threshold, claimed)
	}
	if o.wants(KindAudio) && len(audio) > 0 {
		hs, s, errs := hashAudio(ctx, audio, o.AudioSeconds, o.Workers)
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		res.Skipped += s
		res.ReadErrors = append(res.ReadErrors, errs...)
		res.Hashed[KindAudio] = len(hs)
		res.Audio = audioGroups(hs, o.AudioThreshold, claimed)
	}
	if o.wants(KindVideo) && len(video) > 0 {
		hs, s, errs := hashVideos(ctx, video, o.VideoSeconds, o.Workers)
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		res.Skipped += s
		res.ReadErrors = append(res.ReadErrors, errs...)
		res.Hashed[KindVideo] = len(hs)
		g, errs2 := videoGroups(ctx, hs, o.VideoThreshold, o.VideoSeconds, claimed)
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		res.Video = g
		res.ReadErrors = append(res.ReadErrors, errs2...)
	}
	return res, nil
}

// feed sends files to in until all are sent or ctx is cancelled, whichever
// comes first. The workers drain what was already sent and stop.
func feed(ctx context.Context, in chan<- File, files []File) {
	for _, f := range files {
		// Checked first: select picks at random among ready cases, so a
		// cancelled context would otherwise still let files through.
		if ctx.Err() != nil {
			return
		}
		select {
		case in <- f:
		case <-ctx.Done():
			return
		}
	}
}
