// translate (experimental): shows the model's answers and thinking in your
// language. Display only: the model keeps reading its own words, and ctrl+o
// (or the line under a block) flips back to the original. A block already
// in your language is left alone, so with "ko" Korean stays as it is and
// English is shown in Korean; with "en", the other way round.
//
// Copy this file to ~/.atto/extensions/, run /reload, then /translate on.
//
//   /translate                    what it is set to
//   /translate on | off           (off until you turn it on)
//   /translate lang ko            the language to show (ko, en, ja, ...)
//   /translate engine apple       Apple's on-device Translation (macOS 26+)
//   /translate engine model p/id  a model from models.json, through atto.complete
//   /translate thinking on | off  translate reasoning blocks too (on)
//
// The settings are kept in ~/.atto/translate.json.
//
// The apple engine is free and works offline. It needs the language pair
// installed (System Settings → General → Language & Region → Translation
// Languages) and builds a small helper with swiftc on first use (Xcode or
// the Command Line Tools). Code blocks, inline code and URLs are kept; the
// text is sent line by line, list markers and headings aside.
//
// The model engine sends each block whole to the model you name, with an
// instruction to keep code as it is. A small local model is enough.

type Settings = {
  enabled: boolean;
  lang: string;
  engine: "apple" | "model";
  model: string;
  thinking: boolean;
};

const DEFAULTS: Settings = { enabled: false, lang: "ko", engine: "apple", model: "", thinking: true };

// The helper: reads {"to", "texts"} from the file named on its command
// line, detects the source language, and prints {"from", "texts"}, or
// {"skip": true} when the text is already in the target language, or
// {"error"}. The high-fidelity model was better than low-latency at
// technical text, and low-latency needs a download of its own.
const HELPER_VERSION = 1;
const HELPER_SOURCE = String.raw`
import Foundation
import Translation
import NaturalLanguage

struct Request: Decodable { let to: String; let texts: [String] }

func reply(_ o: [String: Any]) {
    let d = try! JSONSerialization.data(withJSONObject: o)
    print(String(data: d, encoding: .utf8)!)
}

@main struct Main {
    static func main() async {
        guard CommandLine.arguments.count == 2,
              let data = FileManager.default.contents(atPath: CommandLine.arguments[1]),
              let req = try? JSONDecoder().decode(Request.self, from: data) else {
            reply(["error": "usage: atto-translate <request.json>"])
            exit(2)
        }
        let rec = NLLanguageRecognizer()
        rec.processString(req.texts.joined(separator: "\n"))
        guard let lang = rec.dominantLanguage else { reply(["skip": true]); return }
        let source = Locale.Language(identifier: lang.rawValue)
        let target = Locale.Language(identifier: req.to)
        if source.languageCode == target.languageCode { reply(["skip": true, "from": lang.rawValue]); return }
        switch await LanguageAvailability().status(from: source, to: target) {
        case .unsupported:
            reply(["error": "translating \(lang.rawValue) to \(req.to) is not supported"]); return
        case .supported:
            reply(["error": "the \(lang.rawValue) → \(req.to) languages are not installed: System Settings → General → Language & Region → Translation Languages"]); return
        default:
            break
        }
        let items = req.texts.enumerated().map { TranslationSession.Request(sourceText: $1, clientIdentifier: String($0)) }
        var out = req.texts
        do {
            var res: [TranslationSession.Response]
            do {
                res = try await TranslationSession(installedSource: source, target: target, preferredStrategy: .highFidelity).translations(from: items)
            } catch {
                res = try await TranslationSession(installedSource: source, target: target).translations(from: items)
            }
            for r in res {
                if let id = r.clientIdentifier, let i = Int(id) { out[i] = r.targetText }
            }
            reply(["from": lang.rawValue, "texts": out])
        } catch {
            reply(["error": "\(error)"])
        }
    }
}
`;

