#!/usr/bin/env python3
import argparse
import datetime as _dt
import json
from pathlib import Path
from typing import Dict, List


def parse_suite(name: str, path: Path) -> Dict:
    total = passed = failed = skipped = 0
    failures: List[Dict[str, str]] = []
    elapsed = 0.0
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        action = event.get("Action")
        test_name = event.get("Test")
        if action == "run" and test_name:
            total += 1
        elif action == "pass" and test_name:
            passed += 1
            elapsed += float(event.get("Elapsed", 0.0) or 0.0)
        elif action == "fail" and test_name:
            failed += 1
            elapsed += float(event.get("Elapsed", 0.0) or 0.0)
            failures.append({
                "name": test_name,
                "output": event.get("Output", ""),
            })
        elif action == "skip" and test_name:
            skipped += 1
    return {
        "name": name,
        "file": str(path),
        "total": total,
        "passed": passed,
        "failed": failed,
        "skipped": skipped,
        "elapsed_seconds": round(elapsed, 6),
        "failures": failures,
        "ok": failed == 0,
    }


def write_markdown(path: Path, phase: str, suites: List[Dict], ok: bool) -> None:
    with path.open("w", encoding="utf-8") as fh:
        fh.write(f"# {phase.upper()} Test Summary\n\n")
        status = "PASS" if ok else "FAIL"
        fh.write(f"Overall status: **{status}**\n\n")
        fh.write("| Suite | Total | Passed | Failed | Skipped | Duration (s) |\n")
        fh.write("| --- | --- | --- | --- | --- | --- |\n")
        for suite in suites:
            fh.write(
                f"| {suite['name']} | {suite['total']} | {suite['passed']} | {suite['failed']} | {suite['skipped']} | {suite['elapsed_seconds']} |\n"
            )
        fh.write("\n")
        for suite in suites:
            if suite["failures"]:
                fh.write(f"## Failures — {suite['name']}\n\n")
                for failure in suite["failures"]:
                    fh.write(f"- **{failure['name']}**\n")
                    output = failure.get("output", "").strip()
                    if output:
                        fh.write("```\n")
                        fh.write(output)
                        fh.write("\n```\n")
                fh.write("\n")


def main() -> None:
    parser = argparse.ArgumentParser(description="Summarise Go test JSON output")
    parser.add_argument("--phase", required=True)
    parser.add_argument("--suite", action="append", required=True, help="name=path to JSON stream")
    parser.add_argument("--out", required=True)
    parser.add_argument("--markdown", default="")
    args = parser.parse_args()

    suites: List[Dict] = []
    ok = True
    for entry in args.suite:
        if "=" not in entry:
            raise SystemExit(f"invalid suite entry: {entry}")
        name, path = entry.split("=", 1)
        suite_path = Path(path)
        suites.append(parse_suite(name, suite_path))
        ok = ok and suites[-1]["ok"]

    summary = {
        "phase": args.phase,
        "generated_at": _dt.datetime.utcnow().replace(microsecond=0).isoformat() + "Z",
        "ok": ok,
        "suites": suites,
    }

    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")

    if args.markdown:
        md_path = Path(args.markdown)
        md_path.parent.mkdir(parents=True, exist_ok=True)
        write_markdown(md_path, args.phase, suites, ok)


if __name__ == "__main__":
    main()
