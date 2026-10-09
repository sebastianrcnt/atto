# Example extensions

Copy one to `~/.atto/extensions/` (or a project's `.atto/extensions/`) and run `/reload`. The API is in `atto extensions docs` and `atto extensions types`; the native `/diff` (`extensions/native_diff.go`) is another example.

| File | What it does |
| --- | --- |
| `translate.ts` | Experimental: shows answers and thinking in your language (`/translate on`), with Apple's on-device Translation or a model; off until turned on |
| `translate-thinking.ts` | Shows reasoning blocks translated by a small local model, display only |
| `token-speed.ts` | Shows the last response's generation speed (tok/s) and time to first token in the status line |

| `counter.tsx` | `/counter` opens a shared pane with a persisted counter and button |
| `review-tools.ts` | Wraps native tool rows using `next()` |

Translation work runs in message/reasoning observers, never in render hooks.
The bounded worker cache supplies `assistantMessage` display overrides via
`next({...e, props:{...e.props, text}})`; overlays are persisted by the worker.
Caches are ephemeral and older entries replay passively, without retranslation.
