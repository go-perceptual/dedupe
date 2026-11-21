package find

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"

	"github.com/go-perceptual/chroma"
	"github.com/go-perceptual/media"
)

// AudioThreshold is the chroma score at or above which two recordings are
// reported as the same.
//
// ⛔ 0.9, and the number was MEASURED rather than chosen — see
// go-perceptual/perceptual, where the same eight-chord melody played
// BACKWARDS scores 0.7064, and with only four chords 0.8176. A chroma feature
// is a profile of which pitch classes are present, so two recordings built
// from the same notes agree substantially even when they are not the same
// piece. The natural-looking 0.7 admits a reversed recording.
const AudioThreshold = 0.9

// AudioSecondsDefault bounds how much of each recording is compared.
//
// ⛔ A bound is necessary, not a preference. Chroma features are roughly 97
// float64 a second (8.077 frames of 12 bands), so a five-minute track is
// about 230 KB and a tree of two thousand tracks would be 460 MB held at
// once, before any comparison starts. The tool would be killed by the kernel
// rather than report anything.
//
// ⚠ What this costs, stated rather than hidden: two recordings that differ
// only AFTER the window are reported as the same, and a clip taken from
// beyond the window does not match its source. Raise --audio-seconds when
// that matters; the memory grows linearly with it.
const AudioSecondsDefault = 180

// audioFeature is one recording's chroma profile.
type audioFeature struct {
	File
	features [][]float64
}

// audioFeatures returns one file's chroma features.
//
// The decoding, downmixing and resampling are go-perceptual/media's — see
// that package for why each step is the way it is, and why none of them
// should exist in two places. What stays here is which feature, and the bound.
func audioFeatures(path string, maxSeconds int) ([][]float64, error) {
	// ⛔ chroma.SampleRate, not a number. The rate is part of the FEATURE: the
	// frame geometry and the frequency bounds are expressed in samples at it,
	// so audio fed at another rate produces a perfectly plausible fingerprint
	// that matches nothing.
	pcm, err := media.Mono(path, chroma.SampleRate, maxSeconds)
	if err != nil {
		return nil, err
	}
	features, err := chroma.Features(pcm)
	if err != nil {
		return nil, err
	}
	if len(features) == 0 {
		return nil, media.ErrNoSamples
	}
	return features, nil
}

// hashAudio computes the chroma features of every audio file, in parallel.
func hashAudio(ctx context.Context, files []File, maxSeconds, workers int) ([]audioFeature, int, []error) {
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	type outcome struct {
		f       audioFeature
		skipped bool
		err     error
	}
	in := make(chan File)
	out := make(chan outcome)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range in {
				feat, err := audioFeatures(f.Path, maxSeconds)
				switch {
				// ⛔ A track too short to fingerprint is a SKIP, counted and
				// reported, not a failure: a one-second notification sound is
				// not a broken file. A file that could not be READ is a
				// failure, and the report says INCOMPLETE for it. Collapsing
				// the two would either cry wolf over every tree or hide a
				// genuinely unreadable file among the skips.
				case errors.Is(err, media.ErrNoSamples):
					out <- outcome{skipped: true}
				case err != nil:
					out <- outcome{err: fmt.Errorf("%s: %w", f.Path, err)}
				default:
					out <- outcome{f: audioFeature{File: f, features: feat}}
				}
			}
		}()
	}
	go func() {
		feed(ctx, in, files)
		close(in)
		wg.Wait()
		close(out)
	}()

	var hs []audioFeature
	var errs []error
	skipped := 0
	for o := range out {
		switch {
		case o.skipped:
			skipped++
		case o.err != nil:
			errs = append(errs, o.err)
		default:
			hs = append(hs, o.f)
		}
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].Path < hs[j].Path })
	return hs, skipped, errs
}

// audioGroups clusters recordings that score at or above threshold.
//
// Single-linkage, like the other passes: see unionFind for the consequence.
// The group reports the LOWEST pairwise score inside it, so a stretched chain
// is visible rather than hidden.
func audioGroups(hs []audioFeature, threshold float64, claimed map[string]bool) []Group {
	var todo []audioFeature
	for _, h := range hs {
		if !claimed[h.Path] {
			todo = append(todo, h)
		}
	}

	uf := newUnionFind(len(todo))
	for i := 0; i < len(todo); i++ {
		for j := i + 1; j < len(todo); j++ {
			if chroma.Compare(todo[i].features, todo[j].features) >= threshold {
				uf.union(i, j)
			}
		}
	}

	var groups []Group
	for _, idx := range uf.groups() {
		g := Group{Kind: "audio"}
		for _, i := range idx {
			g.Files = append(g.Files, todo[i].File)
		}
		worst := 1.0
		for a := 0; a < len(idx); a++ {
			for b := a + 1; b < len(idx); b++ {
				if s := chroma.Compare(todo[idx[a]].features, todo[idx[b]].features); s < worst {
					worst = s
				}
			}
		}
		g.Score = worst
		sortGroup(&g)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Recoverable() > groups[j].Recoverable()
	})
	return groups
}