const quote = (s: string) => "'" + s.replace(/'/g, `'\\''`) + "'";

export default function (atto: Atto) {
  atto.setCompleteConcurrency(1);

  let dir = ""; // ~/.atto
  let settings: Settings = { ...DEFAULTS };
  const save = () => atto.fs.writeFile(`${dir}/translate.json`, JSON.stringify(settings, null, 2) + "\n");
  const showStatus = () => atto.ui.setStatus("translate", settings.enabled ? `⇄ ${settings.lang}` : null);
  const ready = (async () => {
    const r = await atto.exec(`printf %s "\${ATTO_DIR:-$HOME/.atto}"`);
    dir = r.stdout.trim();
    try {
      settings = { ...DEFAULTS, ...JSON.parse(atto.fs.readFile(`${dir}/translate.json`)) };
    } catch {
      // not set yet
    }
    showStatus();
  })();


  // One block at a time, in order: a local server or the helper answers
  // one request at a time anyway.
  let queue: Promise<unknown> = Promise.resolve();
  const enqueue = (fn: () => Promise<void>) => {
    queue = queue.then(fn, fn);
  };

  // --- the apple engine ---

  let helper = "";
  let building: Promise<string> | null = null;
  const buildHelper = (): Promise<string> => {
    if (helper) return Promise.resolve(helper);
    if (building) return building;
    building = (async () => {
      const bin = `${dir}/cache/translate/atto-translate-${HELPER_VERSION}`;
      if (atto.fs.exists(bin)) return (helper = bin);
      const os = await atto.exec("uname -s");
      if (os.stdout.trim() !== "Darwin") throw new Error("the apple engine needs macOS 26 or later; use /translate engine model <provider/id>");
      const src = `${dir}/cache/translate/atto-translate.swift`;
      atto.fs.writeFile(src, HELPER_SOURCE);
      atto.ui.notify("translate: building the Apple Translation helper (once)…");
      const r = await atto.exec(`swiftc -parse-as-library -O ${quote(src)} -o ${quote(bin)}`, { timeout: 300000 });
      if (r.code !== 0) throw new Error(`building the helper failed (it needs macOS 26 and swiftc): ${(r.stderr || r.stdout).trim().slice(0, 400)}`);
      return (helper = bin);
    })().finally(() => {
      building = null;
    });
    return building;
  };

  let reqN = 0;
  const appleTranslate = async (texts: string[]): Promise<string[] | null> => {
    const bin = await buildHelper();
    const req = `${dir}/cache/translate/request-${atto.session.id}-${reqN++ % 4}.json`;
    atto.fs.writeFile(req, JSON.stringify({ to: settings.lang, texts }));
    const r = await atto.exec(`${quote(bin)} ${quote(req)}`, { timeout: 120000 });
    let out: { skip?: boolean; texts?: string[]; error?: string };
    try {
      out = JSON.parse(r.stdout);
    } catch {
      throw new Error(`the helper failed: ${(r.stderr || r.stdout).trim().slice(0, 400)}`);
    }
    if (out.error) throw new Error(out.error);
    return out.skip ? null : out.texts ?? null;
  };

  // A block of Markdown, line by line: code blocks, table rules and lines
  // without letters stay; list markers, quote marks and heading marks stay
  // in front of the line's text; table cells go one by one. Inline code is
  // left in the text (the model keeps it, and Korean particles then fit the
  // word); URLs go as {0}, {1}..., which the model keeps better than the
  // URLs themselves.
  const translateMarkdown = async (text: string): Promise<string | null> => {
    const lines = text.split("\n");
    const texts: string[] = [];
    const plan: (string | ((t: string[]) => string))[] = [];
    const letters = /\p{L}/u;
    let fence = "";
    for (const line of lines) {
      const f = line.match(/^\s*(```+|~~~+)/);
      if (fence) {
        if (f && f[1][0] === fence[0] && f[1].length >= fence.length) fence = "";
        plan.push(line);
        continue;
      }
      if (f) {
        fence = f[1];
        plan.push(line);
        continue;
      }
      if (!letters.test(line)) {
        plan.push(line);
        continue;
      }
      if (/^\s*\|/.test(line)) {
        const cells = line.split("|");
        const at: number[] = [];
        cells.forEach((c, i) => {
          if (letters.test(c)) {
            at.push(texts.length);
            texts.push(c.trim());
          } else at.push(-1);
        });
        plan.push((t) => cells.map((c, i) => (at[i] < 0 ? c : ` ${t[at[i]]} `)).join("|"));
        continue;
      }
      const m = line.match(/^(\s*(?:(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?|>\s*|#{1,6}\s+)*)(.*)$/)!;
      const k = texts.length;
      texts.push(m[2]);
      plan.push((t) => m[1] + t[k]);
    }
    if (texts.length === 0) return null;
    const urls: string[][] = texts.map(() => []);
    const sent = texts.map((t, i) =>
      t.replace(/https?:\/\/[^\s)>\]`]+[^\s)>\]`.,;:!?'"]/g, (u) => `{${urls[i].push(u) - 1}}`),
    );
    const got = await appleTranslate(sent);
    if (!got) return null;
    const out = got.map((t, i) => t.replace(/\{(\d+)\}/g, (m, n) => urls[i][+n] ?? m));
    return plan.map((p) => (typeof p === "string" ? p : p(out))).join("\n");
  };

  // --- the model engine ---

  const LANGUAGES: Record<string, string> = { ko: "Korean", en: "English", ja: "Japanese", zh: "Chinese", es: "Spanish", fr: "French", de: "German" };

  // inLanguage tells, for Korean and English, whether text is already in
  // it, by its share of Hangul letters (code aside); null for the others.
  const inLanguage = (text: string, lang: string): boolean | null => {
    const prose = text.replace(/```[\s\S]*?```/g, "").replace(/`[^`]*`/g, "");
    const hangul = (prose.match(/[ㄱ-ㆎ가-힣]/g) || []).length;
    const latin = (prose.match(/[A-Za-z]/g) || []).length;
    if (hangul + latin === 0) return true;
    const share = hangul / (hangul + latin);
    if (lang === "ko") return share > 0.3;
    if (lang === "en") return share < 0.05;
    return null;
  };

  const modelTranslate = async (text: string): Promise<string | null> => {
    if (!settings.model) throw new Error("no model set: /translate engine model <provider/id>");
    const language = LANGUAGES[settings.lang] ?? settings.lang;
    const { text: out } = await atto.complete({
      model: settings.model,
      system:
        `Translate the user's text into natural ${language}. Keep Markdown, code, identifiers, ` +
        `file paths, commands, URLs and error messages exactly as they are. Output only the translation.`,
      prompt: text,
      reasoningEffort: "none",
      maxTokens: Math.min(8000, Math.ceil(text.length / 2) + 256),
      timeoutMs: 120000,
    });
    return out.trim();
  };

  // --- the blocks ---

  let warned = "";
  const translateBlock = (blockId: string, text: string) => {
    text = text.trim();
    if (!text || inLanguage(text, settings.lang) === true) return;
    enqueue(async () => {
      if (!settings.enabled) return;
      atto.ui.setBlockStatus(blockId, "translating…");
      try {
        const out = settings.engine === "apple" ? await translateMarkdown(text) : await modelTranslate(text);
        if (out) atto.ui.setBlockDisplay(blockId, out);
        atto.ui.setBlockStatus(blockId, null);
      } catch (err) {
        const msg = String(err instanceof Error ? err.message : err);
        atto.ui.setBlockStatus(blockId, "translation failed");
        atto.log(`translate: ${msg}`);
        if (msg !== warned) {
          warned = msg;
          atto.ui.notify(`translate: ${msg}`, "warning");
        }
      }
    });
  };

  atto.on("message_end", async (e) => {
    await ready;
    if (settings.enabled) translateBlock(e.blockId, e.text);
  });
  atto.on("reasoning_end", async (e) => {
    await ready;
    if (settings.enabled && settings.thinking) translateBlock(e.blockId, e.text);
  });

  // --- /translate ---

  const describe = () =>
    `translate: ${settings.enabled ? "on" : "off"} · ${settings.lang} · ` +
    (settings.engine === "apple" ? "apple" : `model ${settings.model || "(none set)"}`) +
    ` · thinking ${settings.thinking ? "on" : "off"}`;

  atto.registerCommand("translate", {
    description: "Show answers in your language (experimental): on|off, lang <code>, engine apple|model <p/id>, thinking on|off",
    handler: async (args, ctx) => {
      await ready;
      const [cmd, ...rest] = args.trim().split(/\s+/).filter(Boolean);
      switch (cmd) {
        case undefined:
          break;
        case "on":
        case "off":
          settings.enabled = cmd === "on";
          break;
        case "lang":
          if (!rest[0]) return ctx.ui.notify("usage: /translate lang <code> (ko, en, ja, ...)", "warning");
          settings.lang = rest[0];
          break;
        case "engine":
          if (rest[0] === "apple") settings.engine = "apple";
          else if (rest[0] === "model" && rest[1]) {
            settings.engine = "model";
            settings.model = rest[1];
          } else return ctx.ui.notify("usage: /translate engine apple | model <provider/id>", "warning");
          break;
        case "thinking":
          if (rest[0] !== "on" && rest[0] !== "off") return ctx.ui.notify("usage: /translate thinking on|off", "warning");
          settings.thinking = rest[0] === "on";
          break;
        default:
          return ctx.ui.notify("usage: /translate [on|off|lang <code>|engine apple|engine model <p/id>|thinking on|off]", "warning");
      }
      if (cmd) save();
      warned = "";
      showStatus();
      ctx.ui.notify(describe());
      // Build the helper now rather than at the first answer.
      if (settings.enabled && settings.engine === "apple") {
        buildHelper().catch((err) => ctx.ui.notify(`translate: ${err instanceof Error ? err.message : err}`, "warning"));
      }
    },
  });
}
