package main

import (
	"github.com/go-perceptual/media"
)

// Kind is what a file turned out to hold.
//
// ⛔ An ALIAS for media.Kind, not a type of its own. The --only flag and the
// report both carry these strings, and a second enumeration that had to be
// mapped onto the first is exactly how the two drift apart: a medium added to
// media would be classified correctly and then fall into the default arm of
// every switch here, silently.
type Kind = media.Kind

const (
	KindImage = media.KindImage
	KindAudio = media.KindAudio
	KindVideo = media.KindVideo
	KindOther = media.KindOther
)

// classify says what a file holds, by reading it. See media.Classify for why
// an extension is not trusted, and for how little of the file it needs.
func classify(path string) (Kind, error) { return media.Classify(path) }
