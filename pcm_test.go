package main

import (
	"encoding/binary"
	"image"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
	"github.com/go-avkit/decode"
	"github.com/go-perceptual/chroma"
)

// writeWAV emits 16-bit stereo PCM as a RIFF/WAVE file.
//
// A WAV is generated rather than committed because it is two orders of
// magnitude larger than its encoded equivalents, and because the PCM path is
// the one place where the BYTES on disk are the thing under test.
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

// melody renders chords in sequence, one second each, phase running
// continuously so a chord change is the only discontinuity.
//
// The frequencies sit away from the chroma band edges. The bands are centred
// on the equal-tempered semitones above A0 = 27.5 Hz, so a tone on a boundary
// splits its energy between two bands and a probe measures the band geometry
// instead of the thing under test.
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

// TestWAVIsReadThroughThePassthrough is the regression test for a gap this
// tool has to work around.
//
// ⛔ The container demuxes a WAV and reports its codec as linear PCM, and
// go-avkit's decoder refuses it -- rightly, since the samples already ARE
// PCM. Without a passthrough, dedupe cannot see a WAV at all, and a folder of
// WAVs would report "no duplicates found".
func TestWAVIsReadThroughThePassthrough(t *testing.T) {
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
}

// TestWAVTimescaleIsItsSampleRate guards the assumption the derivation of the
// PCM sample width rests on.
//
// ⛔ TrackConfig carries no width, so pcmMono derives it from a chunk's byte
// length divided by its frame count -- valid only while a sample's Duration
// counts FRAMES, which holds only while the track's timescale IS its sample
// rate. pcmMono asserts that at run time and refuses otherwise; this says so
// out loud when it breaks, instead of leaving every WAV silently unreadable.
func TestWAVTimescaleIsItsSampleRate(t *testing.T) {
	for _, rate := range []int{8000, 22050, 44100, 48000} {
		p := filepath.Join(t.TempDir(), "probe.wav")
		writeWAV(t, p, melody(fourChords()[:1], rate), rate)

		r, closeFile, err := container.OpenFile(p)
		if err != nil {
			t.Fatalf("%d Hz: %v", rate, err)
		}
		tracks := r.File().Tracks
		if len(tracks) != 1 {
			closeFile()
			t.Fatalf("%d Hz: %d tracks, want 1", rate, len(tracks))
		}
		cfg, err := r.TrackConfig(tracks[0].ID)
		if err != nil {
			closeFile()
			t.Fatal(err)
		}
		if !isPCM(cfg.Codec) {
			t.Errorf("%d Hz: codec %q is not one the passthrough handles", rate, cfg.Codec)
		}
		if cfg.Timescale != uint32(cfg.SampleRate) {
			closeFile()
			t.Fatalf("%d Hz: timescale %d is no longer the sample rate %d -- the PCM width can no longer be derived",
				rate, cfg.Timescale, cfg.SampleRate)
		}
		mono, err := pcmMono(r, tracks[0], cfg, audioSecondsDefault)
		closeFile()
		if err != nil {
			t.Fatalf("%d Hz: %v", rate, err)
		}
		if len(mono) != rate {
			t.Errorf("%d Hz: %d samples from one second, want %d", rate, len(mono), rate)
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

func TestIsPCMNamesOnlyPCM(t *testing.T) {
	for _, c := range []string{"ipcm", "fpcm", "raw "} {
		if !isPCM(c) {
			t.Errorf("isPCM(%q) is false", c)
		}
	}
	for _, c := range []string{"mp4a", "Opus", "fLaC", "alac", ""} {
		if isPCM(c) {
			t.Errorf("isPCM(%q) is true", c)
		}
	}
}

// TestPCMValueScalesToUnit checks each width against its full-scale value: a
// wrong divisor reads as a quieter recording that still hashes.
func TestPCMValueScalesToUnit(t *testing.T) {
	for _, c := range []struct {
		name, codec string
		b           []byte
		width       int
		want        float64
	}{
		{"16-bit max", "ipcm", []byte{0xff, 0x7f}, 2, 32767.0 / 32768},
		{"16-bit min", "ipcm", []byte{0x00, 0x80}, 2, -1},
		{"16-bit zero", "ipcm", []byte{0x00, 0x00}, 2, 0},
		{"24-bit max", "ipcm", []byte{0xff, 0xff, 0x7f}, 3, 8388607.0 / 8388608},
		{"24-bit min", "ipcm", []byte{0x00, 0x00, 0x80}, 3, -1},
		{"24-bit minus one", "ipcm", []byte{0xff, 0xff, 0xff}, 3, -1.0 / 8388608},
		{"32-bit min", "ipcm", []byte{0, 0, 0, 0x80}, 4, -1},
		{"8-bit silence", "raw ", []byte{128}, 1, 0},
		{"8-bit min", "raw ", []byte{0}, 1, -1},
		{"8-bit max", "raw ", []byte{255}, 1, 127.0 / 128},
		{"float32 one", "fpcm", []byte{0, 0, 0x80, 0x3f}, 4, 1},
		{"float64 one", "fpcm", []byte{0, 0, 0, 0, 0, 0, 0xf0, 0x3f}, 8, 1},
	} {
		got, err := pcmValue(c.codec, c.b, c.width)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestPCMValueRefusesWhatItCannotRead is the negative witness for the guard
// above.
//
// ⛔ Without it the refusal branch is code no test executes: every WAV in the
// corpus is 16-bit, so the derivation always succeeds and the error message
// and its condition are both unverified.
func TestPCMValueRefusesWhatItCannotRead(t *testing.T) {
	if _, err := pcmValue("ipcm", []byte{0, 0, 0, 0, 0, 0}, 6); err == nil {
		t.Error("a 6-byte integer sample was accepted")
	}
	if _, err := pcmValue("fpcm", []byte{0, 0}, 2); err == nil {
		t.Error("a 2-byte float sample was accepted")
	}
	if _, err := pcmValue("mp4a", []byte{0, 0}, 2); err == nil {
		t.Error("a codec that is not PCM was accepted")
	}
}

// --- the shared downmix and resampler -----------------------------------

func TestToMonoAveragesRatherThanTakingTheFirstChannel(t *testing.T) {
	// Left at full scale, right silent. Taking channel one gives 1.0;
	// averaging gives 0.5, and averaging is what this tool must do -- a track
	// whose content sits in one channel would otherwise be compared against
	// silence, and silence matches other silence.
	got, err := toMono(decode.Audio{
		Channels: 2, Format: decode.SampleS16,
		Data: []byte{0xff, 0x7f, 0x00, 0x00},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d samples, want 1", len(got))
	}
	if want := (32767.0 / 32768) / 2; got[0] != want {
		t.Errorf("got %v, want %v -- a downmix that is not an average", got[0], want)
	}
}

func TestToMonoHandlesEachSampleFormat(t *testing.T) {
	for _, c := range []struct {
		name   string
		format decode.SampleFormat
		data   []byte
		want   float64
	}{
		{"S16", decode.SampleS16, []byte{0x00, 0x40}, 0.5},
		{"S32", decode.SampleS32, []byte{0, 0, 0, 0x40}, 0.5},
		{"F32", decode.SampleF32, []byte{0, 0, 0, 0x3f}, 0.5},
	} {
		got, err := toMono(decode.Audio{Channels: 1, Format: c.format, Data: c.data})
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: got %v, want [%v]", c.name, got, c.want)
		}
	}
}

func TestToMonoRefusesWhatItCannotRead(t *testing.T) {
	if _, err := toMono(decode.Audio{Channels: 2, Format: decode.SampleFormat(99), Data: make([]byte, 8)}); err == nil {
		t.Error("an unknown sample format was accepted")
	}
	if _, err := toMono(decode.Audio{Channels: 0, Format: decode.SampleS16}); err == nil {
		t.Error("a buffer with no channels was accepted")
	}
}

// TestResampleClampsRatherThanWrapping covers the overshoot guard.
//
// ⛔ A polyphase resampler rings, and its ringing overshoots a full-scale
// signal. Converting that to int16 without clamping WRAPS it to the opposite
// sign, which puts a click into the feature -- content that is not in the
// recording.
func TestResampleClampsRatherThanWrapping(t *testing.T) {
	in := []float64{0, 1.5, -1.5, 1, -1, 0.5}
	out, err := resampleToFeatureRate(in, chroma.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("%d samples out, %d in", len(out), len(in))
	}
	if out[1] != 32767 {
		t.Errorf("1.5 became %d, want 32767", out[1])
	}
	if out[2] != -32767 {
		t.Errorf("-1.5 became %d, want -32767", out[2])
	}
}

func TestResampleRefusesAnImpossibleRate(t *testing.T) {
	if _, err := resampleToFeatureRate([]float64{0, 1}, 0); err == nil {
		t.Error("a track at 0 Hz was accepted")
	}
}

func TestGcd(t *testing.T) {
	for _, c := range [][3]int{
		{11025, 44100, 11025},
		{11025, 48000, 75},
		{11025, 11025, 11025},
		{7, 13, 1},
	} {
		if got := gcd(c[0], c[1]); got != c[2] {
			t.Errorf("gcd(%d, %d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
}

// --- the video frame wrapper --------------------------------------------

// TestFrameImageRefusesWhatItCannotHashHonestly covers the two refusals.
//
// ⛔ A 10-bit frame shifted down to 8 bits WOULD hash, and would hash
// differently from the same film delivered as 8-bit -- so one film would get
// two fingerprints, which is the single thing a deduplicator must not do.
func TestFrameImageRefusesWhatItCannotHashHonestly(t *testing.T) {
	ten := decode.Frame{
		Y: make([]byte, 64*64*2), Cb: make([]byte, 32*32*2), Cr: make([]byte, 32*32*2),
		YStride: 128, CStride: 64, Width: 64, Height: 64,
		Chroma: decode.Chroma420, BitDepth: 10,
	}
	_, err := frameImage(ten)
	if err == nil {
		t.Error("a 10-bit frame was accepted")
	} else if !strings.Contains(err.Error(), "10-bit") {
		t.Errorf("the error does not name the bit depth: %v", err)
	}

	odd := decode.Frame{
		Y: make([]byte, 64*64), Cb: make([]byte, 64*64), Cr: make([]byte, 64*64),
		YStride: 64, CStride: 64, Width: 64, Height: 64,
		Chroma: decode.Chroma(99), BitDepth: 8,
	}
	if _, err := frameImage(odd); err == nil {
		t.Error("an unknown chroma layout was accepted")
	}
}

// TestFrameImageWrapsEachChromaLayout is the positive half: a frame wrapped
// at the wrong ratio reads its chroma planes at the wrong stride and hashes a
// picture nobody filmed.
func TestFrameImageWrapsEachChromaLayout(t *testing.T) {
	for _, c := range []struct {
		chroma decode.Chroma
		cw, ch int
		want   image.YCbCrSubsampleRatio
	}{
		{decode.Chroma420, 32, 32, image.YCbCrSubsampleRatio420},
		{decode.Chroma422, 32, 64, image.YCbCrSubsampleRatio422},
		{decode.Chroma444, 64, 64, image.YCbCrSubsampleRatio444},
	} {
		img, err := frameImage(decode.Frame{
			Y: make([]byte, 64*64), Cb: make([]byte, c.cw*c.ch), Cr: make([]byte, c.cw*c.ch),
			YStride: 64, CStride: c.cw, Width: 64, Height: 64,
			Chroma: c.chroma, BitDepth: 8,
		})
		if err != nil {
			t.Errorf("%v: %v", c.chroma, err)
			continue
		}
		ycbcr, ok := img.(*image.YCbCr)
		if !ok {
			t.Errorf("%v: got %T", c.chroma, img)
			continue
		}
		if ycbcr.SubsampleRatio != c.want {
			t.Errorf("%v: ratio %v, want %v", c.chroma, ycbcr.SubsampleRatio, c.want)
		}
	}
}

func TestAudioAndVideoRefuseAFileWithNoSuchTrack(t *testing.T) {
	// A video with no audio track, asked for its audio, and the reverse.
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
