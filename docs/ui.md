# Shared UI elements

Design, 2026-10-09; **accepted; stages 1–4 implemented** (decisions below). Every frontend must
show an extension's drawing and atto's own panels through the same contract: the
TUI first, a new multi-session web UI served on `0.0.0.0`, then GUI/Flutter.
Swing and the frozen web client were deleted; `archive/swing` and
`archive/web-frozen` preserve them. No compatibility layer is required.

**Decisions (2026-10-09):** the user accepted every recommended answer in section 7.
Simplification for the first stages: routing checks `rev` (and site/id/key)
only. The `(clientId, requestKey)` dedupe cache and `uiEpoch` are deferred until
a measured need; a stale or duplicated action after reconnect is rejected by
`rev`, never re-executed.

## 1. Boundary and precedents

There are three layers, not a replacement terminal or a remote DOM:

1. **Native interaction:** prompt editor, scrolling, focus, selection, paging,
   clipboard and key interpretation belong to each client. The command center
   and pickers remain native for now; reconsider during web implementation.
2. **Typed transcript:** `userMessage`, `agentMessage`, `reasoning`,
   `commandExecution`, `notice`, `compaction`, etc. remain protocol items,
   rendered natively. Extensions may wrap or change their *display* through
   item sites and `next()`. They never change conversation/model data this way.
3. **Element trees:** status items, panes (goal/jobs/extensions), above-prompt
   band, toasts, dialogs, extension-added transcript blocks and `/diff`.
   Built-ins use this layer too, not separate frontend-specific panels.

A pure Go **`ui` package** owns constructors, validation, registry, composition,
throttling and events; no goja/`app`/`tui`/`server` dependencies. Go built-ins
build the same trees in full and `-tags noext` builds. `server` supplies session
state, persistence and transport; `extensions` supplies only the full-build goja
adapter. Clients render data; no callback, Go closure or JavaScript source
crosses the wire.

Stages 1–2 provide the pure `ui` registry, protocol, persistence, portable TUI
adapter and Go built-ins. Stage 3 adds the full-build goja binding, JSX catalog,
session JSON store and typed examples; removes live string UI APIs/notifications
and frontend string widgets/status/display plumbing. Legacy session strings are
converted on read to passive trees, without rewriting files or historical code.
The new web UI (stage 4) is implemented; external providers (stage 5) remain future work.

