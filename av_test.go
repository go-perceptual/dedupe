package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ⛔ The tests that call the grouping's internals — classify, hashVideoFile,
// the level-1 gate and the level-2 memory release — moved with the code to
// find/av_test.go. What stays here drives the command.

// copyFixture puts a committed fixture into a scratch tree under a name of
// the test's choosing, so a test can give a file a LYING extension.
func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAudioPassGroupsEveryEncoding is the deduplicator's central claim for
// sound: the same recording in four formats is one recording.
func TestAudioPassGroupsEveryEncoding(t *testing.T) {
	dir := t.TempDir()
	// ⛔ One of them is deliberately misnamed. The old extension filter would
	// have skipped it, and the report would have been a smaller group that
	// looked perfectly correct.
	copyFixture(t, "testdata/melody.flac", filepath.Join(dir, "song.flac"))
	copyFixture(t, "testdata/melody.mp3", filepath.Join(dir, "song.mp3"))
	copyFixture(t, "testdata/melody.opus", filepath.Join(dir, "song.opus"))
	copyFixture(t, "testdata/melody.m4a", filepath.Join(dir, "renamed.dat"))

	out, err := runCLI(t, dir, "--only", "audio")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "SAME RECORDING, DIFFERENT BYTES — 1 group(s)") {
		t.Fatalf("the four encodings were not grouped as one recording:\n%s", out)
	}
	for _, name := range []string{"song.flac", "song.mp3", "song.opus", "renamed.dat"} {
		if !strings.Contains(out, name) {
			t.Errorf("%s is missing from the group:\n%s", name, out)
		}
	}
}

// TestVideoPassGroupsEveryScale is the same claim for video, and it is the
// question that was asked of this tool: is a 4K and a 1080p copy of one film
// one film?
func TestVideoPassGroupsEveryScale(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "testdata/clip-360p.mp4", filepath.Join(dir, "film.mp4"))
	copyFixture(t, "testdata/clip-180p.mp4", filepath.Join(dir, "film-half.mp4"))
	copyFixture(t, "testdata/clip-90p.mp4", filepath.Join(dir, "film-quarter"))

	out, err := runCLI(t, dir, "--only", "video")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "SAME VIDEO, DIFFERENT BYTES — 1 group(s)") {
		t.Fatalf("three scales of one clip were not grouped:\n%s", out)
	}
	for _, name := range []string{"film.mp4", "film-half.mp4", "film-quarter"} {
		if !strings.Contains(out, name) {
			t.Errorf("%s is missing from the group:\n%s", name, out)
		}
	}
}

// TestUnrelatedMediaIsNotGrouped is the control for both passes above.
//
// ⛔ Without it, they would both still pass if the comparison had become
// blind and returned a high score for everything — which is the failure mode
// a deduplicator must never have, since acting on it loses files.
func TestUnrelatedMediaIsNotGrouped(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "testdata/clip-360p.mp4", filepath.Join(dir, "film.mp4"))
	copyFixture(t, "testdata/clip-other.mp4", filepath.Join(dir, "bars.mp4"))
	copyFixture(t, "testdata/melody.flac", filepath.Join(dir, "song.flac"))

	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "no duplicates found") {
		t.Fatalf("unrelated media was grouped:\n%s", out)
	}
}

// TestAVideoIsNotComparedWithARecording guards the choice classify makes for
// a file holding both.
func TestAVideoIsNotComparedWithARecording(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, "testdata/clip-360p.mp4", filepath.Join(dir, "film.mp4"))
	copyFixture(t, "testdata/melody.flac", filepath.Join(dir, "song.flac"))

	out, err := runCLI(t, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "1 recording(s), 1 video(s)") {
		t.Fatalf("the two media were not kept in separate passes:\n%s", out)
	}
}

func TestOnlyFlagRejectsAnUnknownPass(t *testing.T) {
	if _, err := runCLI(t, t.TempDir(), "--only", "pictures"); err == nil {
		t.Error("--only pictures was accepted")
	}
	for _, bad := range [][]string{
		{"--audio-threshold", "1.5"},
		{"--video-threshold", "-0.1"},
		{"--audio-seconds", "0"},
		{"--video-seconds", "0"},
	} {
		args := append([]string{t.TempDir()}, bad...)
		if _, err := runCLI(t, args...); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
}
