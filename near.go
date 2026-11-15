package main

import (
	"runtime"
	"sort"
	"sync"

	"github.com/go-perceptual/pdq"
)

// hashAll computes the perceptual hash of every file, in parallel.
//
// Errors are collected, not fatal: one undecodable file must not end the
// scan, and the count of files that could not be read is reported beside the
// result so a short answer cannot be mistaken for a clean one.
func hashAll(files []File, minQuality pdq.Quality, workers int) ([]hashed, int, []error) {
	if workers < 1 {
		workers = runtime.NumCPU()
	}

	type outcome struct {
		h       hashed
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
				h, err := perceptual(f.Path, minQuality)
				switch {
				case err == errLowQuality:
					out <- outcome{skipped: true}
				case err != nil:
					out <- outcome{err: err}
				default:
					h.File = f
					out <- outcome{h: h}
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

	var hs []hashed
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

// nearGroups clusters hashes that lie within threshold of each other.
//
// The clustering is single-linkage, shared with the audio and video passes:
// see unionFind for the consequence that choice carries. The group's reported
// Distance is the WIDEST distance inside it, so a stretched chain is visible
// instead of hidden.
func nearGroups(hs []hashed, threshold int, claimed map[string]bool) []Group {
	// Files already reported as exact duplicates are not repeated here.
	var todo []hashed
	for _, h := range hs {
		if !claimed[h.Path] {
			todo = append(todo, h)
		}
	}

	uf := newUnionFind(len(todo))
	for i := 0; i < len(todo); i++ {
		for j := i + 1; j < len(todo); j++ {
			if todo[i].hash.Distance(todo[j].hash) <= threshold {
				uf.union(i, j)
			}
		}
	}

	var groups []Group
	for _, idx := range uf.groups() {
		var g Group
		g.Kind = "near"
		for _, i := range idx {
			g.Files = append(g.Files, todo[i].File)
		}
		// The widest distance in the group, which is what single linkage can
		// stretch.
		for a := 0; a < len(idx); a++ {
			for b := a + 1; b < len(idx); b++ {
				if d := todo[idx[a]].hash.Distance(todo[idx[b]].hash); d > g.Distance {
					g.Distance = d
				}
			}
		}
		sortGroup(&g)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Recoverable() > groups[j].Recoverable()
	})
	return groups
}

// sortGroup puts the largest file first: it is the one with the most pixels
// to lose, so it is the natural one to keep.
func sortGroup(g *Group) {
	sort.Slice(g.Files, func(i, j int) bool {
		if g.Files[i].Size != g.Files[j].Size {
			return g.Files[i].Size > g.Files[j].Size
		}
		return g.Files[i].Path < g.Files[j].Path
	})
}