**Precedents.** [Claude Code's interface guide][mods-interface] and
[reference][mods-reference] inform sites (Pane/AbovePrompt/transcript/Spinner/
AskUserQuestion), resolve/next, callbacks, open/close/invalidate/toast, fallback
and throttling. Similar shape, not compatibility. Defer its Svg/Client/Raster,
blit, reactive $.state and surface-only sites. [Google A2UI][a2ui] uses flat
components with IDs, a separate data model, streaming and client widget
catalogs. Borrow stable keys/declarative safety/catalogs; prefer nested **full
trees** over adjacency lists, bindings and patches until measurements justify
them.

**One drawing per session.** Render providers run in the session worker, never
once per attached client. State/callbacks are session-owned; all clients receive
the same tree. Render events have `surface: "shared"`, not whichever client last
resized; `resolve(e)` returns the portable catalog. Event callbacks receive the
actual originating surface/client. Capabilities select local layout/fallback,
not worker state. All v1 sites exist on TUI and web; headless clients produce
text, not dialogs. Unsupported catalog elements use text fallback; never
silently remove a site.

## 2. Element contract v1

Wire node: `{type: string, key?: string, props: object, children?: Node[],
events?: EventType[]}`; constructors lift key/events out of authoring props.
Constructors/JSX normalize strings to Text; one root, no cycles. IDs are
nonempty ≤128 bytes, with provider-local letters/digits/`_`/`-`; item IDs are
engine-assigned. Keys are stable, unique **throughout the composed site tree**,
≤128 UTF-8 bytes. Required for controls and Collapse, optional elsewhere; the
adapter prefixes provider-local keys so wrappers do not collide with `next()`.
Providers cannot manufacture another owner's keys.

The table is the complete v1 prop set; `?` means optional, defaults appear after
`=`. All leaves forbid children; only Box and Collapse accept element children,
and Text accepts strings/Text spans at authoring time (normalized to Text
nodes). Unknown props on known elements, wrong types/ranges, duplicate keys or
illegal children invalidate the tree. Unknown *element types* are instead opaque
passive fallbacks: render text/source/label/alt content and descendant text in
order; if empty, `[unsupported: TYPE]`. Never route their events.

Shared optional props: `color?: ThemeKey="text"`, `backgroundColor?: ThemeKey`
(default transparent). Theme keys are `text`, `muted`, `accent`, `success`,
`warning`, `error`, `border`, `surface`, `diffAdd`, `diffRemove`, `diffHunk`; no
raw ANSI, CSS or RGB. Controls share `disabled?: boolean=false`, `autoFocus?:
boolean=false`; callback/event props below are authoring-only, normalized to the
node's `events: EventType[]` on the wire. Style defaults inherit from
containers; disabled controls are muted and non-interactive.

| Element | Props and children/defaults | TUI cells / web DOM and CSS |
| --- | --- | --- |
| **Box** | `flexDirection?: "column"\|"row"="column"`, `gap?: int=0`, `padding?: int=0`, `width?: int\|"fill"="fill"`, `height?: int` (natural), `grow?: number=0`, `align?: "start"\|"center"\|"end"="start"`, `borderStyle?: "none"\|"single"\|"round"\|"double"\|"ascii"="none"`. 0+ children. | Stack or row; padding/gap are cells, borders cost one cell per edge (`┌─┐`, `╭─╮`, `╔═╗`, `+-+`). CSS flex, ch/line-height units, corresponding border/radius. |
| **Text** | `text?: string=""`, `bold?: bool=false`, `italic?: bool=false`, `underline?: bool=false`, `wrap?: "wrap"\|"truncate"="wrap"`, `maxLines?: int` (unlimited). Text-span children only, appended to text. | Grapheme/cell-aware wrapping or end `…`, SGR theme styles; styled spans, white-space/pre-wrap, overflow and line clamp. |
| **Markdown** | **`text: string`**, `maxLines?: int` (unlimited). No children. | Existing terminal Markdown conventions, wrapping; semantic headings/lists/code/links in DOM. Raw HTML is shown as text, never interpreted. |
| **Code** | **`source: string`**, `language?: string="text"`, `path?: string=""`, `startLine?: int=1`, `lineNumbers?: bool=false`, `wrap?: "wrap"\|"truncate"="truncate"`. | Monospace lines, optional dim path/gutter, truncate with `…`; escaped `<pre><code>`, optional safe highlighting and gutter. Unknown languages are plain. |
| **Diff** | **`source: string`** (unified diff), `path?: string=""`, `lineNumbers?: bool=false`, `wrap?: "wrap"\|"truncate"="truncate"`. | Preserve `+`/`-`, theme add/remove/hunk, dim headers; escaped preformatted DOM with the same semantic classes, unified only in v1. Binary/unparseable lines remain text. |
| **Link** | **`href: string`**, `label?: string` (=href). | Underlined label plus URL if different, safe OSC 8 only when supported; `<a>` with safe URL, `rel="noopener noreferrer"`. No server-side opening. |
| **Button** | **`key`, `label: string`**, `plain?: bool=false`, `hotkey?: string` (one `[a-z0-9]`), **`onPress(e)`**. | `[ label ]` or plain `a: label`, focused accent; `<button>`, optional key badge, native accessible focus. |
| **Input** | **`key`**, `label?: string=""`, `value?: string=""`, `placeholder?: string=""`, `submitLabel?: string="submit"`, `maxLength?: int=4096`, `onInput?(value,e)`, **`onSubmit(value,e)`**. | Single-line local editor, label/cursor/Enter hint, horizontally scrolls; labeled `<input type="text">` in a form. No shared prompt editor and no password/secrets field. |
| **Select** | **`key`, `options: {value:string,label:string,description?:string,disabled?:bool}[]`**, `label?: string=""`, `value?: string` (=first enabled), **`onSelect(value,e)`**. ≥1 option, unique values; explicit value must exist. | Local arrow-key list with marker and dim descriptions; accessible listbox/radio-style list, Enter/click commits. Empty/all-disabled lists must be replaced by Text. |
| **List / Table** | Wire type **`List`**; `mode?: "list"\|"table"="list"`, **`rows: {key:string,cells:string[]}[]`**, `columns?: {label:string,width?:int,align?:"start"\|"end"}[]` (width auto, align start), `emptyText?: string="No items"`. Table requires ≥1 column and matching cell counts; list requires one cell per row. Row keys unique. | Bulleted lines or padded headers/rows, cell truncation; `<ul>` or semantic `<table>`, same column order. No sorting/selection callbacks in v1; interactive rows use Boxes/buttons instead. |
| **Progress** | `value?: number` (indeterminate if absent; otherwise 0..1), `label?: string=""`, `width?: int=20`. | `[###---]` + percent or local spinner + label; `<progress>` with accessible label. Animation is local, not tree updates per frame. |
| **Collapse** | **`key`, `title: string`**, `defaultOpen?: bool=false`, `previewLines?: int=0`. 0+ children. | Disclosure + optional clipped preview, native expansion by key; `<details><summary>` plus preview when closed. Expansion is per client, not a worker callback. |
| **Image** | **`resource: string`, `alt: string`**, `columns?: int=40`, `rows?: int=12`, `fit?: "contain"\|"cover"="contain"`. | Authenticated image resource via existing terminal image support; otherwise `[image: alt]`. `<img>` with alt/object-fit and equivalent size constraints. No paths, remote URLs or inline SVG. |

Dimensions are nonnegative integers (width/height ≥1), bounded to 512 columns,
256 rows; gaps/padding ≤16. Web converts a logical column to its measured text
cell and a row to line-height. It need not reproduce terminal pixels. Box row
layout allocates fixed/intrinsic widths first, shares remaining cells among fill
children (weight grow, or 1), then clips to the parent; column children fill
available width. Row overflow clips, not wraps. Heights clip content; the
**site** owns scrolling. Status and toast roots are passive (no
controls/Input/Select); status is one truncated row.

TUI output must fit its allocated width using `tui` grapheme-width rules, never
split combining/emoji clusters, and expand tabs at four-cell stops. Border
fallback is ASCII where necessary. Web uses scoped CSS/theme variables and DOM
text nodes, not `innerHTML`. Identical content, order, state and actions matter;
pixel identity does not. For invalid trees, errors/timeouts or bad references,
show the site's **unmodified built-in default** and log owner/site/id plus a
bounded reason (extensions.log for extensions). Extension-owned empty sites show
an unavailable placeholder (band/status empty), not a stale tree.

## 3. Sites, composition and replay

Site identity is `(threadId, site, id)`. Extension-created IDs are automatically
namespaced `<extension>/<id>`; built-ins use `atto/<id>`. Registration order is
deterministic (built-ins at the tail; extensions in loader order). Matching
handlers compose as middleware: `next(e)` calls the next handler, eventually the
site default; omitting it replaces that drawing, returning it leaves it alone,
putting it in a Box wraps it. `null` removes the contribution (item sites revert
to native). Render only on props changes/open/invalidate, not every client
paint.

For item sites, the tail of `next(e)` returns an opaque **engine reference**,
not a server-rendered Text tree. Wire form is reserved
`{type:"engine",props:{site,id,overrides},key?}`: the validator mints it,
clients resolve it against their typed item and native renderer. It is not an
extension constructor or a reference to arbitrary code. `next({...e, props:
...})` records only whitelisted display overrides; immutable identity/provenance
is unchanged. References cannot cross sites/IDs or form cycles; at most one per
tree. The client always offers a native “show original” action outside extension
drawing.

| Site | Props passed to render handler; lifecycle | Session-file persistence |
| --- | --- | --- |
| `pane` | `title:string`, `placement:"auto"\|"side"\|"abovePrompt"`, `columns:int`, `rows:int`, `closeOnEscape:bool`; id is pane ID. `open`/`close` change shared visibility. | Tree/open state transient; cold resume rebuilds Go panes and extensions reopen explicitly. Attach restores current open panes. |
| `band` | `busy:bool`; one keyed slot per provider/id, default empty. Compositor stacks slots in registration order above prompt. | Transient. |
| `status` | `busy:bool`, `priority:int`, `align:"start"\|"end"`; keyed item IDs, default empty. Low priority disappears first, ties reverse registration order; preserve high-priority goal/activity. | Transient; rebuild from runtime state, not saved strings. |
| `toast` | `level:"info"\|"warning"\|"error"`, `expiresAt:int` (Unix ms); unique ID, default text. Close/expiry shared; clients do not replay expired toasts. | Transient. |
| `transcript` | `title:string`, `entryId:string`; extension-added block, default empty. Open appends once; render replaces this block, close hides via a saved tombstone. | Save `ui_block` full tree/title/owner and `ui_block_update`/close targeting entry ID; display-only, excluded from model context. |
| `dialog` | `kind:"select"\|"confirm"\|"input"\|"custom"`, `title:string`, `options?:{value,label}[]`, `initialValue?:string`; ID is broker question ID. | Transient; outstanding dialogs in attach snapshot, not recovered as promises after worker crash. |
| `userMessage` | `itemId`, `entryId?`, `status`, `text`, `images`, `clientId?`, `inputId?`; view overrides: text. | Completed overlay saved against entry ID. |
| `assistantMessage` | `itemId`, `entryId?`, `blockId?`, `status`, `text`, `kind:"answer"\|"reasoning"`, `model?`; view overrides: text. Maps `agentMessage` and `reasoning`, not a protocol type rename. | Completed overlay saved against entry ID + block kind. |
| `toolCall` | `itemId`, `entryId?`, `callId?`, `status`, `command`, `description`, `output`, `exitCode?`, `durationMs`, `job?`, `background`, `shell`, `images`; view overrides: description/output. Maps `commandExecution`, including user shell. | Completed overlay saved against entry ID + call ID. |
| `notice` | `itemId`, `entryId?`, `status`, `text`, `level`, `title?`, `origin?`; view overrides: text/title. | Save only if the underlying notice is persisted. |

Identity/status/provenance are read-only; only listed view fields change through
next. Save completed `ui_item_display` trees against stable entry/block/call
IDs; streaming overlays stay transient until saved identity exists. Later
changes append display entries. `thread/items`/offline pages carry latest
overlays, so attach never needs the entire transcript. Replay follows
active-branch entries, shows saved trees without extensions, and **never calls
historical code**. Worker actions stay disabled until a current provider
explicitly rebinds the site/key via fresh rendering; offline/archived actions
remain disabled. Links, Collapse and show-original work locally. Drop completed
trees on last detach, as with today's completed items; no unbounded display
cache.

Pane layout is client-owned: auto uses a side dock at ≥120 logical columns with
≥72 left for transcript and ≥32 for pane, otherwise above prompt. Requested side
also falls back above prompt when these minima fail; explicit abovePrompt stays
there. Defaults: 40 columns, up to 8 rows above prompt, capped at one third of
available height while keeping editor/status visible. Several panes use native
tabs; selected tab/resize/scroll/focus are local. `focus:true` requests focus
only on the invoking client, only after a user command/press, never steals a
nonempty editor. Clicking/native focus navigation can focus any control.
Printable keys go to a focused Input, otherwise hotkeys act only inside the
focused site; reserved navigation keys stay native. Duplicate hotkeys invalidate
a tree. User close requests shared close; Esc returns focus and also requests
close only with closeOnEscape. Moving focus alone closes nothing. Input drafts
and Select highlights are local until commit (unless onInput opts into shared
updates); keep a draft through unrelated redraws when the worker value is
unchanged, reset it when value changes or its key is removed. Collapse choices
survive redraws by key.

Dialogs use the existing asynchronous question broker/first-valid-answer-wins
rule and automatic-work gate. select/confirm/input are helpers building a Go
default tree. Their `next()` is a reference to that Go-built control subtree: a
wrapper must include it **exactly once**, not replace required choices/submit
with unrelated controls. Unlike native item references, dialog references are
expanded by the Go registry to the default subtree before transport; no client
needs a second, native dialog renderer. Custom dialogs own their controls; close
settles cancel. Reload/session close cancels pending questions; losing clients
does not answer them. Worker questions wait with no attached clients; standalone
noninteractive `-p` helpers retain undefined/false defaults. Native
command-center/pickers and private credential/trust flows are not
extension-wrappable dialog sites.

## 4. Protocol revision 3 additions

Extend revision 3 with negotiated `capabilities.ui.version:1`; do not resurrect
old string APIs for removed clients. Existing typed items/execution methods
stay. Session messages include `threadId` and `uiEpoch` (worker incarnation);
notifications use the existing hub `eventId`/`serverInstanceId` cursor. The
table shows the remaining fields; capabilities are connection-level.

| Message | Concrete params / result |
| --- | --- |
| `ui/capabilities` client notification | `{surface:"terminal"\|"web"\|"gui"\|"flutter"\|"headless",width:int,elements:string[]}`; also allowed in initialize's `capabilities.ui` with `version:1`. Reannounce on resize; width in logical columns, 0 if unmeasured. Identity comes from transport, not payload. |
| `ui/render` server notification | `{site,id,rev,tree:Node\|null}`; full validated tree, null removes slot/overlay. Max 256 KiB UTF-8 JSON, including props/keys. No diff/patch messages. |
| `ui/open` server notification | `{site,id,rev,options:{title?,placement?,columns?,rows?,closeOnEscape?,priority?,align?,level?,expiresAt?},focusClientId?}`; shared existence/visibility, optional addressed focus hint. Open first, then render; default drawing until render arrives. |
| `ui/close` server notification | `{site,id,rev,reason:"user"\|"provider"\|"expired"\|"unload"\|"answered"}`; remove live instance, retain revision tombstone. |
| `ui/event` client **request** | `{site,id,key,type:"press"\|"input"\|"submit"\|"select"\|"close",value?:string,rev,serverInstanceId,requestKey}` → `{accepted:true,rev}` or typed error. Close uses reserved key `$site`; press/close forbid value, other events require it. |

`rev` is an increasing safe JSON integer allocated on the worker lane for every
published UI mutation/removal, never reused across navigation/reload/reopen.
`uiEpoch` changes on worker restart even if the gateway/hub instance survives;
replace UI state on epoch change and reject old-epoch actions. Route callbacks
by site/id/key **and epoch/rev**. Server derives `clientId` and originating
surface from the connection; gateways preserve it. The new web UI uses WS
independent identities, not legacy HTTP/SSE's shared anonymous identity.
Read-only clients may observe but cannot send actions. Validate epoch/rev,
ownership, declared event, value length/option membership and disabled state.
Stale actions return `revisionConflict`/currentRevision; resnapshot, never press
a different button reusing that key. Close requires a closable instance; helper
close uses the broker. Queue accepted callbacks onto the provider runtime
without waiting under lane/transport locks. Log failures and toast them; clients
must not retry them as unaccepted requests.

**Ordering/exactly-once scope:** subscribe before snapshot, discard
notifications at/below its event cursor, then apply later event IDs once; rev
additionally rejects older site updates. Gaps in event IDs are normal. Coalesce
unpublished invalidations to the latest state (10 renders/sec/site); only then
assign rev and hub eventId. Published events are immutable: a slow subscriber
resets/resnapshots rather than receiving rewritten event IDs. Open/close/dialog
settlement bypass throttling. Reset, cursor overflow, instance change and
reconnect use the snapshot rules in [protocol.md](protocol.md).

The attach/read snapshot gains
`ui:{version:1,uiEpoch,instances:[{site,id,rev,options, tree}]}` containing
**all current panes, band/status trees**, active dialogs and unexpired toasts.
Loaded items carry `uiDisplay?:{rev,tree}`; extension blocks are typed `uiBlock` items with
title/owner/tree/entryId. Snapshot shares one lane-consistent cursor with items.
Branch movement resets bindings, advances revisions and resnapshots; discard old
page results. Null/removal and close tombstones protect against late
notifications.

Event IDs guarantee exactly-once **reduction**, not side effects. Add UI dedupe:
`(clientId,requestKey)` caches the accepted result/payload for 10 minutes, max
4096 per worker; different payload with reused key is invalid. A retry within
this horizon/identity never repeats the callback. New connection identity or
worker restart has no such guarantee; never automatically resend an uncertain
action. This new UI-only mechanism does not make existing execution requests
durable. Dedupe lookup precedes rev checking, so a retry of an accepted press
returns its original result even after that press caused a new revision.

**Later out-of-process providers:** reserve `ui/register` request
`{threadId,uiEpoch,providerId,sites:[{site,id,options?}]}` → `{registrationId}`.
Authorize explicitly, reserve a provider namespace/connection lease, reject
collisions. That connection pushes the same `ui/render`/open/close payloads as
requests (rev omitted: worker validates/assigns it) and receives routed
`ui/event` notifications with a worker event token and derived client identity.
Trees use keys/declared events, not callback handles. Disconnect removes
transient sites, cancels questions and disables persisted actions. No new
renderer or goja is needed, even in slim builds; initial external providers own
slots, not synchronous `next()` middleware. Provider result/ack flow can be
added with this lease later.

## 5. Extension authoring API

Implemented in `extensions/atto.d.ts` (sketch below; table above
supplies constructor prop interfaces). Keep **notify/select/confirm/input** as
helpers; remove the old setBlockStatus, setBlockDisplay, showText, setStatus,
setWidget and AttoShowTextOptions.

```ts
type UISite = "pane" | "band" | "status" | "toast" | "transcript" | "dialog"
  | "userMessage" | "assistantMessage" | "toolCall" | "notice";
type UIMatch = { site: UISite; id?: string };
type UIEvent = { clientId: string; surface: string; rev: number };
type UIRenderEvent = { site: UISite; id: string; surface: "shared";
  props: UISiteProps }; // discriminated union of the site table
// UIElement is an authoring node; UIEngineRef is opaque and minted by next.
type UITree = UIElement | UIEngineRef | null;
type UINext = (e?: UIRenderEvent) => Awaitable<UITree>;
interface AttoUI {
  render(match: UIMatch,
    fn: (e: UIRenderEvent, next: UINext) => Awaitable<UITree>): () => void;
  resolve(e: UIRenderEvent): UIConstructors; // Box, Text, ...; callable props factories
  jsx(factory: UIConstructor, props: object | null, ...children: UIChild[]): UIElement;
  open(o: UIOpenOptions): Promise<void>;
  close(o: { site: UISite; id: string }): Promise<void>;
  invalidate(match?: UIMatch): void; // omitted = this provider's live sites
  toast(text: string, o?: { level?: "info"|"warning"|"error";
    timeoutMs?: number }): Promise<void>; // default info, 4000 ms; range 500..30000
  notify(text: string, level?: "info"|"warning"|"error"): void;
  select(title: string, options: string[]): Promise<string | undefined>;
  confirm(text: string): Promise<boolean>;
  input(prompt: string): Promise<string | undefined>;
}
interface UIOpenOptions {
  site: "pane"|"band"|"status"|"transcript"|"dialog";
  id: string; title?: string; focus?: boolean; closeOnEscape?: boolean;
  placement?: "auto"|"side"|"abovePrompt"; columns?: number; rows?: number;
  priority?: number; align?: "start"|"end";
}
interface AttoStore { // atto.store; JSON only, extension + session scoped
  get<T extends JSONValue>(key: string): Promise<T | undefined>;
  set(key: string, value: JSONValue): Promise<void>;
  delete(key: string): Promise<void>;
  keys(): Promise<string[]>;
}
// Atto gains readonly store: AttoStore. JSONValue is the usual recursive JSON union.
```

Callbacks are constructor props: `onPress:(e:UIEvent)=>Awaitable<void>`, and
`onInput/onSubmit/onSelect:(value:string,e:UIEvent)=>Awaitable<void>`. The
adapter extracts them into a registry for the emitted revision; only keys/event
names survive serialization. Render hooks read state and return trees: no I/O or
state writes while rendering; async exists for composition, not slow model
calls. `notify` creates a typed notice, not a toast. Open options default title
to id, placement to auto, closeOnEscape/focus to false, priority to 0, align to
start; columns/rows use the pane defaults. Dialog open creates custom dialogs;
helpers supply broker-owned props. Open/close cannot target item sites;
duplicate open updates metadata, not transcript append. Register/render before
open; IDs in match/open are provider-local, while events carry resolved
identity.

`atto.store` uses the session writer to append namespaced JSON updates (≤1 MiB
per extension/session, ≤64 KiB/value); survives reload/cold resume, inherited as
of a fork point. `/clear` gets a fresh store; branch replay restores reachable
updates. Module variables are ephemeral worker state; a reactive `atto.state`
and a global cross-session store are deferred. Writes do not implicitly redraw:
invalidate explicitly. Pane visibility/focus is not persisted in this store by
the engine.

```tsx
export default function (atto: Atto) {
  let count = 0;
  atto.on("session_start", async () => {
    count = (await atto.store.get<number>("count")) ?? 0;
    atto.ui.invalidate({ site: "pane", id: "counter" });
  });
  atto.ui.render({ site: "pane", id: "counter" }, (e) => {
    const { Box, Text, Button } = atto.ui.resolve(e);
    return <Box gap={1}>
      <Text text={`Count: ${count}`} />
      <Button key="more" label="Add one" hotkey="a" onPress={async () => {
        count++;
        await atto.store.set("count", count);
        atto.ui.invalidate({ site: "pane", id: "counter" });
      }} />
    </Box>;
  });
  atto.registerCommand("counter", { handler: () =>
    atto.ui.open({ site: "pane", id: "counter", title: "Counter", focus: true }) });
}
```

```ts
atto.ui.render({ site: "toolCall" }, async (e, next) => {
  const { Box, Text } = atto.ui.resolve(e);
  const original = await next(e); // each client draws its native tool item
  return Box({ children: [original, Text({ text: "Reviewed by my extension",
    color: "muted" })].filter(x => x !== null) });
});
// Or change just display text: next({...e, props:{...e.props, description:"Checking…"}}).
```

Extend existing esbuild → CommonJS/ES2017 → goja with `.tsx/.jsx` discovery and
`jsxFactory: "atto.ui.jsx"`/fragment normalization. `jsx` invokes constructor
functions with props/children; no React, DOM runtime or browser JS is bundled.
Declare JSX types alongside atto.d.ts. Constructor calls work in plain `.ts`
identically. Reload disposes registrations, cancels dialogs, retires old
callback revisions and re-renders; never leaks callbacks into another session.

## 6. Go built-ins and safety/testing

The pure-Go API (see `ui/registry.go` for exact signatures):

```go
// package ui: immutable data nodes; callbacks are separate from wire nodes.
type Renderer func(Event, Next) (Node, error)
type Next func(Event) (Node, error)
type Handler func(context.Context, Action) error
func Box(p BoxProps, children ...Node) Node
func Text(p TextProps) Node // analogous constructors for every v1 element
func Validate(site Site, tree Node) error
func (r *Registry) Render(owner string, match Match, fn Renderer) Dispose
func (r *Registry) Bind(owner string, match Match, key string, kind EventType, fn Handler)
func (r *Registry) Open(owner string, opts OpenOptions) error
func (r *Registry) Close(owner string, site Site, id string) error
func (r *Registry) Invalidate(match Match)
func (r *Registry) Route(ctx context.Context, a Action) error
// Registry output -> server persistence/hub; transport/Go callbacks stay outside Node.
```

Build/migrate first: `/diff` (Collapse + summary List/Table + Diff, preserving
native_diff.go's git/error/truncation logic), status items (including
goal/activity), goal pane (Text/Progress/buttons), jobs pane (List/Table +
action buttons), then dialog helpers. Their actions call existing worker
services, not synthetic keypresses. Custom statusLine commands stay worker-side
and opt-in, sharing one execution/refresh cache among displaying clients;
convert safe text/SGR output to themed Text, do not put arbitrary ANSI in trees.
Frontend-only connection status may use local Go trees with no shared actions.

**Limits:** 256 KiB serialized tree, 2048 nodes, depth 32, aggregate text 128
KiB, 256 live sites/session, 4 MiB live tree budget, max 10 redraws/sec/site and
60/sec per provider. Validate before allocation-heavy layout; latest dirty state
wins, not an unbounded redraw queue. Render deadline 100 ms/handler, 250
ms/site; timeout/exception uses default drawing. Input events debounce 100
ms/client, submit/press/select do not coalesce; cap action ingress 20/sec/client
with a typed rate-limit error. Store/I/O waits and callbacks use existing
extension watchdog rules, user dialog waits do not hold lane locks. Image bytes
are separate bounded resources (≤10 MiB encoded, ≤16 million decoded pixels),
not part of a tree.

**Security:** trees are data, not HTML, scripts, executable modules, terminal
escapes or arbitrary file reads. Strip user-supplied control sequences,
including OSC, before terminal layout. Links/Markdown links allow canonical
https URLs and http only to localhost/loopback; ≤2048 bytes, no credentials,
controls or javascript/data/file schemes. Disallowed URLs invalidate the
drawing. Web builds DOM safely, sanitizes Markdown through the same allowlist,
uses CSP and no HTML injection. Image resources are authenticated session-scoped
opaque IDs. Preserve bearer/Origin checks; binding `0.0.0.0` is not permission
to expose an unauthenticated shell. Use trusted LAN/Tailscale or TLS reverse
proxy; do not log query tokens. Private login secrets never enter shared
trees/store/logs. Extensions retain their existing trust approvals: this layer
is not a sandbox for worker-side code.

**Tests:** one tree/site fixture corpus drives frontend-specific goldens: TUI
cells at 40/80/120/160 columns (graphemes/borders/clipping/themes); web DOM,
accessibility and screenshots at equivalent breakpoints; later Flutter too.
Headless text rendering/key-based action simulation needs no terminal/browser/
goja. Fuzz validation/limits/engine refs. Protocol tests cover snapshot races,
duplicates/reordering/coalescing, paging/replay, stale actions, two-client
dialog races, reset/reload/branch/disconnect/crash and malicious
links/Markdown/escapes. Full/noext built-in fixtures must produce identical
trees. Scripted-provider tests verify display changes never alter model request
bytes or typed transcript truth.

## 7. Reviewable delivery and remaining decisions

1. **Pure ui + protocol:** schemas/constructors/validator, headless renderer,
   registry/throttler, revision/event DTOs, snapshot reducer, persistence and
   callback routing tests. Leave old APIs temporarily only to keep this stage
   buildable; no new users. Add normative protocol/types docs when implemented.
2. **TUI + Go built-ins:** element adapter on `tui.Component`, hit-testing/native
   focus integration, engine references; move `/diff`, status, goal, jobs, dialogs
   in separate commits with golden/full-noext parity gates. Keep typed transcript,
   paging, command center and pickers native.
3. **Goja binding + remove strings (implemented):** thin constructor/callback bridge, render
   middleware, session store, JSX/types/examples; delete old setters/showText,
   UIState/string notifications/display plumbing. No compatibility shim. Back up
   sessions before any format cleanup; old saved string entries may be converted
   once to passive trees, not maintained as a second rendering API.
4. **New web UI (implemented):** ordinary Go HTTP server bound to `0.0.0.0` with existing auth,
   static assets and WS gateway over workers; many sessions via thread/list and
   independent attach/detach. DOM catalog follows these rules, not the frozen
   client. Native web prompt editor/scroll/focus/pickers; audit whether command
   center/pickers now merit shared trees. Browser reconnect never re-executes an
   uncertain action. Closing web server detaches, never stops session work.
5. **External providers later:** register/lease/push/event adapter with explicit
   authorization, noext tests and one example in another language. No new widget
   schemas or extension VM are prerequisites.

Open questions, with recommended answers:

- **Shared tree vs e.surface variants?** Keep `surface:"shared"` during render,
  actual surface on actions. Responsive layout/fallback in clients avoids one
  browser resize changing another user's tree; revisit only for optional media.
- **How rich should v1 layout/catalog be?** Keep the 13 families above (List has
  table mode), unified Diff and text-only table cells. Defer SVG/Raster/Client,
  absolute positioning, arbitrary styles, reactive bindings and blit/patching.
- **Persistence versus resumed interactivity?** Persist transcript displays and
  session store, not pane/focus/draft state or promises. Require fresh explicit
  callback rebinding; never run historical extension code on offline replay.
- **Close/focus scope?** Shared pane visibility, local selected tab/focus. A close
  request closes it everywhere; a focus hint targets only its initiating client.
- **Exactly-once across crashes/global extension storage?** Neither in v1. Keep
  bounded live UI dedupe and session-scoped store; durable action journals/global
  transactional storage deserve separate designs, not promises hidden in trees.

[OSC 7501 program-status reporting][osc7501] is a **separate small TUI
feature**: report terminal program activity/waiting state, not a UI
element/site, tree transport or cross-frontend drawing mechanism.

[mods-interface]: https://code.claude.com/docs/en/plugins/mods/interface.md
[mods-reference]: https://code.claude.com/docs/en/plugins/mods/reference.md
[a2ui]: https://a2ui.org
[osc7501]: https://www.superlogical.com/rex/docs/build/program-status

## Stage 4 delivery (2026-10-09)

`server/web` is a new plain TypeScript DOM client of revision 3, embedded in Go.
This is the smaller permitted DOM option rather than adding a Preact runtime;
Tailwind standalone + Go esbuild builds committed dist without Node. The frozen
client was consulted only for Beautiful UI design/animation patterns and build
pins, never its HTTP/SSE/string protocol. Beautiful UI adaptations and licenses
are recorded in THIRD_PARTY_NOTICES; Vercel AI Elements supplies behavior ideas
only, with no copied source.

An explicit `--web` flag keeps existing WS-only app-server listeners stable.
`atto serve` and TUI `/remote` enable it on `0.0.0.0:7879` by default. Static
assets are public and contain no session/token data; `/ws` requires the existing
off-loopback bearer auth and Origin checks. The fragment bootstrap is consumed
once into tab-scoped sessionStorage; the socket uses an auth subprotocol offer,
not a query string. Transport close never closes its runtime/workers.

Every catalog family and site has a DOM adapter. Controls use declared events
and current rev; local drafts, collapse state, site hotkeys and focus survive
unrelated drawings. Saved controls stay passive, engine references resolve to
native typed items, and original display is always available. Pages load tail
first, preserve live items and anchors, and invalidate on branch/reset. Reconnect
buffers notifications around replacement snapshots and never resends actions.
Native inventory shows live/needs-you and agent parent links, archived reads,
multiple open session tabs and confirmed mutations. The composer supports
send/steer/queue/send-now/interrupt, runtime slash suggestions, model/effort and
image attachment. Pane tabs/layout, status priority, expiry and dialogs are local.
Queue previews/resume and the context card now use Go trees for both frontends;
goal/jobs/diff/extension UI already use the shared worker registry.

**Boundary audit:** inventory, model/effort pickers and tree/checkpoint navigation
remain native: their paging/search, scroll anchors and selection are client-local,
and dialogs for worker questions already use shared trees. Credential and
repository-trust flows intentionally remain in the local CLI. Tool/reasoning
items use accessible disclosures, plain code/output and unified diff lines;
there is no syntax-highlighting dependency. Browser Markdown follows the TUI's safe CommonMark/GFM conventions (tables,
headings, paragraphs, nested/ordered/task lists, blockquotes, fences, hard breaks,
emphasis, inline code and allowed links); raw HTML remains literal text. Tool grouping is
not yet identical to the TUI's grouping preference: the web draws one disclosure
per command. Clipboard text falls back to a selectable dialog on plain HTTP.

