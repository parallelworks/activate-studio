#!/usr/bin/env python3
"""GUFI-shaped scaffolding for the extraction test.

The real index is built by gufi_dir2index, which does not build on macOS and
is a heavy dependency for a test whose subject is text extraction. What
enrich.py actually requires of the index is narrow: one db.db per directory,
mirroring the corpus tree. This creates that, and reads back what enrich.py
wrote, so the test can assert on the rows search and chat would see.

    index-helper.py init  <index-root> <kb-root>   # empty db.db per directory
    index-helper.py dump  <index-root>             # {relpath: text} as JSON
"""
import json
import os
import sqlite3
import sys
from pathlib import Path


def init(index_root: Path, kb_root: Path) -> int:
    for dirpath, dirnames, _ in os.walk(kb_root):
        dirnames.sort()
        rel = Path(dirpath).relative_to(kb_root)
        target = index_root / rel
        target.mkdir(parents=True, exist_ok=True)
        sqlite3.connect(str(target / 'db.db')).close()
    return 0


def dump(index_root: Path) -> int:
    out: dict[str, str] = {}
    for dirpath, dirnames, _ in os.walk(index_root):
        dirnames.sort()
        db_file = Path(dirpath) / 'db.db'
        if not db_file.exists():
            continue
        rel_dir = Path(dirpath).relative_to(index_root)
        db = sqlite3.connect(str(db_file))
        try:
            rows = db.execute('SELECT fname, wordf FROM words').fetchall()
        except sqlite3.OperationalError:
            rows = []
        finally:
            db.close()
        for fname, wordf in rows:
            key = str(rel_dir / fname) if str(rel_dir) != '.' else fname
            out[key] = wordf or ''
    json.dump(out, sys.stdout)
    return 0


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__, file=sys.stderr)
        return 2
    cmd = sys.argv[1]
    if cmd == 'init':
        return init(Path(sys.argv[2]), Path(sys.argv[3]))
    if cmd == 'dump':
        return dump(Path(sys.argv[2]))
    print(f'unknown command: {cmd}', file=sys.stderr)
    return 2


if __name__ == '__main__':
    raise SystemExit(main())
