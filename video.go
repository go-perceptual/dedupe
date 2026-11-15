package main

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"sort"
	"sync"

	"github.com/go-perceptual/media"
	"github.com/go-perceptual/pdq"
	"github.com/go-perceptual/tmk"
)

// Level1Gate is the level-1 score a pair must reach before level 2 is
// computed for it.
//
// ⛔ This is a PREFILTER, and it is deliberately looser than the decision
// threshold. Level 1 is the mean frame and carries no timing at all, so it
// cannot separate a film from the same film re-cut — but it will separate a
// film from an unrelated one, cheaply, and that is all a prefilter has to do.
// Set it at the decision threshold and the prefilter starts making the
// decision, discarding pairs level 2 would have confirmed.
//
// ⚠ It is a real recall risk and not a free optimisation: a pair that level 1
// scores below this is never looked at again. 0.5 is half way between the 0.7
// decision and the 0.0 of unrelated material. Measured on the fixtures: a
// clip scores 0.9851 against its half-scale copy and 0.9881 against its
// quarter-scale one, while an unrelated clip scores -0.0829.
const Level1Gate = 0.5

// videoSecondsDefault bounds how much of each video is hashed.
//
// ⛔ Bounded for TIME, not memory: the level-1 sweep already holds only a
// kilobyte a video, but DECODING is the expensive part — every frame of every
// video is resampled to 15 fps and put through PDQ. An unbounded pass over a
// folder of feature films decodes for hours before printing anything.
//
// ⚠ What it costs: two videos that differ only after the window are reported
// as the same. Three minutes is enough to separate different films and short
// enough to keep a scan interactive.
const videoSecondsDefault = 180

// videoHash is one video's signature.
type videoHash struct {
	File
	hash *tmk.Hash
}

// hashVideoFile decodes a file and hashes it.
//
// Decoding and the resampling to tmk.FPS are go-perceptual/media's — including
// the part that is easy to get wrong and impossible to notice: frames are
// resampled BY TIME, not by taking every nth frame, so a 30 fps and a 25 fps
// copy of one film land on the same indices.
//
// level2 says whether the level-2 features are kept. ⛔ They are 262 KB per
// video against 1 KB for level 1, so a tree of five hundred videos is 131 MB
// of level-2 features held at once, before any comparison. TMK's own
// documentation calls the level-1 feature "what a large index stores", and
// this is that design used as intended: sweep on level 1, and pay for level 2
// only on the pairs that survive. The cost is decoding a candidate twice,
// which is the right trade — a tree where few videos match barely pays it.
func hashVideoFile(path string, maxSeconds int, level2 bool) (*tmk.Hash, error) {
	h := tmk.New()
	err := media.Frames(path, tmk.FPS, maxSeconds, func(index int, img image.Image) error {
		feature, _, err := pdq.Float(img)
		if err != nil {
			return err
		}
		// ⛔ The INDEX media hands over, not a counter of our own: tmk.Add
		// takes the frame's position in TIME, and the kernel compares phases
		// at those integer positions.
		h.Add(index, feature)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out, err := h.Finish()
	if err != nil {
		return nil, err
	}
	if !level2 {
		// Cos and Sin are the level-2 features. Releasing them here is what
		// keeps the sweep's memory at a kilobyte a video; Average and the
		// parameters Level1 needs survive.
		out.Cos, out.Sin = nil, nil
	}
	return out, nil
}

// hashVideos computes the level-1 signature of every video, in parallel.
func hashVideos(files []File, maxSeconds, workers int) ([]videoHash, int, []error) {
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	type outcome struct {
		h       videoHash
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
				h, err := hashVideoFile(f.Path, maxSeconds, false)
				switch {
				case errors.Is(err, media.ErrNoFrames):
					out <- outcome{skipped: true}
				case err != nil:
					out <- outcome{err: fmt.Errorf("%s: %w", f.Path, err)}
				default:
					out <- outcome{h: videoHash{File: f, hash: h}}
				}
			}
		}()
	}
	go func() {
		for _, f := range files {
			in <- f
		}
		close(in)
		wg.Wait()
		close(out)
	}()

	var hs []videoHash
	var errs []error
	skipped := 0
	for o := range out {
		switch {
		case o.skipped:
			skipped++
		case o.err != nil:
			errs = append(errs, o.err)
		default:
			hs = append(hs, o.h)
		}
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].Path < hs[j].Path })
	return hs, skipped, errs
}

// videoGroups clusters videos whose level-2 score reaches threshold.
func videoGroups(hs []videoHash, threshold float64, maxSeconds int, claimed map[string]bool) ([]Group, []error) {
	var todo []videoHash
	for _, h := range hs {
		if !claimed[h.Path] {
			todo = append(todo, h)
		}
	}

	var errs []error
	// level2 holds the full hashes, built lazily and only for files that
	// appear in a surviving pair.
	level2 := map[string]*tmk.Hash{}
	full := func(i int) *tmk.Hash {
		p := todo[i].Path
		if h, ok := level2[p]; ok {
			return h
		}
		h, err := hashVideoFile(p, maxSeconds, true)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: re-reading for level 2: %w", p, err))
			level2[p] = nil
			return nil
		}
		level2[p] = h
		return h
	}

	// score caches the level-2 score of a pair, since the clustering asks for
	// it once to group and again to report the group's worst pair.
	scores := map[[2]int]float64{}
	scoreOf := func(i, j int) float64 {
		if i > j {
			i, j = j, i
		}
		if s, ok := scores[[2]int{i, j}]; ok {
			return s
		}
		s := 0.0
		if l1, err := tmk.Level1(todo[i].hash, todo[j].hash); err != nil {
			errs = append(errs, err)
		} else if l1 >= Level1Gate {
			if a, b := full(i), full(j); a != nil && b != nil {
				if l2, err := tmk.Level2(a, b); err != nil {
					errs = append(errs, err)
				} else {
					s = l2
				}
			}
		}
		scores[[2]int{i, j}] = s
		return s
	}

	uf := newUnionFind(len(todo))
	for i := 0; i < len(todo); i++ {
		for j := i + 1; j < len(todo); j++ {
			if scoreOf(i, j) >= threshold {
				uf.union(i, j)
			}
		}
	}

	var groups []Group
	for _, idx := range uf.groups() {
		g := Group{Kind: "video"}
		for _, i := range idx {
			g.Files = append(g.Files, todo[i].File)
		}
		worst := 1.0
		for a := 0; a < len(idx); a++ {
			for b := a + 1; b < len(idx); b++ {
				if s := scoreOf(idx[a], idx[b]); s < worst {
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
	return groups, errs
}
