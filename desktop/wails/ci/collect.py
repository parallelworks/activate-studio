"""Gathers the self-test results of every mode into measurements.json and a
table for the job summary.

Usage: collect.py OUT_ROOT --os NAME --binary PATH [--installer PATH]
                  [--deb PATH] [--build-seconds N] [--web-build-seconds N]
                  [--deps FILE]
"""
import argparse
import json
import os
import sys


def load(path):
    try:
        with open(path, encoding="utf-8-sig") as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def size(path):
    try:
        return os.path.getsize(path) if path else None
    except OSError:
        return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("root")
    ap.add_argument("--os", required=True)
    ap.add_argument("--binary", required=True)
    ap.add_argument("--installer")
    ap.add_argument("--deb")
    ap.add_argument("--build-seconds", type=float)
    ap.add_argument("--web-build-seconds", type=float)
    ap.add_argument("--deps")
    a = ap.parse_args()
    os.makedirs(a.root, exist_ok=True)

    out = {
        "os": a.os,
        "binaryBytes": size(a.binary),
        "installerBytes": size(a.installer),
        "debBytes": size(a.deb),
        "shellBuildSeconds": a.build_seconds,
        "studioBuildSeconds": a.web_build_seconds,
        "runtimeDeps": open(a.deps).read().splitlines() if a.deps and os.path.exists(a.deps) else None,
        "modes": {},
    }
    for mode in sorted(os.listdir(a.root)):
        d = os.path.join(a.root, mode)
        r = load(os.path.join(d, "results.json"))
        after = load(os.path.join(d, "after-exit.json"))
        hk = load(os.path.join(d, "hardkill.json"))
        if hk is not None:
            out["hardKill"] = hk
            continue
        if r is None and after is None:
            continue
        s = (r or {}).get("summary", {})
        mem = (r or {}).get("idleMemory", {})
        checks = (r or {}).get("checks", {}) or {}
        out["modes"][mode] = {
            "summary": s,
            "coldStartMs": s.get("coldStartMs"),
            "serverReadyMs": s.get("serverReadyMs"),
            "shellRSSKB": mem.get("shellRSSKB"),
            "webviewRSSKB": mem.get("webviewRSSKB"),
            "afterExit": after,
            "timeout": (r or {}).get("timeout", False),
            "userAgent": checks.get("userAgent"),
            "origin": checks.get("origin"),
            "stream": {k: (checks.get(k) or {}).get("arrivals") for k in ("stream", "streamDirect", "chatStream")},
            "eventSource": (checks.get("eventSource") or {}).get("arrivals"),
            "sandbox": open(os.path.join(d, "sandbox.txt")).read().strip() if os.path.exists(os.path.join(d, "sandbox.txt")) else None,
        }
    json.dump(out, open(os.path.join(a.root, "measurements.json"), "w"), indent=2)

    lines = [f"### {a.os}", "", f"shell binary {out['binaryBytes']} bytes, installer {out['installerBytes']}, deb {out['debBytes']}, shell build {a.build_seconds} s", ""]
    lines.append("| mode | cold start ms | server ready ms | shell RSS KB | web view RSS KB | stream via mode path | orphans after exit |")
    lines.append("|---|---|---|---|---|---|---|")
    for mode, m in out["modes"].items():
        st = (m["summary"] or {}).get("streaming", {}) if m["summary"] else {}
        alive = (m["afterExit"] or {}).get("aliveAfterExit")
        lines.append(f"| {mode} | {m['coldStartMs']} | {m['serverReadyMs']} | {m['shellRSSKB']} | {m['webviewRSSKB']} | {st.get('fetchThroughModePath')} | {alive} |")
    if "hardKill" in out:
        lines += ["", f"hard kill: {json.dumps(out['hardKill'])}"]
    text = "\n".join(lines) + "\n"
    print(text)
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as f:
            f.write(text)
    json.dump(out, sys.stdout, indent=2)


if __name__ == "__main__":
    main()
