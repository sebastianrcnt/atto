/// <reference path="../atto.d.ts" />
// /diff: what changed in the session's working tree. Built into atto, and
// written against the public extension API only (atto.exec,
// atto.registerCommand and ctx.ui.showText), so it doubles as a reference
// extension: copy it to ~/.atto/extensions/diff.ts and change it (a user or
// project extension of the same name replaces this one).
//
//   /diff                changes against HEAD: staged, then unstaged
//   /diff --staged       only what is staged (--cached works too)
//   /diff <path>         only changes under <path>

// Longest diff shown; atto saves what is shown in the session file.
const MAX_DIFF_LINES = 2000;
// Rows of the file list; the rest are counted.
const MAX_ROWS = 40;
// Diff lines shown while the block is collapsed, after the summary.
const PREVIEW_DIFF_LINES = 14;

interface FileStat {
  path: string;
  added: number;
  removed: number;
  binary: boolean;
}

export default function (atto: Atto) {
  atto.registerCommand("diff", {
    description: "Show what changed in the working tree: /diff [--staged] [path]",
    handler: async (args, ctx) => {
      const { staged, path } = parseArgs(args);
      if (path.includes("'")) {
        ctx.ui.notify("/diff: a path with a single quote is not supported", "warning");
        return;
      }

      const cwd = ctx.cwd;
      const git = (rest: string) => atto.exec("git -c core.quotepath=off " + rest, { cwd, timeout: 30000 });

      const probe = await git("rev-parse --is-inside-work-tree");
      if (probe.code !== 0) {
        const why = probe.stderr.trim();
        if (/not a git repository/i.test(why)) {
          ctx.ui.notify("Not a git repository: " + cwd, "warning");
        } else {
          ctx.ui.notify("/diff: git failed" + (why ? ": " + why.split("\n")[0] : ""), "error");
        }
        return;
      }

      const spec = path ? " -- '" + path + "'" : "";
      const flags = "--no-color --no-ext-diff --src-prefix=a/ --dst-prefix=b/";
      const none = { stdout: "", stderr: "", code: 0, killed: false };
      const [status, cached, working] = await Promise.all([
        git("status --porcelain=v1" + spec),
        git("diff --cached " + flags + spec),
        staged ? Promise.resolve(none) : git("diff " + flags + spec),
      ]);
      for (const r of [status, cached, working]) {
        if (r.code !== 0) {
          ctx.ui.notify("/diff: git failed: " + (r.stderr.trim().split("\n")[0] || "exit " + r.code), "error");
          return;
        }
      }

      const codes = new Map<string, string>();
      const untracked: string[] = [];
      for (const line of status.stdout.split("\n")) {
        if (line.length < 4) continue;
        const xy = line.slice(0, 2);
        const name = unquote(line.slice(3).split(" -> ").pop() as string);
        if (xy === "??") untracked.push(name);
        else codes.set(name, xy);
      }

      const stagedStats = parseDiff(cached.stdout);
      const workingStats = parseDiff(working.stdout);
      const files = new Map<string, FileStat>();
      for (const s of [...stagedStats, ...workingStats]) {
        const f = files.get(s.path);
        if (f) {
          f.added += s.added;
          f.removed += s.removed;
          f.binary = f.binary || s.binary;
        } else {
          files.set(s.path, { ...s });
        }
      }

      const where = path ? " in " + path : "";
      if (files.size === 0 && (staged || untracked.length === 0)) {
        ctx.ui.notify(staged ? "Nothing staged" + where + "." : "No changes" + where + ".", "info");
        return;
      }

      // Summary: the totals, then a row per file.
      let added = 0;
      let removed = 0;
      for (const f of files.values()) {
        added += f.added;
        removed += f.removed;
      }
      let total = `${files.size} file${files.size === 1 ? "" : "s"} changed, +${added} -${removed}`;
      if (!staged) {
        const bits = [];
        if (stagedStats.length) bits.push(`${stagedStats.length} staged`);
        if (workingStats.length) bits.push(`${workingStats.length} unstaged`);
        if (untracked.length) bits.push(`${untracked.length} untracked`);
        if (bits.length) total += " (" + bits.join(", ") + ")";
      }
      const summary: string[] = [total];
      const rows: string[][] = [];
      for (const f of files.values()) {
        rows.push([codes.get(f.path) || "  ", f.path, f.binary ? "binary" : `+${f.added} -${f.removed}`]);
      }
      if (!staged) for (const u of untracked) rows.push(["??", u, "untracked"]);
      const width = Math.min(60, Math.max(...rows.map((r) => r[1].length)));
      for (const r of rows.slice(0, MAX_ROWS)) {
        summary.push("  " + r[0] + " " + r[1].padEnd(width) + "  " + r[2]);
      }
      if (rows.length > MAX_ROWS) summary.push(`  ... and ${rows.length - MAX_ROWS} more`);

      // The diffs: staged first. With --staged there is just the one.
      const diff: string[] = [];
      const section = (title: string, text: string) => {
        if (!text.trim()) return;
        if (!staged) diff.push(`== ${title} ==`);
        diff.push(...text.replace(/\n+$/, "").split("\n"));
      };
      section("Staged changes", cached.stdout);
      section("Unstaged changes", working.stdout);
      if (diff.length > MAX_DIFF_LINES) {
        const hidden = diff.length - MAX_DIFF_LINES;
        diff.length = MAX_DIFF_LINES;
        diff.push(`... diff cut: ${hidden} more lines (run git diff for all of it)`);
      }

      const lines = diff.length ? [...summary, "", ...diff] : summary;
      const title = "git diff" + (staged ? " --staged" : "") + (path ? " " + path : "");
      ctx.ui.showText(title, lines.join("\n"), {
        lang: "diff",
        preview: summary.length + 1 + PREVIEW_DIFF_LINES,
      });
    },
  });
}

function parseArgs(args: string): { staged: boolean; path: string } {
  let staged = false;
  const rest: string[] = [];
  for (const word of args.trim().split(/\s+/)) {
    if (word === "--staged" || word === "--cached") staged = true;
    else if (word && word !== "--") rest.push(word);
  }
  // The rest is one path, spaces and all; quotes around it are optional.
  return { staged, path: unquote(rest.join(" ")) };
}

function unquote(s: string): string {
  const m = /^(["'])(.*)\1$/.exec(s);
  return m ? m[2] : s;
}

// Per-file counts of a unified diff: lines before a file's first hunk
// (headers) are not counted.
function parseDiff(text: string): FileStat[] {
  const out: FileStat[] = [];
  let cur: FileStat | null = null;
  let inHunk = false;
  for (const line of text.split("\n")) {
    if (line.startsWith("diff --git ")) {
      const m = /^diff --git a\/(.*) b\/(.*)$/.exec(line);
      cur = { path: m ? m[2] : line.slice(11), added: 0, removed: 0, binary: false };
      out.push(cur);
      inHunk = false;
    } else if (!cur) {
      continue;
    } else if (line.startsWith("@@")) {
      inHunk = true;
    } else if (!inHunk) {
      if (line.startsWith("Binary files ") || line.startsWith("GIT binary patch")) cur.binary = true;
      else if (line.startsWith("rename to ")) cur.path = line.slice(10);
    } else if (line.startsWith("+")) {
      cur.added++;
    } else if (line.startsWith("-")) {
      cur.removed++;
    }
  }
  return out;
}
