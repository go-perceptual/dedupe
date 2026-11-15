// dedupe finds duplicate and near-duplicate media in a directory tree.
//
// It classifies every file by READING it, then runs four passes. The first
// finds files whose BYTES are identical, grouping by size and hashing only
// same-size candidates. The other three find files whose CONTENT is the same
// although their bytes are not — recompressed, resized, re-encoded, converted
// — one per medium: PDQ for pictures, chroma for recordings, TMK+PDQF for
// video.
//
// ⛔ It reports. It does not delete, move or link anything, and it takes no
// flag that would: the output is a list for a person to act on. A tool that
// removes files on the strength of a similarity threshold is a tool that
// removes the wrong file eventually, and the perceptual passes are HEURISTICS
// with documented false-positive modes.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/go-perceptual/pdq"
	"github.com/go-perceptual/tmk"
	"github.com/spf13/cobra"
)

// osExit allows tests to override os.Exit.
var osExit = os.Exit

func main() {
	if err := RootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		osExit(1)
	}
}

// RootCmd returns the top-level command, exported so tests drive the wiring
// through SetArgs/SetOut/SetErr instead of spawning a process.
func RootCmd() *cobra.Command {
	var (
		threshold      int
		minQuality     int
		minSize        int64
		workers        int
		exactOnly      bool
		audioThreshold float64
		videoThreshold float64
		audioSeconds   int
		videoSeconds   int
		only           string
	)

	cmd := &cobra.Command{
		Use:   "dedupe [flags] <dir>...",
		Short: "Find duplicate and near-duplicate media in a directory tree",
		Long: `Find duplicate and near-duplicate media in a directory tree.

Every file is classified by READING it, never by its extension: a film saved
as .dat and a photograph renamed .thumb are exactly the copies this tool
exists to find.

Then four passes. The first reports files whose BYTES are identical. The other
three report files whose content is the same although their bytes differ:

  pictures   PDQ Hamming distance — 0 is the same picture, 30 or less a
             confident match, unrelated pictures sit around 128
  recordings chroma score — 0.9 or more is the same recording
  video      TMK+PDQF score — 0.7 or more is the same video

dedupe REPORTS. It never deletes, moves or links anything.`,
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if threshold < 0 || threshold > 256 {
				return fmt.Errorf("--threshold must be between 0 and 256, got %d", threshold)
			}
			if minQuality < 0 || minQuality > 100 {
				return fmt.Errorf("--min-quality must be between 0 and 100, got %d", minQuality)
			}
			if audioThreshold < 0 || audioThreshold > 1 {
				return fmt.Errorf("--audio-threshold must be between 0 and 1, got %v", audioThreshold)
			}
			if videoThreshold < 0 || videoThreshold > 1 {
				return fmt.Errorf("--video-threshold must be between 0 and 1, got %v", videoThreshold)
			}
			if audioSeconds < 1 {
				return fmt.Errorf("--audio-seconds must be at least 1, got %d", audioSeconds)
			}
			if videoSeconds < 1 {
				return fmt.Errorf("--video-seconds must be at least 1, got %d", videoSeconds)
			}
			switch only {
			case "", "image", "audio", "video":
			default:
				return fmt.Errorf("--only must be image, audio or video, got %q", only)
			}
			return run(cmd, args, options{
				threshold:      threshold,
				minQuality:     pdq.Quality(minQuality),
				minSize:        minSize,
				workers:        workers,
				exactOnly:      exactOnly,
				audioThreshold: audioThreshold,
				videoThreshold: videoThreshold,
				audioSeconds:   audioSeconds,
				videoSeconds:   videoSeconds,
				only:           Kind(only),
			})
		},
	}

	f := cmd.Flags()
	f.IntVar(&threshold, "threshold", 20, "largest PDQ distance still called the same picture (0-256)")
	f.IntVar(&minQuality, "min-quality", 50, "ignore images whose PDQ quality is below this (0-100)")
	f.Int64Var(&minSize, "min-size", 1024, "ignore files smaller than this many bytes")
	f.IntVar(&workers, "workers", 0, "parallel hashers (0 means one per CPU)")
	f.BoolVar(&exactOnly, "exact-only", false, "report only byte-identical files, skipping every perceptual pass")
	f.Float64Var(&audioThreshold, "audio-threshold", AudioThreshold,
		"chroma score at or above which two recordings are the same (0-1)")
	f.Float64Var(&videoThreshold, "video-threshold", tmk.DefaultThreshold,
		"TMK score at or above which two videos are the same (0-1)")
	f.IntVar(&audioSeconds, "audio-seconds", audioSecondsDefault,
		"compare at most this many seconds of each recording")
	f.IntVar(&videoSeconds, "video-seconds", videoSecondsDefault,
		"compare at most this many seconds of each video")
	f.StringVar(&only, "only", "", "run one pass only: image, audio or video")
	return cmd
}

type options struct {
	threshold      int
	minQuality     pdq.Quality
	minSize        int64
	workers        int
	exactOnly      bool
	audioThreshold float64
	videoThreshold float64
	audioSeconds   int
	videoSeconds   int
	only           Kind
}

// wants says whether a pass should run.
func (o options) wants(k Kind) bool { return o.only == "" || o.only == k }

