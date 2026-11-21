package find

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-perceptual/tmk"
)

// fixtures is where the committed media lives: the module root's testdata,
// shared with the command's tests.
const fixtures = "../testdata/"

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
		{"melody.flac", "a.mp4", KindAudio},
		{"melody.mp3", "b.png", KindAudio},
		{"melody.opus", "c", KindAudio},
		{"melody.m4a", "d.txt", KindAudio},
		{"clip-360p.mp4", "e.flac", KindVideo},
		{"clip-90p.mp4", "f", KindVideo},
	} {
		p := filepath.Join(dir, c.as)
		copyFixture(t, fixtures+c.fixture, p)
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
	a, err := hashVideoFile(fixtures+"clip-360p.mp4", VideoSecondsDefault, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fixtures + "clip-180p.mp4", fixtures + "clip-90p.mp4"} {
		b, err := hashVideoFile(path, VideoSecondsDefault, false)
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
	other, err := hashVideoFile(fixtures+"clip-other.mp4", VideoSecondsDefault, false)
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
	sweep, err := hashVideoFile(fixtures+"clip-360p.mp4", VideoSecondsDefault, false)
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

	confirm, err := hashVideoFile(fixtures+"clip-360p.mp4", VideoSecondsDefault, true)
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
