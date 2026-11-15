package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-perceptual/chroma"
)

// ⛔ The tests for isPCM, pcmMono, pcmValue, toMono, resampleToFeatureRate,
// gcd and frameImage are GONE FROM HERE, not deleted: those functions moved
// into github.com/go-perceptual/media, and so did their tests, which are
// stronger there. Each property was checked present before this file lost it:
//
//	isPCM names only PCM                        -> TestIsPCMNamesOnlyPCM
//	every PCM width scales to unit              -> TestPCMValueScalesToUnit
//	an underivable width is refused             -> TestPCMValueRefusesWhatItCannotRead
//	the timescale invariant, at four rates      -> TestWAVTimescaleIsItsSampleRate
//	the downmix AVERAGES, never takes channel 1 -> TestDownmixAveragesRatherThanTakingTheFirstChannel
//	every sample format is handled              -> TestDownmixHandlesEachSampleFormat
//	the resampler CLAMPS instead of wrapping    -> TestResampleClampsRatherThanWrapping
//	frameImage refuses 10-bit, names the depth  -> TestFrameImageRefusesWhatItCannotHashHonestly
//	frameImage wraps each chroma layout         -> TestFrameImageWrapsEachChromaLayout
//
// And media tests two things this file never did, because they needed a real
// file rather than a byte slice: every PCM sample WIDTH through a generated
// WAV — 8, 16, 24, 32-bit and float32, with the sample COUNT asserted, which
// is what catches a bad width derivation — and that frame index n at 15 fps
// holds the same picture as index 2n at 30 fps.
//
// What remains below is what is this tool's own: that a WAV reaches the
// grouping at all, and that --audio-seconds bounds the read.

// writeWAV emits 16-bit stereo PCM as a RIFF/WAVE file.
//
// Generated rather than committed: a WAV is two orders of magnitude larger
// than its encoded equivalents.
func writeWAV(t *testing.T, path string, mono []int16, rate int) {
	t.Helper()
	dataBytes := len(mono) * 2 * 2

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := func(v any) {
		if err := binary.Write(f, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	f.WriteString("RIFF")
	w(uint32(36 + dataBytes))
	f.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1)) // linear PCM
	w(uint16(2)) // channels
	w(uint32(rate))
	w(uint32(rate * 2 * 2))
	w(uint16(4))  // block align
	w(uint16(16)) // bits per sample
	f.WriteString("data")
	w(uint32(dataBytes))
	for _, s := range mono {
		w(s)
		w(s)
	}
}

// melody renders chords in sequence, one second each, with the phase running
// continuously so a chord change is the only discontinuity.
//
// The frequencies sit away from the chroma band edges: the bands are centred
// on the equal-tempered semitones above A0 = 27.5 Hz, so a tone on a boundary
// splits its energy between two bands and a probe ends up measuring the band
// geometry instead of the thing under test.
func melody(chords [][]float64, rate int) []int16 {
	out := make([]int16, 0, rate*len(chords))
	phase := 0
	for _, freqs := range chords {
		for i := 0; i < rate; i++ {
			var v float64
			for _, f := range freqs {
				v += math.Sin(2 * math.Pi * f * float64(phase) / float64(rate))
			}
			out = append(out, int16(v/float64(len(freqs))*0.7*32767))
			phase++
		}
	}
	return out
}

func fourChords() [][]float64 {
	return [][]float64{
		{261.10, 329.30, 391.60},
		{293.30, 369.60, 440.90},
		{329.30, 415.00, 493.30},
		{349.60, 440.90, 523.60},
	}
}

// TestAWAVReachesTheGrouping is the end-to-end check that the PCM
// passthrough is wired in here, not merely present in media.
//
// ⛔ go-avkit's decoder refuses PCM — rightly, there is nothing to decode —
// so without the passthrough a folder of WAVs reports "no duplicates found",
// which is indistinguishable from a folder with no duplicates in it.
func TestAWAVReachesTheGrouping(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.wav")
	writeWAV(t, p, melody(fourChords(), 44100), 44100)

	if k, err := classify(p); err != nil || k != KindAudio {
		t.Fatalf("classify = %q, %v; want audio", k, err)
	}
	features, err := audioFeatures(p, audioSecondsDefault)
	if err != nil {
		t.Fatalf("reading a WAV: %v", err)
	}
	if len(features) == 0 {
		t.Fatal("no features from four seconds of audio")
	}
	for i, f := range features {
		if len(f) != 12 {
			t.Fatalf("frame %d has %d bands, want 12", i, len(f))
		}
	}

	// And through the whole tool: the same music as a WAV and as a FLAC must
	// group, which exercises the passthrough and a real decoder against each
	// other.
	copyFixture(t, "testdata/melody.flac", filepath.Join(dir, "b.flac"))
	out, err := runCLI(t, dir, "--only", "audio")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "SAME RECORDING, DIFFERENT BYTES — 1 group(s)") {
		t.Fatalf("a WAV and a FLAC of the same music were not grouped:\n%s", out)
	}
}

// TestAudioSecondsBoundsWhatIsRead covers the limit that keeps a tree of
// albums from being held in memory at once.
func TestAudioSecondsBoundsWhatIsRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "long.wav")
	writeWAV(t, p, melody(fourChords(), 44100), 44100) // four seconds

	long, err := audioFeatures(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	short, err := audioFeatures(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(short) >= len(long) {
		t.Errorf("--audio-seconds did not bound the read: 1 s gave %d frames, 4 s gave %d",
			len(short), len(long))
	}
	// Roughly chroma.SampleRate/chroma.Hop frames a second; the bound should
	// land near that rather than anywhere at all.
	perSecond := float64(chroma.SampleRate) / float64(chroma.Hop)
	if float64(len(short)) > 2*perSecond {
		t.Errorf("one second gave %d frames, far more than the ~%.0f expected", len(short), perSecond)
	}
}

// TestTheWrappersRefuseTheWrongMedium: audioFeatures and hashVideoFile are
// this repository's thin wrappers, and a wrapper that swallowed an error
// would turn a missing track into an empty result.
func TestTheWrappersRefuseTheWrongMedium(t *testing.T) {
	if _, err := audioFeatures("testdata/clip-360p.mp4", audioSecondsDefault); err == nil {
		t.Error("a silent video yielded audio features")
	}
	if _, err := hashVideoFile("testdata/melody.flac", videoSecondsDefault, false); err == nil {
		t.Error("an audio file yielded a video hash")
	}
	if _, err := audioFeatures(filepath.Join(t.TempDir(), "absent.wav"), audioSecondsDefault); err == nil {
		t.Error("a missing file yielded audio features")
	}
	if _, err := hashVideoFile(filepath.Join(t.TempDir(), "absent.mp4"), videoSecondsDefault, false); err == nil {
		t.Error("a missing file yielded a video hash")
	}
}
