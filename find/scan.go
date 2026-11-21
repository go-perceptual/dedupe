package find

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/go-perceptual/pdq"
)

// File is one candidate found on disk.
type File struct {
	Path string
	Size int64
	// Media is what the file turned out to HOLD, decided by reading it.
	Media Kind
}

// Group is a set of files that are duplicates of one another.
type Group struct {
	// Kind is "exact" when the bytes are identical, or "near", "audio" or
	// "video" for the three perceptual passes.
	Kind string
	// Files are the members, largest first, so the first is the natural one
	// to keep.
	Files []File
	// Distance is the largest perceptual distance inside the group, for the
	// image pass; 0 otherwise.
	Distance int
	// Score is the LOWEST pairwise similarity inside the group, for the audio
	// and video passes; 0 otherwise.
	//
	// ⛔ The lowest, not the average or the best. Single-linkage clustering
	// can stretch a group along a chain, and only the worst pair shows it.
	Score float64
}

// Recoverable is the number of bytes freed by keeping one member and losing
// the rest.
//
// ⛔ It counts every member but the FIRST, not every member: a duplicate set
// of three files frees two of them. Reporting the sum of the whole group is
// the easy mistake, and it overstates what the user can actually get back.
func (g Group) Recoverable() int64 {
	var n int64
	for _, f := range g.Files[1:] {
		n += f.Size
	}
	return n
}

// walk collects the candidate files under each root.
//
// Unreadable directories are reported rather than skipped silently: a scan
// that could not read part of the tree has not scanned the tree, and a
// smaller answer would read as "fewer duplicates".
func walk(roots []string, minSize int64) ([]File, []error) {
	var out []File
	var errs []error
	seen := map[string]bool{}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				errs = append(errs, err)
				return nil // keep going: one unreadable directory is not the tree
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return nil // symlinks and devices are not content
			}
			// ⛔ No extension filter. It used to be here, and it was wrong:
			// an extension is a claim by whoever named the file, and this
			// tool's whole job is finding the copy that was renamed. What
			// a file holds is decided by classifyAll, which reads it.
			info, err := d.Info()
			if err != nil {
				errs = append(errs, err)
				return nil
			}
			if info.Size() < minSize {
				return nil
			}
			abs, err := filepath.Abs(p)
			if err != nil {
				abs = p
			}
			// The same file reached through two roots is one file.
			if seen[abs] {
				return nil
			}
			seen[abs] = true
			out = append(out, File{Path: abs, Size: info.Size()})
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, errs
}

// classifyAll reads each file's header and keeps only the media ones.
//
// ⛔ This replaced an extension filter, and the change is the point rather
// than a refinement. The old gate trusted the name, so a film saved as
// `video.dat`, a photograph renamed `.thumb`, or an MP3 with no extension at
// all were invisible -- and in a DEDUPLICATOR an invisible file reads as "no
// duplicates found", which is indistinguishable from a clean tree. The cost
// is one open and one 64 KiB read per file in the tree, which is what buys
// the answer being about the tree rather than about its naming conventions.
//
// The count of files that are not media is returned, not dropped silently: a
// scan of ten thousand files that classified nine thousand as other has
// either met a tree with little media in it or a broken classifier, and only
// the number distinguishes those.
func classifyAll(ctx context.Context, files []File, workers int) (media []File, other int, errs []error) {
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	type outcome struct {
		f     File
		other bool
		err   error
	}
	in := make(chan File)
	out := make(chan outcome)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range in {
				k, err := classify(f.Path)
				switch {
				case err != nil:
					out <- outcome{err: err}
				case k == KindOther:
					out <- outcome{other: true}
				default:
					f.Media = k
					out <- outcome{f: f}
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

	for o := range out {
		switch {
		case o.err != nil:
			errs = append(errs, o.err)
		case o.other:
			other++
		default:
			media = append(media, o.f)
		}
	}
	sort.Slice(media, func(i, j int) bool { return media[i].Path < media[j].Path })
	return media, other, errs
}

// byKind splits classified files into the three perceptual passes.
func byKind(files []File) (images, audio, video []File) {
	for _, f := range files {
		switch f.Media {
		case KindImage:
			images = append(images, f)
		case KindAudio:
			audio = append(audio, f)
		case KindVideo:
			video = append(video, f)
		}
	}
	return
}

// contentHash is the SHA-256 of a file's bytes.
func contentHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// exactGroups finds files whose bytes are identical.
//
// Files are grouped by SIZE first and only same-size files are read. Two
// files of different lengths cannot have the same bytes, so this is not an
// optimisation to be traded away: it is what keeps the pass from reading a
// whole tree.
func exactGroups(files []File) ([]Group, map[string]bool, []error) {
	bySize := map[int64][]File{}
	for _, f := range files {
		bySize[f.Size] = append(bySize[f.Size], f)
	}

	var groups []Group
	var errs []error
	claimed := map[string]bool{}

	sizes := make([]int64, 0, len(bySize))
	for s := range bySize {
		sizes = append(sizes, s)
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] > sizes[j] })

	for _, s := range sizes {
		same := bySize[s]
		if len(same) < 2 {
			continue
		}
		byHash := map[string][]File{}
		for _, f := range same {
			h, err := contentHash(f.Path)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			byHash[h] = append(byHash[h], f)
		}
		keys := make([]string, 0, len(byHash))
		for k := range byHash {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			g := Group{Kind: "exact", Files: byHash[k]}
			if len(g.Files) < 2 {
				continue
			}
			sortGroup(&g)
			// ⛔ Only the members this tool would DROP are claimed. The one it
			// would keep still enters the perceptual pass, because the same
			// picture may also exist in another encoding — and claiming the
			// whole group hides exactly that: a PNG with a byte-identical
			// copy could never be matched against its own JPEG, since its
			// only partner had been removed from the comparison.
			for _, f := range g.Files[1:] {
				claimed[f.Path] = true
			}
			groups = append(groups, g)
		}
	}
	return groups, claimed, errs
}

// hashed is one file with its perceptual hash.
type hashed struct {
	File
	hash    pdq.Hash
	quality pdq.Quality
}

var errLowQuality = errors.New("quality below the threshold")

// perceptual hashes one image file.
//
// ⛔ A hash below the quality threshold is REFUSED rather than returned. PDQ
// reports quality precisely because a featureless image produces a hash that
// matches other featureless images, and letting those through turns a page of
// blank thumbnails into one enormous false "duplicate" group.
func perceptual(path string, minQuality pdq.Quality) (hashed, error) {
	f, err := os.Open(path)
	if err != nil {
		return hashed{}, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return hashed{}, err
	}
	h, q, err := pdq.Sum(img)
	if err != nil {
		return hashed{}, err
	}
	if q < minQuality {
		return hashed{}, errLowQuality
	}
	return hashed{hash: h, quality: q}, nil
}
