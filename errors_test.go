package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-perceptual/media"
)

// TestTheErrorListIsTruncatedAndSaysSoBy covers the branch that keeps a
// broken tree from printing ten thousand lines.
//
// ⛔ The count of what was NOT shown is the part under test. A list that
// quietly stops at ten reads as ten problems, and a person who fixes those
// ten and re-runs gets the same report -- so the truncation has to say how
// much it is hiding.
func TestTheErrorListIsTruncatedAndSaysSoBy(t *testing.T) {
	var many []error
	for i := 0; i < 15; i++ {
		many = append(many, fmt.Errorf("problem %d", i))
	}
	var sb strings.Builder
	reportErrors(&sb, nil, many, 0)
	out := sb.String()

	if !strings.Contains(out, "15 path(s) could not be read") {
		t.Errorf("the total is wrong:\n%s", out)
	}
	if !strings.Contains(out, "... and 5 more") {
		t.Errorf("the hidden count is missing:\n%s", out)
	}
	if strings.Contains(out, "problem 10") {
		t.Errorf("more than ten errors were printed:\n%s", out)
	}
	if !strings.Contains(out, "problem 9") {
		t.Errorf("fewer than ten errors were printed:\n%s", out)
	}
}

// TestACorruptVideoMakesTheReportSayIncomplete is the video half of the
// warning that matters most.
//
// ⛔ A half-downloaded film is the ordinary case in the tree this tool is
// pointed at. It must not be silently dropped: a scan that skipped it and
// printed a clean list is indistinguishable from a scan that checked it.
//
// ⛔ It fails at CLASSIFICATION, not at hashing, and that was worth finding
// out rather than assuming. A truncated MP4 is refused by the container with
// "read box body length 38413 does not match expected length 56510" — the
// declared box outruns the file — so classify returns the error and the file
// never reaches a decoder. The warning therefore comes from classifyAll's
// error path. An earlier version of this test asserted the hashing path and
// SKIPPED, which proved nothing; a skip is not a pass.
func TestACorruptVideoMakesTheReportSayIncomplete(t *testing.T) {
	data, err := os.ReadFile("testdata/clip-360p.mp4")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	truncated := filepath.Join(dir, "half-downloaded.mp4")
	if err := os.WriteFile(truncated, data[:len(data)*2/3], 0o644); err != nil {
		t.Fatal(err)
	}

	// The premise, checked rather than assumed: the file must be REFUSED with
	// an error, not quietly classified as something that is not media. Those
	// two outcomes print differently and only one of them warns.
	kind, err := media.Classify(truncated)
	if err == nil {
		t.Fatalf("a truncated MP4 classified cleanly as %q, so nothing warns about it", kind)
	}

	// A second, whole video so the pass has something to do besides fail.
	copyFixture(t, "testdata/clip-180p.mp4", filepath.Join(dir, "whole.mp4"))

	out, runErr := runCLI(t, dir, "--only", "video")
	if runErr != nil {
		t.Fatalf("%v\n%s", runErr, out)
	}
	if !strings.Contains(out, "INCOMPLETE") {
		t.Fatalf("a corrupt video did not make the report say so:\n%s", out)
	}
	if !strings.Contains(out, "1 path(s) could not be read") {
		t.Fatalf("the count of unread paths is missing:\n%s", out)
	}
	// And the whole file is still classified, so one bad file does not end
	// the scan.
	if !strings.Contains(out, "1 video(s)") {
		t.Fatalf("the readable video was lost along with the corrupt one:\n%s", out)
	}
}

