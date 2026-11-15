package main

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"

	"github.com/go-avkit/avkit/container"
)

// Kind is what a file turned out to hold.
type Kind string

const (
	KindImage Kind = "image"
	KindAudio Kind = "audio"
	KindVideo Kind = "video"
	KindOther Kind = "" // not media this tool can compare
)

// headerBytes is how much of a file the classifier reads.
//
// Enough for every signature container.Sniff looks at, and for the
// configuration header of a JPEG, PNG or GIF. MPEG-TS is the greediest of
// them: its sync byte recurs every 188 bytes and a sniffer wants several
// packets before believing it, so this is generous rather than minimal.
const headerBytes = 64 << 10

// classify says what a file holds, by READING it.
//
// ⛔ Not by its extension. An extension is a claim by whoever named the file,
// and a download folder is full of wrong ones: a .jpg holding a PNG, an .mp4
// holding only audio, a video saved with no extension at all. A tool that
// believes the name reports a confident wrong answer, and in a DEDUPLICATOR
// that answer is "no duplicates found" — indistinguishable from a clean tree.
//
// It is done in two stages because a tree scan cannot afford to read every
// file whole. The header alone settles images and the audio-only containers.
// Only a container that could hold either gets opened properly, and that open
// STREAMS rather than slurping the file, which matters when the candidate is
// a 4 GB film.
func classify(path string) (Kind, error) {
	f, err := os.Open(path)
	if err != nil {
		return KindOther, err
	}
	head := make([]byte, headerBytes)
	n, err := f.Read(head)
	closeErr := f.Close()
	if n == 0 {
		if err != nil {
			return KindOther, err
		}
		return KindOther, closeErr
	}
	head = head[:n]

	// Images first: cheap, definitive, and decided entirely by the header.
	if _, _, err := image.DecodeConfig(bytes.NewReader(head)); err == nil {
		return KindImage, nil
	}

	switch container.Sniff(head) {
	case container.FormatUnknown:
		return KindOther, nil
	case container.FormatADTS, container.FormatMPEGAudio, container.FormatFLAC, container.FormatWAV:
		// These carry audio and nothing else, so the header has answered.
		return KindAudio, nil
	}

	// MP4, Matroska, MPEG-TS, AVI and Ogg can hold either, so the track list
	// has to be read. OpenFile streams.
	r, closeFile, err := container.OpenFile(path)
	if err != nil {
		return KindOther, err
	}
	defer closeFile()

	hasVideo, hasAudio := false, false
	for _, tr := range r.File().Tracks {
		switch tr.Kind {
		case container.Video:
			hasVideo = true
		case container.Audio:
			hasAudio = true
		}
	}
	switch {
	case hasVideo:
		// ⛔ A file with both is VIDEO, not audio. Its pictures are the
		// stronger evidence -- two different films can share a soundtrack,
		// and the same film in two languages shares its pictures -- and
		// comparing a film against a song on the strength of its audio track
		// would group things no one would call duplicates.
		return KindVideo, nil
	case hasAudio:
		return KindAudio, nil
	}
	return KindOther, nil
}
