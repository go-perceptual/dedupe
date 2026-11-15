package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"

	"github.com/go-avkit/avkit/container"
	"github.com/go-avkit/decode"
	"github.com/go-dsp/signal"
	"github.com/go-perceptual/chroma"
)

// AudioThreshold is the chroma score at or above which two recordings are
// reported as the same.
//
// ⛔ 0.9, and the number was MEASURED rather than chosen -- see
// go-perceptual/perceptual, where the same eight-chord melody played
// BACKWARDS scores 0.7064, and with only four chords 0.8176. A chroma feature
// is a profile of which pitch classes are present, so two recordings built
// from the same notes agree substantially even when they are not the same
// piece. The natural-looking 0.7 admits a reversed recording.
const AudioThreshold = 0.9

// audioSecondsDefault bounds how much of each recording is compared.
//
// ⛔ A bound is necessary, not a preference. Chroma features are roughly 97
// float64 a second (8.077 frames of 12 bands), so a five-minute track is
// about 230 KB and a tree of two thousand tracks would be 460 MB held at
// once, before any comparison starts. The tool would be killed by the
// kernel rather than report anything.
//
// ⚠ What this costs, stated rather than hidden: two recordings that differ
// only AFTER the window are reported as the same, and a clip taken from
// beyond the window does not match its source. Raise --audio-seconds when
// that matters; the memory grows linearly with it.
const audioSecondsDefault = 180

// audioFeature is one recording's chroma profile.
type audioFeature struct {
	File
	features [][]float64
}

var errNoAudioFeatures = errors.New("too short to produce a chroma feature")

// audioFeatures decodes one file and returns its chroma features, reading at
// most maxSeconds of it.
func audioFeatures(path string, maxSeconds int) ([][]float64, error) {
	r, closeFile, err := container.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer closeFile()

	id, err := decode.FirstAudioTrack(r)
	if err != nil {
		return nil, err
	}
	cfg, err := r.TrackConfig(id)
	if err != nil {
		return nil, err
	}

	var mono []float64
	var rate int

	if isPCM(cfg.Codec) {
		// A WAV's samples already ARE PCM, so go-avkit's decoder refuses them
		// -- rightly, there is nothing to decode. See pcm.go.
		var tr container.Track
		for _, t := range r.File().Tracks {
			if t.ID == id {
				tr = t
			}
		}
		mono, err = pcmMono(r, tr, cfg, maxSeconds)
		if err != nil {
			return nil, err
		}
		rate = cfg.SampleRate
	} else {
		buffers, err := decode.DecodeAudioTrack(r, id, decode.AudioConfig{})
		if err != nil {
			return nil, err
		}
		if len(buffers) == 0 {
			return nil, errNoAudioFeatures
		}
		rate = buffers[0].SampleRate
		limit := maxSeconds * rate
		for _, b := range buffers {
			if b.SampleRate != rate {
				// ⛔ Refused rather than resampled piecewise: splicing
				// separately resampled blocks introduces exactly the
				// discontinuities the feature would then report as content.
				return nil, fmt.Errorf("sample rate changes mid-track (%d then %d)", rate, b.SampleRate)
			}
			m, err := toMono(b)
			if err != nil {
				return nil, err
			}
			mono = append(mono, m...)
			if len(mono) >= limit {
				mono = mono[:limit]
				break
			}
		}
	}

	if len(mono) == 0 {
		return nil, errNoAudioFeatures
	}
	pcm, err := resampleToFeatureRate(mono, rate)
	if err != nil {
		return nil, err
	}
	features, err := chroma.Features(pcm)
	if err != nil {
		return nil, err
	}
	if len(features) == 0 {
		return nil, errNoAudioFeatures
	}
	return features, nil
}

// toMono averages the channels of one buffer into samples in [-1, 1].
//
// Averaging, not taking the first channel: a track whose content sits in one
// channel -- an interview, a badly mixed rip -- would otherwise be compared
// against silence, and silence matches other silence.
func toMono(a decode.Audio) ([]float64, error) {
	n := a.Samples()
	ch := a.Channels
	if ch < 1 {
		return nil, fmt.Errorf("a buffer with %d channels", ch)
	}
	out := make([]float64, n)
	switch a.Format {
	case decode.SampleS16:
		for i := 0; i < n; i++ {
			var sum float64
			for c := 0; c < ch; c++ {
				off := (i*ch + c) * 2
				sum += float64(int16(binary.LittleEndian.Uint16(a.Data[off:]))) / 32768
			}
			out[i] = sum / float64(ch)
		}
	case decode.SampleS32:
		for i := 0; i < n; i++ {
			var sum float64
			for c := 0; c < ch; c++ {
				off := (i*ch + c) * 4
				sum += float64(int32(binary.LittleEndian.Uint32(a.Data[off:]))) / 2147483648
			}
			out[i] = sum / float64(ch)
		}
	case decode.SampleF32:
		for i := 0; i < n; i++ {
			var sum float64
			for c := 0; c < ch; c++ {
				off := (i*ch + c) * 4
				sum += float64(math.Float32frombits(binary.LittleEndian.Uint32(a.Data[off:])))
			}
			out[i] = sum / float64(ch)
		}
	default:
		return nil, fmt.Errorf("sample format %v is not handled", a.Format)
	}
	return out, nil
}

