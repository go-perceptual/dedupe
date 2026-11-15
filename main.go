// dedupe finds duplicate and near-duplicate media in a directory tree.
//
// It runs two passes. The first finds files whose BYTES are identical,
// grouping by size and hashing only same-size candidates. The second finds
// files whose PICTURES are the same although their bytes are not —
// recompressed, resized, re-encoded — using the PDQ perceptual hash.
//
// ⛔ It reports. It does not delete, move or link anything, and it takes no
// flag that would: the output is a list for a person to act on. A tool that
// removes files on the strength of a similarity threshold is a tool that
// removes the wrong file eventually, and the second pass is a HEURISTIC with
// a documented false-positive mode.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/go-perceptual/pdq"
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
		threshold  int
		minQuality int
		minSize    int64
		workers    int
		exactOnly  bool
	)

	cmd := &cobra.Command{
		Use:   "dedupe [flags] <dir>...",
		Short: "Find duplicate and near-duplicate media in a directory tree",
		Long: `Find duplicate and near-duplicate media in a directory tree.

Two passes. The first reports files whose BYTES are identical. The second
reports files whose PICTURES are the same although their bytes differ, using
the PDQ perceptual hash: a distance of 0 is the same picture, 30 or less is a
confident match, and unrelated pictures sit around 128.

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
			return run(cmd, args, options{
				threshold:  threshold,
				minQuality: pdq.Quality(minQuality),
				minSize:    minSize,
				workers:    workers,
				exactOnly:  exactOnly,
			})
		},
	}

	f := cmd.Flags()
	f.IntVar(&threshold, "threshold", 20, "largest perceptual distance still called a duplicate (0-256)")
	f.IntVar(&minQuality, "min-quality", 50, "ignore images whose PDQ quality is below this (0-100)")
	f.Int64Var(&minSize, "min-size", 1024, "ignore files smaller than this many bytes")
	f.IntVar(&workers, "workers", 0, "parallel hashers (0 means one per CPU)")
	f.BoolVar(&exactOnly, "exact-only", false, "report only byte-identical files, skipping the perceptual pass")
	return cmd
}

type options struct {
	threshold  int
	minQuality pdq.Quality
	minSize    int64
	workers    int
	exactOnly  bool
}

func run(cmd *cobra.Command, roots []string, o options) error {
	w := cmd.OutOrStdout()

	for _, r := range roots {
		if _, err := os.Stat(r); err != nil {
			return err
		}
	}

	files, walkErrs := walk(roots, o.minSize)
	// ⛔ The volume read is printed beside the result. A scan that found
	// nothing and a scan that read nothing print the same list of groups, and
	// only this line tells them apart.
	fmt.Fprintf(w, "scanned %d file(s) under %d root(s)\n", len(files), len(roots))
	if len(files) == 0 {
		reportErrors(w, walkErrs, nil, 0)
		return nil
	}

	exact, claimed, exactErrs := exactGroups(files)
	for i := range exact {
		sortGroup(&exact[i])
	}
	sort.Slice(exact, func(i, j int) bool {
		return exact[i].Recoverable() > exact[j].Recoverable()
	})

	var near []Group
	var hashErrs []error
	skipped := 0
	if !o.exactOnly {
		hs, s, errs := hashAll(files, o.minQuality, o.workers)
		skipped, hashErrs = s, errs
		fmt.Fprintf(w, "hashed %d image(s)\n", len(hs))
		near = nearGroups(hs, o.threshold, claimed)
	}

	report(w, exact, near)
	reportErrors(w, walkErrs, append(exactErrs, hashErrs...), skipped)
	return nil
}

func report(w interface{ Write([]byte) (int, error) }, exact, near []Group) {
	var total int64

	if len(exact) > 0 {
		fmt.Fprintf(w, "\nIDENTICAL BYTES — %d group(s)\n", len(exact))
		for _, g := range exact {
			fmt.Fprintf(w, "  %s recoverable\n", bytesOf(g.Recoverable()))
			for i, f := range g.Files {
				fmt.Fprintf(w, "    %s %s (%s)\n", keepOrDrop(i), f.Path, bytesOf(f.Size))
			}
			total += g.Recoverable()
		}
	}

	if len(near) > 0 {
		fmt.Fprintf(w, "\nSAME PICTURE, DIFFERENT BYTES — %d group(s)\n", len(near))
		for _, g := range near {
			fmt.Fprintf(w, "  %s recoverable, widest distance %d\n", bytesOf(g.Recoverable()), g.Distance)
			for i, f := range g.Files {
				fmt.Fprintf(w, "    %s %s (%s)\n", keepOrDrop(i), f.Path, bytesOf(f.Size))
			}
			total += g.Recoverable()
		}
	}

	if len(exact) == 0 && len(near) == 0 {
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
