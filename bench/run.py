#!/usr/bin/env python3
"""Run one model request at a time; check only sys.exit reports, never stdout."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("binary")
parser.add_argument("--dir", default="/Volumes/t5/atto")
parser.add_argument("--out", default="bench/results")
parser.add_argument("--steps", type=int, default=30)
args = parser.parse_args()
questions = json.loads(Path(__file__).with_name("questions.json").read_text())
out = Path(args.out).resolve()
out.mkdir(parents=True, exist_ok=True)
rows = []
for q in questions:
    print(f"Running {q['id']}...", flush=True)
    metrics = out / f"{q['id']}.json"
    metrics.unlink(missing_ok=True)
    command = [args.binary, "-dir", args.dir, "-v", "-steps", str(args.steps),
               "-metrics", str(metrics), q["question"]]
    with (out / f"{q['id']}.stdout").open("w") as stdout, (out / f"{q['id']}.trace").open("w") as stderr:
        run = subprocess.run(command, stdout=stdout, stderr=stderr, check=False)
    stats = json.loads(metrics.read_text()) if metrics.exists() else {"error": "no metrics produced"}
    report = stats.get("report", "")
    correct = run.returncode == 0 and all(re.search(p, report, re.I) for p in q["patterns"])
    rows.append({"id": q["id"], "correct": bool(correct), "exit_code": run.returncode, **stats})
    print(f"  correct={bool(correct)} steps={stats.get('steps')} prompt_tokens={stats.get('prompt_tokens')} syscalls={stats.get('syscalls')}", flush=True)
source = subprocess.check_output(["git", "-C", args.dir, "rev-parse", "HEAD"], text=True).strip()
result = {"repository_commit": source,
          "base_url": os.getenv("ATTO2_BASE_URL", "http://192.168.0.235:8081/v1"),
          "model": os.getenv("ATTO2_MODEL", "orca-local"), "questions": rows}
(out / "summary.json").write_text(json.dumps(result, indent=2) + "\n")
print("\n| Question | Correct | Steps | Last prompt tokens | Syscalls |")
print("|---|---:|---:|---:|---:|")
for row in rows:
    print(f"| {row['id']} | {'yes' if row['correct'] else 'no'} | {row.get('steps', '—')} | {row.get('prompt_tokens', '—')} | {row.get('syscalls', '—')} |")