// resampleToFeatureRate brings mono audio to the rate the chroma feature is
// defined at.
//
// ⛔ The rate is part of the FEATURE, not a preference: the frame geometry and
// the frequency bounds are expressed in samples at that rate, so audio fed at
// another rate produces a perfectly plausible fingerprint that matches
// nothing.
func resampleToFeatureRate(mono []float64, rate int) ([]int16, error) {
	if rate < 1 {
		return nil, fmt.Errorf("a track at %d Hz", rate)
	}
	if rate != chroma.SampleRate {
		g := gcd(chroma.SampleRate, rate)
		out, err := signal.ResamplePoly(mono, chroma.SampleRate/g, rate/g, nil)
		if err != nil {
			return nil, fmt.Errorf("resampling %d Hz to %d Hz: %w", rate, chroma.SampleRate, err)
		}
		mono = out
	}
	pcm := make([]int16, len(mono))
	for i, v := range mono {
		// Clamp: a resampler's ringing overshoots a full-scale signal, and
		// wrapping that to the opposite sign puts a click into the feature --
		// content that is not in the recording.
		switch {
		case v > 1:
			v = 1
		case v < -1:
			v = -1
		}
		pcm[i] = int16(v * 32767)
	}
	return pcm, nil
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// hashAudio computes the chroma features of every audio file, in parallel.
func hashAudio(files []File, maxSeconds, workers int) ([]audioFeature, int, []error) {
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	type outcome struct {
		f       audioFeature
		skipped bool
		err     error
	}
	in := make(chan File)
	out := make(chan outcome)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range in {
				feat, err := audioFeatures(f.Path, maxSeconds)
				switch {
				case errors.Is(err, errNoAudioFeatures):
					out <- outcome{skipped: true}
				case err != nil:
					out <- outcome{err: fmt.Errorf("%s: %w", f.Path, err)}
				default:
					out <- outcome{f: audioFeature{File: f, features: feat}}
				}
			}
		}()
	}
	go func() {
		for _, f := range files {
			in <- f
		}
		close(in)
		wg.Wait()
		close(out)
	}()

	var hs []audioFeature
	var errs []error
	skipped := 0
	for o := range out {
		switch {
		case o.skipped:
			skipped++
		case o.err != nil:
			errs = append(errs, o.err)
		default:
			hs = append(hs, o.f)
		}
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].Path < hs[j].Path })
	return hs, skipped, errs
}

// audioGroups clusters recordings that score at or above threshold.
//
// Single-linkage by union-find, like the image pass, and with the same
// consequence: a chain of recordings each close to the next can put the two
// ends in one group. The group reports the LOWEST pairwise score inside it, so
// a stretched chain is visible rather than hidden.
func audioGroups(hs []audioFeature, threshold float64, claimed map[string]bool) []Group {
	var todo []audioFeature
	for _, h := range hs {
		if !claimed[h.Path] {
			todo = append(todo, h)
		}
	}

	uf := newUnionFind(len(todo))
	for i := 0; i < len(todo); i++ {
		for j := i + 1; j < len(todo); j++ {
			if chroma.Compare(todo[i].features, todo[j].features) >= threshold {
				uf.union(i, j)
			}
		}
	}

	var groups []Group
	for _, idx := range uf.groups() {
		g := Group{Kind: "audio"}
		for _, i := range idx {
			g.Files = append(g.Files, todo[i].File)
		}
		worst := 1.0
		for a := 0; a < len(idx); a++ {
			for b := a + 1; b < len(idx); b++ {
				if s := chroma.Compare(todo[idx[a]].features, todo[idx[b]].features); s < worst {
					worst = s
				}
			}
		}
		g.Score = worst
		sortGroup(&g)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Recoverable() > groups[j].Recoverable()
	})
	return groups
}
