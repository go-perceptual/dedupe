package find

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-perceptual/chroma"
)

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

// TestAWAVYieldsChromaFeatures: go-avkit's decoder refuses PCM, so the PCM
// passthrough has to be wired in here, not merely present in media. The
// end-to-end grouping of a WAV with a FLAC is asserted in the command's tests.
func TestAWAVYieldsChromaFeatures(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.wav")
	writeWAV(t, p, melody(fourChords(), 44100), 44100)

	if k, err := classify(p); err != nil || k != KindAudio {
		t.Fatalf("classify = %q, %v; want audio", k, err)
	}
	features, err := audioFeatures(p, AudioSecondsDefault)
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
		t.Errorf("AudioSeconds did not bound the read: 1 s gave %d frames, 4 s gave %d",
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
	if _, err := audioFeatures(fixtures+"clip-360p.mp4", AudioSecondsDefault); err == nil {
		t.Error("a silent video yielded audio features")
	}
	if _, err := hashVideoFile(fixtures+"melody.flac", VideoSecondsDefault, false); err == nil {
		t.Error("an audio file yielded a video hash")
	}
	if _, err := audioFeatures(filepath.Join(t.TempDir(), "absent.wav"), AudioSecondsDefault); err == nil {
		t.Error("a missing file yielded audio features")
	}
	if _, err := hashVideoFile(filepath.Join(t.TempDir(), "absent.mp4"), VideoSecondsDefault, false); err == nil {
		t.Error("a missing file yielded a video hash")
	}
}