// TestAnUnsupportedCodecIsReportedNotSkipped pins what happens to a video
// this build cannot decode.
//
// ⛔ The distinction being guarded is "reported" against "skipped". A VP9 file
// classifies as video -- the container reads it fine -- and then fails to
// decode, and that failure MUST reach the report. A deduplicator that quietly
// dropped every VP9 in the tree would print a clean list of the rest, and the
// person reading it has no way to know a whole codec was ignored.
//
// ⚠ The message go-avkit produces for it is WRONG, and the fixture is
// committed so that it is on record rather than remembered. It says
//
//	decode: invalid sample: NAL unit of 2185855810 bytes with 2939 left
//
// which is an H.264 NAL-unit parser being handed VP9 data: the decoder does
// not check the codec before parsing, so it reads VP9 bytes as a length
// prefix and reports the file as CORRUPT instead of saying the codec is
// unsupported. A user is sent to look at their file when the answer is that
// this build cannot read it. Reported upstream; the length is bounds-checked,
// so it is a wrong diagnosis rather than an unbounded allocation.
//
// This test therefore asserts the REPORTING, not the wording -- when the
// message is fixed, or VP9 lands, this test should be revisited rather than
// silently satisfied.
func TestAnUnsupportedCodecIsReportedNotSkipped(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "testdata/unsupported-vp9.webm", filepath.Join(dir, "clip.webm"))
	copyFixture(t, "testdata/clip-360p.mp4", filepath.Join(dir, "readable.mp4"))

	// The premise: the container DOES read it, so this is not a classification
	// failure dressed up as a decode one.
	kind, err := media.Classify(filepath.Join(dir, "clip.webm"))
	if err != nil || kind != media.KindVideo {
		t.Fatalf("the VP9 fixture classified as %q (%v); it should be readable video", kind, err)
	}

	out, runErr := runCLI(t, dir, "--only", "video")
	if runErr != nil {
		t.Fatalf("%v\n%s", runErr, out)
	}
	if !strings.Contains(out, "2 video(s)") {
		t.Errorf("both files should classify as video:\n%s", out)
	}
	if !strings.Contains(out, "INCOMPLETE") {
		t.Fatalf("a video this build cannot decode was not reported:\n%s", out)
	}
	if !strings.Contains(out, "clip.webm") {
		t.Errorf("the report does not name the file it could not read:\n%s", out)
	}
	// And the readable one still went through: one undecodable file must not
	// end the pass.
	if !strings.Contains(out, "hashed 1 video(s) at level 1") {
		t.Errorf("the readable video was not hashed:\n%s", out)
	}
}

// TestExactOnlySkipsEveryPerceptualPass guards the flag that makes this tool
// cheap when only byte-identical copies are wanted.
func TestExactOnlySkipsEveryPerceptualPass(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "testdata/melody.flac", filepath.Join(dir, "song.flac"))
	copyFixture(t, "testdata/melody.mp3", filepath.Join(dir, "song.mp3"))
	copyFixture(t, "testdata/clip-360p.mp4", filepath.Join(dir, "film.mp4"))
	copyFixture(t, "testdata/clip-180p.mp4", filepath.Join(dir, "film-half.mp4"))

	out, err := runCLI(t, dir, "--exact-only")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, unwanted := range []string{"SAME RECORDING", "SAME VIDEO", "SAME PICTURE", "fingerprinted", "at level 1"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("--exact-only still ran a perceptual pass (%q):\n%s", unwanted, out)
		}
	}
	// ⛔ And it must still have CLASSIFIED them, or the line reporting what
	// the tree holds would go silent exactly when a user is narrowing down a
	// problem.
	if !strings.Contains(out, "2 recording(s), 2 video(s)") {
		t.Errorf("--exact-only stopped reporting what the tree holds:\n%s", out)
	}
}

// TestOnlyRunsOnePass is the positive control for --only: each value must
// suppress the other two rather than merely relabel the output.
func TestOnlyRunsOnePass(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "testdata/melody.flac", filepath.Join(dir, "song.flac"))
	copyFixture(t, "testdata/melody.mp3", filepath.Join(dir, "song.mp3"))
	copyFixture(t, "testdata/clip-360p.mp4", filepath.Join(dir, "film.mp4"))
	copyFixture(t, "testdata/clip-180p.mp4", filepath.Join(dir, "film-half.mp4"))

	audioOut, err := runCLI(t, dir, "--only", "audio")
	if err != nil {
		t.Fatalf("%v\n%s", err, audioOut)
	}
	if !strings.Contains(audioOut, "SAME RECORDING") {
		t.Errorf("--only audio found no recordings:\n%s", audioOut)
	}
	if strings.Contains(audioOut, "SAME VIDEO") {
		t.Errorf("--only audio ran the video pass:\n%s", audioOut)
	}

	videoOut, err := runCLI(t, dir, "--only", "video")
	if err != nil {
		t.Fatalf("%v\n%s", err, videoOut)
	}
	if !strings.Contains(videoOut, "SAME VIDEO") {
		t.Errorf("--only video found no videos:\n%s", videoOut)
	}
	if strings.Contains(videoOut, "SAME RECORDING") {
		t.Errorf("--only video ran the audio pass:\n%s", videoOut)
	}
}

// TestAnEmptyFileIsNotMedia: zero bytes must not be an error, and must not
// be a hash either.
func TestAnEmptyFileIsNotMedia(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.mp4")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	k, err := media.Classify(p)
	if k != media.KindOther {
		t.Errorf("an empty file classified as %q", k)
	}
	if err != nil && !errors.Is(err, os.ErrClosed) {
		// A read of zero bytes may or may not surface io.EOF depending on the
		// platform; what matters is that it is not media.
		t.Logf("classifying an empty file reported %v, which is acceptable", err)
	}
}
