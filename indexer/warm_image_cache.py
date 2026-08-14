#!/usr/bin/env python3
"""One-time parallel warmer for the image extract cache.

Captioning every image inline during an enrichment pass would serialize
minutes of vision-model calls; this script fills the mtime-checked cache in
parallel so enrich.py finds every image already extracted. Safe to re-run:
fresh cache entries are skipped.
"""
import argparse
import os
import sys
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from enrich import EXCLUDE_DIRS, IMAGE_SUFFIXES, MIN_IMAGE_BYTES, extract_image  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--kb-root', required=True)
    ap.add_argument('--extract-cache', required=True)
    ap.add_argument('--workers', type=int, default=6)
    args = ap.parse_args()

    kb_root = Path(args.kb_root)
    cache_root = Path(args.extract_cache)

    todo = []
    for dirpath, dirnames, filenames in os.walk(kb_root):
        dirnames[:] = sorted(d for d in dirnames if d not in EXCLUDE_DIRS and not d.startswith('.'))
        for fname in filenames:
            fpath = Path(dirpath) / fname
            if fpath.suffix.lower() not in IMAGE_SUFFIXES:
                continue
            if fpath.stat().st_size < MIN_IMAGE_BYTES:
                continue
            rel = fpath.relative_to(kb_root)
            cache_file = cache_root / rel.parent / (fname + '.txt')
            if cache_file.exists() and cache_file.stat().st_mtime >= fpath.stat().st_mtime:
                continue
            todo.append((fpath, cache_file))

    print(f'{len(todo)} images to extract', flush=True)
    done = 0

    def work(item):
        nonlocal done
        fpath, cache_file = item
        text = extract_image(fpath)
        cache_file.parent.mkdir(parents=True, exist_ok=True)
        cache_file.write_text(text or '')
        done += 1
        if done % 25 == 0:
            print(f'  {done}/{len(todo)}', flush=True)

    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        list(pool.map(work, todo))
    print(f'warmed {len(todo)} image cache entries')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
