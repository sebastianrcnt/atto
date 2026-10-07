#!/usr/bin/env python3
"""Run agent and one-request baseline serially; check only the exit report."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import time


def correct(problem, report):
    report = report.strip()
    if problem["checker"] == "exact":
        return report == problem["expected"]
    if problem["checker"] == "regexp":
        return re.fullmatch(problem["expected"], report) is not None
    raise ValueError(problem["checker"])


def run(binary, problem, mode, out):
    stem = out / f'{problem["id"]}-{mode}'
    metrics = stem.with_suffix(".json")
    command = [binary, "-grant", "now,exit", "-steps", "30", "-v", "-metrics", str(metrics)]
    if mode == "baseline":
        command.append("-baseline")
    command.append(problem["prompt"])
    start = time.monotonic()
    with stem.with_suffix(".stdout").open("w") as stdout, stem.with_suffix(".trace").open("w") as stderr:
        result = subprocess.run(command, stdout=stdout, stderr=stderr, check=False)
    stats = json.loads(metrics.read_text())
    stats.update(id=problem["id"], mode=mode, wall_seconds=round(time.monotonic()-start, 3),
                 correct=result.returncode == 0 and correct(problem, stats["report"]))
    metrics.write_text(json.dumps(stats, indent=2) + "\n")
    print(f'{problem["id"]} {mode}: {stats["correct"]}, {stats["steps"]} steps', flush=True)
    return stats


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    parser.add_argument("--out", type=Path, default=Path("bench/pure/results"))
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    problems = json.loads(Path("bench/pure/problems.json").read_text())
    summary = {"model": os.getenv("ATTO2_MODEL", "orca-local"),
               "base_url": os.getenv("ATTO2_BASE_URL", "http://192.168.0.235:8081/v1"), "runs": []}
    for problem in problems:
        for mode in ("agent", "baseline"):
            summary["runs"].append(run(args.binary, problem, mode, args.out))
            (args.out / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")


if __name__ == "__main__":
    main()
