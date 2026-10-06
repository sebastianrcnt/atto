// The transcript store: items by ID in the order they started, updated in
// place as notifications arrive (tested from Go: server/web/transcript_test.go).
//
// The view renders it as rows: an item, or a run of commands (with the
// reasoning between them) that may show as a group (see toolgroup.ts).
// Rows come in blocks of CHUNK, each memoized on its array: a delta makes
// a new array for its own block only, so a frame re-renders one block,
// not thousands of items.

import type { BlockDisplay, Item } from "./types";

export const CHUNK = 50;

// Run is a row of commands the model ran one after another; its ID is
// its first command's, and it is a new object whenever a member changes.
export type Run = { id: string; type: "run"; members: Item[] };
export type Row = Item | Run;

export const isRun = (r: Row): r is Run => r.type === "run";

// A running command's output is kept as the server keeps it (see
// core/transcript): the last KEEP_OUTPUT bytes, trimmed once it doubles.
export const KEEP_OUTPUT = 64 * 1024;

export class Transcript {
  private items: Item[] = [];
  private index = new Map<string, number>();
  private rows: Row[] = [];
  private rowOf = new Map<string, number>(); // item ID → row
  private chunks: Row[][] = [];
  private dirty = new Set<number>();
  // commands in progress, for running()
  private open = new Set<string>();
  notes = 0;

  reset(items: Item[] = []) {
    this.items = [];
    this.index.clear();
    this.rows = [];
    this.rowOf.clear();
    this.chunks = [];
    this.dirty.clear();
    this.open.clear();
    items.forEach((it) => this.upsert(it));
  }

  get length() {
    return this.items.length;
  }

  get(id: string): Item | undefined {
    const i = this.index.get(id);
    return i === undefined ? undefined : this.items[i];
  }

  last(): Item | undefined {
    return this.items[this.items.length - 1];
  }

  upsert(it: Item) {
    const i = this.index.get(it.id);
    if (i === undefined) {
      this.index.set(it.id, this.items.length);
      this.items.push(it);
      this.place(it);
    } else {
      this.items[i] = it;
      const r = this.rowOf.get(it.id)!;
      const row = this.rows[r];
      this.rows[r] = isRun(row) ? { ...row, members: row.members.map((m) => (m.id === it.id ? it : m)) } : it;
      this.dirty.add(Math.floor(r / CHUNK));
    }
    if (it.type === "commandExecution" && it.status === "inProgress" && !it.pending) this.open.add(it.id);
    else this.open.delete(it.id);
  }

  // place adds a new item's row: a command joins the run the transcript
  // ends with, or starts one; reasoning joins an open run; anything else
  // ends it.
  private place(it: Item) {
    const r = this.rows.length - 1;
    const last = this.rows[r];
    if ((it.type === "commandExecution" || it.type === "reasoning") && last && isRun(last)) {
      this.rows[r] = { ...last, members: [...last.members, it] };
      this.rowOf.set(it.id, r);
      this.dirty.add(Math.floor(r / CHUNK));
      return;
    }
    this.rowOf.set(it.id, r + 1);
    this.rows.push(it.type === "commandExecution" ? { id: "run-" + it.id, type: "run", members: [it] } : it);
    this.dirty.add(Math.floor((r + 1) / CHUNK));
  }

  // delta appends streamed text (output, for a command) to an item; one
  // not known (not started yet as far as this client knows) is skipped.
  delta(id: string, d: string) {
    const it = this.get(id);
    if (!it) return;
    if (it.type === "commandExecution") {
      let out = (it.output || "") + d;
      if (out.length > 2 * KEEP_OUTPUT) out = out.slice(out.length - KEEP_OUTPUT);
      this.upsert({ ...it, output: out });
    } else this.upsert({ ...it, text: (it.text || "") + d });
  }

  // display sets what extensions show on an item (item/display; null:
  // nothing). The server sends it in order with the item's own
  // notifications, so a later item/completed carries it too.
  display(id: string, d: BlockDisplay | null) {
    const it = this.get(id);
    if (it) this.upsert({ ...it, display: d });
  }

  note(text: string, tone: "error" | "info" = "info") {
    this.upsert({ id: "note-" + ++this.notes, type: "note", text, tone });
  }

  list(): Item[] {
    return this.items.slice();
  }

  // blocks are the rows in blocks of CHUNK; a block is a new array only
  // when one of its rows changed since the last call.
  blocks(): Row[][] {
    const n = Math.ceil(this.rows.length / CHUNK);
    this.chunks.length = n;
    for (const j of this.dirty) if (j < n) this.chunks[j] = this.rows.slice(j * CHUNK, (j + 1) * CHUNK);
    this.dirty.clear();
    return this.chunks;
  }

  running(): boolean {
    return this.open.size > 0;
  }
}