func run(cmd *cobra.Command, roots []string, o options) error {
	w := cmd.OutOrStdout()

	for _, r := range roots {
		if _, err := os.Stat(r); err != nil {
			return err
		}
	}

	found, walkErrs := walk(roots, o.minSize)
	// ⛔ The volume read is printed beside the result. A scan that found
	// nothing and a scan that read nothing print the same list of groups, and
	// only this line tells them apart.
	fmt.Fprintf(w, "walked %d file(s) under %d root(s)\n", len(found), len(roots))
	if len(found) == 0 {
		reportErrors(w, walkErrs, nil, 0)
		return nil
	}

	files, other, classErrs := classifyAll(found, o.workers)
	images, audio, video := byKind(files)
	fmt.Fprintf(w, "classified by content: %d image(s), %d recording(s), %d video(s); %d not media\n",
		len(images), len(audio), len(video), other)
	if len(files) == 0 {
		reportErrors(w, walkErrs, classErrs, 0)
		return nil
	}

	exact, claimed, exactErrs := exactGroups(files)
	for i := range exact {
		sortGroup(&exact[i])
	}
	sort.Slice(exact, func(i, j int) bool {
		return exact[i].Recoverable() > exact[j].Recoverable()
	})

	var near, audioG, videoG []Group
	readErrs := append(append([]error{}, classErrs...), exactErrs...)
	skipped := 0

	if !o.exactOnly {
		if o.wants(KindImage) && len(images) > 0 {
			hs, s, errs := hashAll(images, o.minQuality, o.workers)
			skipped += s
			readErrs = append(readErrs, errs...)
			fmt.Fprintf(w, "hashed %d image(s)\n", len(hs))
			near = nearGroups(hs, o.threshold, claimed)
		}
		if o.wants(KindAudio) && len(audio) > 0 {
			hs, s, errs := hashAudio(audio, o.audioSeconds, o.workers)
			skipped += s
			readErrs = append(readErrs, errs...)
			fmt.Fprintf(w, "fingerprinted %d recording(s)\n", len(hs))
			audioG = audioGroups(hs, o.audioThreshold, claimed)
		}
		if o.wants(KindVideo) && len(video) > 0 {
			hs, s, errs := hashVideos(video, o.videoSeconds, o.workers)
			skipped += s
			readErrs = append(readErrs, errs...)
			fmt.Fprintf(w, "hashed %d video(s) at level 1\n", len(hs))
			g, errs2 := videoGroups(hs, o.videoThreshold, o.videoSeconds, claimed)
			videoG = g
			readErrs = append(readErrs, errs2...)
		}
	}

	report(w, exact, near, audioG, videoG)
	reportErrors(w, walkErrs, readErrs, skipped)
	return nil
}

func report(w interface{ Write([]byte) (int, error) }, exact, near, audio, video []Group) {
	var total int64

	section := func(title string, groups []Group, detail func(Group) string) {
		if len(groups) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s — %d group(s)\n", title, len(groups))
		for _, g := range groups {
			fmt.Fprintf(w, "  %s recoverable%s\n", bytesOf(g.Recoverable()), detail(g))
			for i, f := range g.Files {
				fmt.Fprintf(w, "    %s %s (%s)\n", keepOrDrop(i), f.Path, bytesOf(f.Size))
			}
			total += g.Recoverable()
		}
	}

	section("IDENTICAL BYTES", exact, func(Group) string { return "" })
	section("SAME PICTURE, DIFFERENT BYTES", near, func(g Group) string {
		return fmt.Sprintf(", widest distance %d", g.Distance)
	})
	section("SAME RECORDING, DIFFERENT BYTES", audio, func(g Group) string {
		return fmt.Sprintf(", weakest pair %.4f", g.Score)
	})
	section("SAME VIDEO, DIFFERENT BYTES", video, func(g Group) string {
		return fmt.Sprintf(", weakest pair %.4f", g.Score)
	})

	if len(exact) == 0 && len(near) == 0 && len(audio) == 0 && len(video) == 0 {
		fmt.Fprintln(w, "\nno duplicates found")
		return
	}
	fmt.Fprintf(w, "\n%s recoverable in total, keeping the largest of each group\n", bytesOf(total))
	fmt.Fprintln(w, "dedupe changes nothing on disk: this is a list to act on, not an action.")
}

// keepOrDrop marks the member this tool would keep. It is advice, printed
// beside every path, never applied.
func keepOrDrop(i int) string {
	if i == 0 {
		return "keep "
	}
	return "  dup"
}

func reportErrors(w interface{ Write([]byte) (int, error) }, walkErrs, readErrs []error, skipped int) {
	if skipped > 0 {
		fmt.Fprintf(w, "\n%d image(s) skipped as too featureless to match on (raise or lower --min-quality)\n", skipped)
	}
	if n := len(walkErrs) + len(readErrs); n > 0 {
		fmt.Fprintf(w, "\n⛔ %d path(s) could not be read, so this report is INCOMPLETE:\n", n)
		shown := 0
		for _, e := range append(append([]error{}, walkErrs...), readErrs...) {
			if shown == 10 {
				fmt.Fprintf(w, "    ... and %d more\n", n-shown)
				break
			}
			fmt.Fprintf(w, "    %v\n", e)
			shown++
		}
	}
}

func bytesOf(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}
