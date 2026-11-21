# dedupe

Find duplicate and near-duplicate media in a directory tree — pictures,
recordings and video.

```
dedupe ~/Downloads
```

Every file is classified by **reading** it, then four passes run:

1. **Identical bytes.** Files are grouped by size, and only same-size files
   are ever read — two files of different lengths cannot have the same bytes.
2. **Same picture, different bytes.** A JPEG re-encoding of a PNG shares no
   bytes with it at all. Uses the
   [PDQ perceptual hash](https://github.com/go-perceptual/pdq): distance 0 is
   the same picture, 30 or less a confident match, unrelated pictures sit
   around 128.
3. **Same recording, different bytes.** The same track as FLAC, MP3, Opus and
   AAC. Uses [chroma](https://github.com/go-perceptual/chroma): 0.9 or more is
   the same recording.
4. **Same video, different bytes.** A 4K release and its 1080p encode. Uses
   [TMK+PDQF](https://github.com/go-perceptual/tmk): 0.7 or more is the same
   video.

## It never trusts a filename

An extension is a claim by whoever named the file, and the copy this tool
exists to find is often precisely the one that was renamed. Classification
reads the bytes: a film saved as `backup.dat`, a photograph renamed `.thumb`,
an MP3 with no extension at all.

That is not a refinement — it is the fix for a defect. This tool used to
filter on an extension list, so those files were invisible, and **in a
deduplicator an invisible file reads as "no duplicates found"**, which is
indistinguishable from a clean tree.

Measured on a tree holding four encodings of one melody and three scales of
one clip, with two of them deliberately misnamed:

```
walked 11 file(s) under 1 root(s)
classified by content: 2 image(s), 5 recording(s), 4 video(s); 0 not media

SAME RECORDING, DIFFERENT BYTES — 1 group(s)
  133.6 KB recoverable, weakest pair 1.0000
    keep  song.flac (66.0 KB)
      dup renamed-audio.dat (48.3 KB)      ← an M4A called .dat
      dup sub/song-copy.mp3 (47.8 KB)
      dup song.opus (37.5 KB)

SAME VIDEO, DIFFERENT BYTES — 1 group(s)
  21.6 KB recoverable, weakest pair 0.9745
    keep  film.mp4 (56.3 KB)
      dup sub/film-small.mp4 (14.7 KB)
      dup no-extension (6.8 KB)            ← an MP4 with no extension
```

The cost is one open and one 64 KiB read per file in the tree. That is what
buys an answer about the tree rather than about its naming conventions.

## ⛔ It reports. It does not act.

`dedupe` never deletes, moves or links anything, and it has no flag that
would. The output is a list for a person to act on, and `keep`/`dup` beside
each path is advice, not an action.

That is a decision, not an omission. The perceptual passes are **heuristics
with documented false-positive modes** — see *single linkage* below — and a
tool that removes files on the strength of a similarity threshold removes the
wrong file eventually. A test asserts that no acting flag has appeared.

## What the first lines are for

**A scan that found nothing and a scan that read nothing print the same empty
list**, and only the volume read tells them apart. The same reasoning covers
the rest of the counts:

- files that are **not media** are counted, not dropped. If a classifier broke
  and called everything "other", the report would be empty and would read as a
  clean tree.
- files it could not **read** are counted and listed, and the report says
  `INCOMPLETE`. A truncated download is refused by the container rather than
  decoded, and that refusal is surfaced instead of swallowed.
- images skipped as too **featureless** are counted, because a silent skip is
  a lie by omission.

## ⚠ A tree of source checkouts is not a download folder

Measured on a working tree of 7419 images across several hundred git clones:
**1778 identical groups, 452 perceptual ones, 815 MB "recoverable"**. Every
figure is correct, and acting on almost any of them would break something.

The largest group was one committed test fixture, `testdata/renders/
rfc1866.png`, present in fifteen worktrees of the **same repository**. Those
files are duplicates in the only sense this tool can see — identical bytes —
and they are each exactly where they belong.

So: point it at `~/Downloads`, a photo import, a scrape. Over checkouts,
build outputs or anything version-controlled it will faithfully report the
copies that are supposed to be there. This is also why it never deletes.

## Use as a library

The grouping behind the command is importable, so a program with a window can
mark duplicate files without running `dedupe` and parsing its text:

```go
import "github.com/go-perceptual/dedupe/find"

o := find.DefaultOptions() // the flag defaults of the command, exactly
o.Only = find.KindImage    // optional: one pass only
res, err := find.Find(ctx, []string{"/photos"}, o)
if err != nil {
	return err // a missing root, an invalid option, or ctx cancelled
}
for _, g := range res.Exact { // also res.Near, res.Audio, res.Video
	// g.Files[0] is the one to keep; g.Recoverable() is what the rest free.
}
```

`Result` carries the volume read (`Walked`, `Images`, `Recordings`, `Videos`,
`NotMedia`, `Hashed`, `Skipped`) and the errors (`WalkErrors`, `ReadErrors`)
beside the groups. ⛔ A non-empty error list means the groups are INCOMPLETE,
exactly as the command's report says: check it before reading "no duplicates"
as an answer.

Cancelling `ctx` stops the scan between files and returns `ctx.Err()` with no
partial result, since a half-scanned tree reads as "fewer duplicates". The
library, like the command, reports and never changes anything on disk.
`go install github.com/go-perceptual/dedupe@latest` installs the command as
before.

## Flags

| flag | default | |
|---|---|---|
| `--threshold` | 20 | largest PDQ distance still called the same picture (0–256) |
| `--min-quality` | 50 | ignore images PDQ scores below this |
| `--audio-threshold` | 0.9 | chroma score at or above which two recordings are the same |
| `--video-threshold` | 0.7 | TMK score at or above which two videos are the same |
| `--audio-seconds` | 180 | compare at most this much of each recording |
| `--video-seconds` | 180 | compare at most this much of each video |
| `--only` | all | run one pass only: `image`, `audio` or `video` |
| `--min-size` | 1024 | ignore files smaller than this |
| `--workers` | one per CPU | parallel hashers |
| `--exact-only` | off | skip every perceptual pass |

`--min-quality` exists because PDQ reports a quality score precisely so that
featureless images can be filtered out: a blank thumbnail hashes like every
other blank thumbnail, and letting them through turns a cache of empty images
into one enormous false "duplicate" group.

### Why the audio threshold is 0.9 and not 0.7

Measured, not chosen. A chroma feature is a profile of which pitch classes are
present, so two recordings built from the same notes agree substantially even
when they are not the same piece. The same eight-chord melody played
**backwards** scores **0.7064** — the natural-looking 0.7 admits it. With only
**four** chords it scores **0.8176**, because fewer distinct chords means more
of the piece overlaps itself, so the real margin above 0.9 is 0.08 and 0.85
would be wrong too.

### Why the windows exist

`--audio-seconds` and `--video-seconds` are bounds, not preferences.

Chroma features run at roughly 97 float64 a second, so a five-minute track is
about 230 KB and a tree of two thousand tracks would be 460 MB held at once
before any comparison started. Video is bounded for **time** rather than
memory: every frame of every video is resampled to 15 fps and put through PDQ,
and an unbounded pass over a folder of feature films decodes for hours before
printing anything.

⚠ What they cost, stated rather than hidden: two files that differ only
**after** the window are reported as the same, and a clip taken from beyond
the window does not match its source.

## How the video pass stays in memory

TMK's level-2 features are 262 KB a video against 1 KB for level 1, so holding
them for a whole tree is what would make this tool unusable on a real download
folder — five hundred videos is 131 MB before any comparison.

TMK's own documentation calls the level-1 feature *"what a large index
stores"*, and that is the design used as intended: the sweep keeps level 1
only, and level 2 is computed for the pairs that survive. The cost is decoding
a candidate twice, which is the right trade — a tree where few videos match
barely pays it.

The prefilter gate is 0.5, deliberately far below the 0.7 decision, because a
prefilter that sits at the decision threshold starts **making** the decision.
It is a real recall risk rather than a free optimisation, so it is measured:

| level-1 score | |
|---|---|
| a clip vs its half-scale copy | 0.9851 |
| a clip vs its quarter-scale copy | 0.9881 |
| a clip vs an unrelated clip | −0.0829 |

A test fails if a true match's margin above the gate disappears, and fails if
the gate stops rejecting unrelated material — a prefilter that rejects nothing
has not saved anything.

## Single linkage, and what it costs

Near-duplicates are clustered by union-find, shared by all three perceptual
passes: A joins B's group when A is close to **any** member, not to all of
them. A chain of items each close to the next can therefore put the two ends
in one group although they are far apart.

Rather than hide that, each group reports its **worst** pair — the widest
distance for pictures, the lowest score for recordings and video — so a
stretched chain is visible in the output.

## Formats

Pictures: PNG, JPEG and GIF, through Go's own decoders.

Recordings and video come from [go-avkit](https://github.com/go-avkit): MP4,
Matroska/WebM, MPEG-TS, Ogg, WAV, AVI, native FLAC, ADTS and MPEG audio;
H.264 and H.265 video; FLAC, MP3, Opus, AAC-LC and HE-AAC audio.

A WAV holds samples that already **are** PCM, so `go-avkit/decode` refuses
them — rightly, since there is nothing to decode. This tool reads PCM tracks
straight through. The sample width is not carried by the track configuration,
so it is derived from a chunk's byte length and frame count, which holds only
while the track's timescale is its sample rate; that equality is asserted at
run time and checked by a test at four sample rates, because PCM read at the
wrong width is noise that still hashes.

Still missing from the decoders: VP9, AV1, Vorbis and MP2. A file in one of
those is **reported as unreadable, not skipped** — the report says
`INCOMPLETE` and names it. That matters more than it sounds: a deduplicator
that quietly dropped every VP9 in a tree would print a clean list of the rest,
and nothing would tell you a whole codec had been ignored.

⚠ The message is currently misleading, and is reported upstream. A VP9 file
produces

```
decode: invalid sample: NAL unit of 2185855810 bytes with 2939 left
```

which is an H.264 NAL-unit parser being handed VP9 data: the decoder does not
check the codec before parsing, so it reads VP9 bytes as a length prefix and
calls the file **corrupt** instead of saying the codec is unsupported. The
length is bounds-checked, so this is a wrong diagnosis rather than an
unbounded allocation — but it sends you to inspect a file that is fine.

## Test fixtures

Committed so CI runs the real decoders on a machine with no ffmpeg, and their
provenance is written down here rather than remembered.

The audio fixtures are four encodings of one four-second, four-chord melody;
the video fixtures are a two-second clip and **scaled copies of that clip**,
which is what a 4K and a 1080p release of one film are. WAVs are generated
inside the tests, since a WAV is two orders of magnitude larger than its
encoded equivalents and the PCM path is the one place where the bytes on disk
are the thing under test.

```console
ffmpeg -i melody-source.wav -c:a flac -compression_level 12 testdata/melody.flac
ffmpeg -i melody-source.wav -c:a libmp3lame -b:a 96k  testdata/melody.mp3
ffmpeg -i melody-source.wav -c:a libopus   -b:a 64k   testdata/melody.opus
ffmpeg -i melody-source.wav -c:a aac       -b:a 96k   testdata/melody.m4a

ffmpeg -f lavfi -i "testsrc2=size=640x360:rate=15:duration=2" \
  -c:v libx264 -preset veryslow -crf 30 -pix_fmt yuv420p testdata/clip-360p.mp4
ffmpeg -i testdata/clip-360p.mp4 -vf "scale=320:180:flags=lanczos" \
  -c:v libx264 -preset veryslow -crf 30 -pix_fmt yuv420p testdata/clip-180p.mp4
ffmpeg -i testdata/clip-360p.mp4 -vf "scale=160:90:flags=lanczos" \
  -c:v libx264 -preset veryslow -crf 32 -pix_fmt yuv420p testdata/clip-90p.mp4
ffmpeg -f lavfi -i "smptebars=size=640x360:rate=15:duration=2" \
  -c:v libx264 -preset veryslow -crf 30 -pix_fmt yuv420p testdata/clip-other.mp4
ffmpeg -f lavfi -i "testsrc2=size=160x90:rate=15:duration=1" \
  -c:v libvpx-vp9 -b:v 50k -pix_fmt yuv420p testdata/unsupported-vp9.webm
```

⚠ `clip-180p.mp4` is derived from `clip-360p.mp4`, **not** rendered
independently at that size, and that detail is load-bearing. `testsrc2`
rendered independently at 640×360 and 320×180 scores **0.3467**, because the
filter draws its pattern parametrically for the target size — different cell
sizes, different text — so the two clips genuinely hold different pictures.
That looked like a hash defect and was a fixture defect.

## Licence

BSD-3-Clause.
