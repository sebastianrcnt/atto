// Numbers as the terminal writes them (tui/format.go, app/statusline.go).

// duration is tui.FormatDuration: 80ms, 4.2s, 3m 07s, 1h 05m.
export function duration(ms: number): string {
  if (ms < 100) return `${Math.max(0, Math.round(ms))}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const pad = (n: number) => String(n).padStart(2, "0");
  if (s < 3600) {
    const m = Math.floor(s / 60);
    return `${m}m ${pad(Math.floor(s) - 60 * m)}s`;
  }
  const h = Math.floor(s / 3600);
  return `${h}h ${pad(Math.floor(s / 60) - 60 * h)}m`;
}

// tokens is tui.FormatTokens: 950, 12.5k, 1.2M.
export function tokens(n: number): string {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
  return String(n);
}

// compact is pi's footer notation (compactTokens): 950, 1.2k, 12k, 1.2M.
export function compact(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10000) return (n / 1e3).toFixed(1).replace(/\.0$/, "") + "k";
  if (n < 1e6) return Math.round(n / 1e3) + "k";
  if (n < 1e7) return (n / 1e6).toFixed(1) + "M";
  return Math.round(n / 1e6) + "M";
}
