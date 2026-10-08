# TUI width evaluation (#3)

Compared `github.com/rivo/uniseg v0.4.7` (atto) with
`github.com/charmbracelet/x/ansi v0.11.8`, in a temporary module outside the
repository. No dependency was added. The regression cases live in
`tui/grapheme_test.go`.

## Expected policy and terminal differences

Unicode UAX #29 defines grapheme boundaries, not terminal columns. UAX #11
provides East Asian width properties; UTS #51 defines emoji presentation and
sequences. The expectations here use wide CJK characters, narrow Ambiguous
characters, two-column fully qualified emoji clusters, and zero-column combining
marks, ZWJ and ZWSP. Text-presentation heart and warning symbols occupy one column;
VS16 requests two-column emoji presentation. Fully qualified keycaps include both
VS16 and U+20E3. Tabs follow atto's documented three-column policy, not physical
terminal tab stops. Cursor controls are ignored when measuring text: the width is
not the final cursor position after executing arbitrary controls.

This was a noninteractive `TERM=dumb` run, **not** a live comparison of kitty,
iTerm2, Terminal.app and Windows Terminal. These are regression expectations for
grapheme-aware, narrow-Ambiguous rendering, not a promise that all versions and
fonts of those four terminals agree:

- iTerm2 and Terminal.app have settings affecting Ambiguous width; Unicode-version
  settings and CJK configurations can also change width decisions. A wide
  Ambiguous policy makes `·Ω─` six columns instead of three.
- Emoji shaping and grapheme measurement vary with terminal version and font
  fallback. Terminals measuring individual code points rather than clusters can
  give ZWJ families/professions, flags and skin-tone sequences more than two
  columns. Windows Terminal's text-measurement mode is relevant here. These
  differences require an actual terminal/version/font matrix to verify.
- Native tabs advance to tab stops (usually every eight columns), unlike atto's
  expansion. Neither a fixed three-column width nor x/ansi's zero-column tab
  measurement models arbitrary terminal tab stops.
- Unqualified keycaps (without VS16), isolated variation selectors and styling
  inserted *inside* an emoji cluster are not assigned universal terminal widths
  by this evaluation. The fix deliberately targets fully qualified keycaps.

## Display-width comparison

`atto` shows before → after where changed. All numbers are columns.

| Case | atto | x/ansi | Expected |
| --- | ---: | ---: | ---: |
| CJK `漢字` | 4 | 4 | 4 |
| Family `👨‍👩‍👧‍👦` | 2 | 2 | 2 |
| Woman technologist `👩‍💻` | 2 | 2 | 2 |
| Man health worker `👨‍⚕️` | 2 | 2 | 2 |
| US flag `🇺🇸` | 2 | 2 | 2 |
| Korea flag `🇰🇷` | 2 | 2 | 2 |
| Skin tone `👍🏽` | 2 | 2 | 2 |
| Profession with skin tone `👩🏽‍💻` | 2 | 2 | 2 |
| Digit keycap `1️⃣` | 1 → 2 | 2 | 2 |
| Hash keycap `#️⃣` | 1 → 2 | 2 | 2 |
| Star keycap `*️⃣` | 1 → 2 | 2 | 2 |
| Combining acute `e` + U+0301 | 1 | 1 | 1 |
| Text heart `❤` | 1 | 1 | 1 |
| Emoji heart `❤️` | 2 | 2 | 2 |
| Text warning `⚠` | 1 | 1 | 1 |
| Emoji warning `⚠️` | 2 | 2 | 2 |
| Ambiguous `·Ω─` | 3 | 3 | 3 |
| ZWJ `x` + U+200D + `y` | 2 | 2 | 2 |
| ZWSP `x` + U+200B + `y` | 2 | 2 | 2 |
| Tab `a\tb` | 5 | 2 | 5 (atto policy) |
| SGR around `👩‍💻` | 2 | 2 | 2 |
| SGR around `1️⃣` | 1 → 2 | 2 | 2 |
| OSC 8 hyperlink around `👩‍💻`, BEL terminators | 2 | 2 | 2 |
| OSC 8 hyperlink around `👩‍💻`, ST terminators | 2 | 2 | 2 |
| OSC 52 embedded between `a` and `b` | 2 | 2 | 2 |
| CSI cursor movement embedded between `a` and `b` | 2 | 2 | 2 (text width) |
| SGR between `e` and U+0301 | 1 | 1 | 1 |

## Truncation, wrapping and stripping