Tests run TS core/catalog and page workflows through Go/esbuild/goja with a
DOM shim at phone and desktop widths, plus the shared TUI fixture, Go auth/CSP/
bootstrap/detach tests and real CLI process e2e. The 2026-10-10 polish additionally
checks real headless Chrome at 1400×900, 1024×768 and 390×844 in both themes,
including local-model turns, shared diff/dialog/context trees, image drafts and
gateway reconnect. Physical touch keyboards and LAN access remain manual QA.
See docs/ui-stage-4-report.md for verification and screenshot paths.

### Incremental browser rendering (2026-10-10)

The browser keeps one layout/sidebar/topbar/tabs shell and a detached, persistent
thread view for every open tab. Each thread owns its transcript scroller,
composer area/form and one textarea. Small metadata input tuples invalidate
individual regions (pane, band, queue, suggestions, activity, attachments,
toolbar, status, toasts and dialogs); transcript text is not part of those
inputs. Inventory refreshes compare results, and thread inventory versions
exclude deltas. Sidebar and per-thread scroller offsets survive region updates
and tab detachment/reattachment.

Transcript rows are keyed by thread ID + item ID. Views accumulate dirty item
IDs until the next animation frame: full item events replace only that row,
while deltas replace only the children of its native Markdown/output container.
Rows and disclosures stay attached during deltas. Older pages insert before
existing rows and restore the scroll-height anchor; live updates follow the
tail only when the reader was already near it. Snapshot-only comparisons retain
identical loaded rows across rehydration; reset generations still invalidate
in-flight pages. Item/site caches are pruned when pages/sites/tabs disappear.

Shared site trees are cached by thread/site/id/rev. Connectivity changes update
control enablement and the callback gate without drawing the tree again; native
engine references can update within a cached drawing. Existing local drafts,
disclosures and revision-bound callbacks remain authoritative. The textarea is
never replaced within a thread view: ordinary typing still schedules no paint
(except changing slash suggestions), and all paints still wait for an IME
composition to end. Initial/resize sizing is measured after DOM attachment;
subsequent editor growth happens only on input, draft-value changes or resize.

No framework, HTML parsing, new runtime dependency, CSP change or protocol
change accompanies this renderer. The bounded `atto-paint` performance entries
and the external CDP measurement procedure are documented in the stage-4 report.
