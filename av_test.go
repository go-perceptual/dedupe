package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-perceptual/tmk"
)

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

func TestClassifyReadsEachMediumFromItsBytes(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		fixture, as string
		want        Kind
	}{
		// Each one is given an extension that is wrong, absent, or belongs to
		// another medium. The classifier must not care.
		{"testdata/melody.flac", "a.mp4", KindAudio},
		{"testdata/melody.mp3", "b.png", KindAudio},
		{"testdata/melody.opus", "c", KindAudio},
		{"testdata/melody.m4a", "d.txt", KindAudio},
		{"testdata/clip-360p.mp4", "e.flac", KindVideo},
		{"testdata/clip-90p.mp4", "f", KindVideo},
	} {
		p := filepath.Join(dir, c.as)
		copyFixture(t, c.fixture, p)
		got, err := classify(p)
		if err != nil {
			t.Errorf("%s as %s: %v", c.fixture, c.as, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s named %q classified as %q, want %q", c.fixture, c.as, got, c.want)
		}
	}
}

func TestClassifyRefusesWhatIsNotMedia(t *testing.T) {
	dir := t.TempDir()
	// ⛔ Including a file named as media, which is the inverse of the test
	// above: the name must not get a non-media file IN either.
	for _, name := range []string{"notes.txt", "fake.mp4", "fake.jpg", "empty.png"} {
		body := []byte(strings.Repeat("this is not media at all. ", 200))
		if name == "empty.png" {
			body = nil
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		got, _ := classify(p)
		if got != KindOther {
			t.Errorf("%s classified as %q, want not-media", name, got)
		}
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

// TestLevel1GateDoesNotRejectATrueMatch is the measurement that justifies
// Level1Gate.
//
// ⛔ The gate is MY invention, not something TMK prescribes, and it is a
// recall risk: a pair that level 1 scores below it is never looked at again,
// so if the gate sits too high the tool silently stops finding duplicates.
// Asserting it is "safe" would be an opinion. This measures the level-1 score
// of the hardest true match in the corpus — a clip against its own
// quarter-scale copy — and fails if the margin above the gate disappears.
func TestLevel1GateDoesNotRejectATrueMatch(t *testing.T) {
	a, err := hashVideoFile("testdata/clip-360p.mp4", videoSecondsDefault, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"testdata/clip-180p.mp4", "testdata/clip-90p.mp4"} {
		b, err := hashVideoFile(path, videoSecondsDefault, false)
		if err != nil {
			t.Fatal(err)
		}
		l1, err := tmk.Level1(a, b)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("level 1, clip-360p vs %s: %.4f (gate %.2f)", filepath.Base(path), l1, Level1Gate)
		if l1 < Level1Gate {
			t.Errorf("%s: level 1 scores %.4f, BELOW the %.2f gate, so this true match is discarded before level 2",
				path, l1, Level1Gate)
		}
		if l1-Level1Gate < 0.2 {
			t.Errorf("%s: level 1 is %.4f, only %.4f above the gate -- too little margin to call the prefilter safe",
				path, l1, l1-Level1Gate)
		}
	}

	// And the negative half: the gate must actually reject something, or it is
	// not a prefilter at all and the level-2 cost was never avoided.
	other, err := hashVideoFile("testdata/clip-other.mp4", videoSecondsDefault, false)
	if err != nil {
		t.Fatal(err)
	}
	l1, err := tmk.Level1(a, other)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("level 1, clip-360p vs an unrelated clip: %.4f", l1)
	if l1 >= Level1Gate {
		t.Errorf("an unrelated clip scores %.4f at level 1, so the gate rejects nothing", l1)
	}
}

// TestLevel2IsNotHeldForEveryVideo guards the memory decision.
//
// ⛔ The level-2 features are 262 KB a video against 1 KB for level 1, and
// holding them for a whole tree is what makes this tool unusable on a real
// download folder. The sweep must therefore release them. This asserts the
// shape rather than the byte count: Cos and Sin nil after a level-1 hash,
// present after a level-2 one.
func TestLevel2IsNotHeldForEveryVideo(t *testing.T) {
	sweep, err := hashVideoFile("testdata/clip-360p.mp4", videoSecondsDefault, false)
	if err != nil {
		t.Fatal(err)
	}
	if sweep.Cos != nil || sweep.Sin != nil {
		t.Error("the level-1 sweep kept the level-2 features, so memory grows by 262 KB a video")
	}
	var zero bool
	for _, v := range sweep.Average {
		if v != 0 {
			zero = true
			break
		}
	}
	if !zero {
		t.Error("the level-1 feature is all zeroes, so releasing level 2 took the hash with it")
	}

	confirm, err := hashVideoFile("testdata/clip-360p.mp4", videoSecondsDefault, true)
	if err != nil {
		t.Fatal(err)
	}
	if confirm.Cos == nil || confirm.Sin == nil {
		t.Fatal("the level-2 hash has no level-2 features")
	}
	// The two must agree at level 1, or the sweep and the confirmation are
	// looking at different videos.
	l1, err := tmk.Level1(sweep, confirm)
	if err != nil {
		t.Fatal(err)
	}
	if l1 < 0.9999 {
		t.Errorf("the same file hashed twice scores %.6f at level 1", l1)
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