For each table entry with expected width `w`, the probe truncates `text + "XY"`
to `w+1` with tail `…` and wraps the same text at `w`. Comparisons below remove
escapes (including atto's selection wrap marks) to compare displayed text.
Expected truncation is `text + "…"`, with tabs expanded to three spaces.
Expected wrapping is `[text, "XY"]`, or `[text, "X", "Y"]` for width one.

All entries agree with those expectations after the atto fix. x/ansi agrees
except:

| Operation/case | atto before | atto after / expected | x/ansi |
| --- | --- | --- | --- |
| Truncate `1️⃣XY`, width 3 | `1️⃣XY` | `1️⃣…` | `1️⃣X…` (four columns) |
| Wrap `1️⃣XY`, width 2 | `[1️⃣X, Y]` | `[1️⃣, XY]` | `[1️⃣X, Y]` |
| Wrap `éXY`, width 1 | `[é, X, Y]` | `[é, X, Y]` | `[e, ́X, Y]` |
| Wrap `e<SGR>́XY`, width 1 | `[é, X, Y]` | `[é, X, Y]` | `[e, ́X, Y]` |
| Truncate `a\tbXY`, width 6 | `a   b…` | `a   b…` | `a\tbXY` |
| Wrap `a\tbXY`, width 5 | `[a   b, XY]` | `[a   b, XY]` | `[a\tbXY]` |

Hash, star and styled keycaps reproduce the digit-keycap differences.
`StripEscapes` and `ansi.Strip` give identical results for every table entry.
`StripControls` is intentionally different: it removes control characters but
leaves escape payload text visible (e.g. `a<CSI 2C>b` becomes `a[2Cb`). It is an
untrusted-text sanitizer, not an ANSI parser; it retains tabs and newlines.

An additional probe placed SGR inside a ZWJ emoji: `👩<SGR>‍💻`. Atto's width
measurement strips SGR and reports two, while its scanner splits text at escapes
and wraps the sequence in separate pieces; x/ansi reports four and also splits
it. Neither implementation provides escape-transparent emoji segmentation.
That existing limitation is not changed here: actual shaping across an internal
style change needs terminal verification, and replacing the scanner with x/ansi
would not fix it. Editor rune layout and shimmer rune styling are also outside
this narrowly scoped formatting correction.

## Decision

Keep uniseg and the existing scanner. Correct just fully qualified keycap widths
in `VisibleWidth` and `cellScanner`, preserving the ASCII fast path, style
handling, selection markers and renderer caches. x/ansi's width query recognizes
keycaps, but its truncation/wrapping still mishandle these ASCII-starting clusters
and wrapping splits an ASCII base from its combining mark. Adoption would not
be a smaller or more correct fix.

`TestGraphemeFormatting` covers all requested categories and all four APIs;
`TestEmojiKeycapBoundaries` covers all twelve bases, multiple keycaps, narrow cuts
and `WrapHard`. The original randomized reference tests remain unchanged.

## Performance and validation

There are no `Benchmark` functions in `tui` itself. Existing renderer and cache
benchmarks are in `app`; these were run before and after with:

```sh
go test ./tui ./app -run '^$' -bench . -benchmem -count 3
```

Because timings varied substantially even in untouched cache/command code, a
second measurement alternated the original and fixed `width.go` in the same
worktree for three rounds, using `-benchtime 200ms -cpu 1` and selecting
`BenchmarkRender(LongTranscript|Streaming)` plus `BenchmarkBuiltinStatus/cached`.
Median times from those interleaved rounds:

| Benchmark | Before ns/op | After ns/op |
| --- | ---: | ---: |
| Long transcript / full repaint | 61,865 | 45,338 |
| Long transcript / fullscreen diff | 53,692 | 52,278 |
| Long transcript / inline diff | 70,549 | 65,494 |
| Streaming / full repaint | 274,368 | 239,050 |
| Streaming / fullscreen diff | 246,211 | 225,658 |
| Streaming / inline diff | 222,295 | 222,985 |
| Builtin status / cached | 196 | 208 |

There is no consistent renderer slowdown. Do not interpret the faster numbers as
an optimization: workload/setup and scheduling noise dominate these runs. In the
longer initial runs, the six renderer cases retain the same allocation counts
(except full-repaint setup variation), and the status cache stays at 64 B/op and
2 allocs/op. Short interleaved runs vary by one allocation in inline modes. No
renderer or cache code changed, and ASCII scanning does not call the correction.

The new regression tests were run before the fix and failed on the keycap width,
truncation and wrapping assertions. After the fix, `go test ./tui` passes, including
the original 30,000-case randomized scanner/reference comparison.

Before committing, `gofmt -l .` produced no paths, `go vet ./...` passed, and
`go test ./...` passed for the entire repository. No existing tests were changed.
