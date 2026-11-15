# dedupe

Find duplicate and near-duplicate media in a directory tree.

```
dedupe ~/Downloads
```

Two passes:

1. **Identical bytes.** Files are grouped by size, and only same-size files
   are ever read — two files of different lengths cannot have the same bytes.
2. **Same picture, different bytes.** A JPEG re-encoding of a PNG shares no
   bytes with it at all. This pass uses the
   [PDQ perceptual hash](https://github.com/go-perceptual/pdq): distance 0 is
   the same picture, 30 or less is a confident match, unrelated pictures sit
   around 128.

## ⛔ It reports. It does not act.

`dedupe` never deletes, moves or links anything, and it has no flag that
would. The output is a list for a person to act on, and `keep`/`dup` beside
each path is advice, not an action.

That is a decision, not an omission. The second pass is a **heuristic with a
documented false-positive mode** — see *single linkage* below — and a tool
that removes files on the strength of a similarity threshold removes the
wrong file eventually. A test asserts that no acting flag has appeared.

## What it prints

```
scanned 1274 file(s) under 1 root(s)
hashed 1180 image(s)

IDENTICAL BYTES — 3 group(s)
  2.4 MB recoverable
    keep  /home/u/Downloads/plan.png (2.4 MB)
      dup /home/u/Downloads/plan (1).png (2.4 MB)

SAME PICTURE, DIFFERENT BYTES — 1 group(s)
  840.0 KB recoverable, widest distance 8
    keep  /home/u/Downloads/scan.png (1.2 MB)
      dup /home/u/Downloads/scan.jpg (840.0 KB)

3.2 MB recoverable in total, keeping the largest of each group
```

The first line is deliberate: **a scan that found nothing and a scan that
read nothing print the same empty list**, and only the volume read tells them
apart. For the same reason, files it could not decode and images skipped as
too featureless are counted in the output rather than dropped silently.

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

## Flags

| flag | default | |
|---|---|---|
| `--threshold` | 20 | largest perceptual distance still called a duplicate (0–256) |
| `--min-quality` | 50 | ignore images PDQ scores below this |
| `--min-size` | 1024 | ignore files smaller than this |
| `--workers` | one per CPU | parallel hashers |
| `--exact-only` | off | skip the perceptual pass |

`--min-quality` exists because PDQ reports a quality score precisely so that
featureless images can be filtered out: a blank thumbnail hashes like every
other blank thumbnail, and letting them through turns a cache of empty images
into one enormous false "duplicate" group.

## Single linkage, and what it costs

Near-duplicates are clustered by union-find: A joins B's group when A is close
to **any** member, not to all of them. A chain of images each close to the
next can therefore put the two ends in one group although they are far apart.

Rather than hide that, each group reports the **widest** distance inside it,
so a stretched chain is visible in the output.

## Formats

PNG, JPEG and GIF. The extension list sits beside the decoder imports in the
source on purpose: adding a decoder without adding its extension makes the
tool silently ignore that format, which looks exactly like "no duplicates
found".

Audio and video are not handled yet; `go-perceptual/tmk` and
`go-perceptual/chroma` are where that will come from.

## Licence

BSD-3-Clause.
