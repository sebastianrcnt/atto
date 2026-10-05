# Example extensions

Copy one to `~/.atto/extensions/` (or a project's `.atto/extensions/`) and run `/reload`. The API is in `atto extensions docs` and `atto extensions types`; the built-in `/diff` (`extensions/builtin/diff.ts`) is another example.

| File | What it does |
| --- | --- |
| `translate.ts` | Experimental: shows answers and thinking in your language (`/translate on`), with Apple's on-device Translation or a model; off until turned on |
| `translate-thinking.ts` | Shows reasoning blocks translated by a small local model, display only |
