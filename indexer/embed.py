#!/usr/bin/env python3
"""Embed enriched knowledge base text into per-directory vec0 tables.

Phase A (plain sqlite3): chunk each file's extracted text (from the fts5
`words` table written by enrich.py) into a `gchunks` table.
Phase B (gufi_sqlite3, which carries sqlite-lembed and sqlite-vec): load the
GGUF embedding model once, then for each directory db ATTACH, rebuild the
`gvec` vec0 table from gchunks, DETACH.

Chunks are capped per file; full-text search covers the tail. The 384-dim
model is sentence-oriented, so chunks are short.
"""
import argparse
import os
import sqlite3
import subprocess
import sys
from pathlib import Path

CHUNK_CHARS = 1000
CHUNK_OVERLAP = 120
MAX_CHUNKS_PER_FILE = 12
# Markup files stay in full-text search but are not embedded: dense markup
# segfaults the lembed tokenizer and carries little semantic signal anyway.
NO_EMBED_SUFFIXES = ('.svg', '.xml', '.html', '.css', '.json', '.csv', '.tsv')


def prose_like(c: str) -> bool:
    letters = sum(ch.isalpha() or ch.isspace() for ch in c)
    return letters / max(len(c), 1) > 0.6


def chunk(text: str) -> list[str]:
    out = []
    i = 0
    while i < len(text) and len(out) < MAX_CHUNKS_PER_FILE:
        c = text[i:i + CHUNK_CHARS]
        if prose_like(c):
            out.append(c)
        i += CHUNK_CHARS - CHUNK_OVERLAP
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--index', required=True, help='GUFI index tree top')
    ap.add_argument('--model', required=True, help='GGUF embedding model path')
    ap.add_argument('--gufi-sqlite3',
                    default=os.path.join(os.environ.get('GUFI_BIN', '/opt/gufi/bin'), 'gufi_sqlite3'),
                    help='gufi_sqlite3 binary; defaults under $GUFI_BIN')
    ap.add_argument('--subdir', default='', help='restrict to this KB-relative subtree (incremental indexing)')
    ap.add_argument('--no-recurse', action='store_true', help='process only the subdir itself, not its children')
    args = ap.parse_args()

    index_root = Path(args.index)
    walk_root = index_root / args.subdir if args.subdir else index_root
    dbs = []
    for dirpath, dirnames, _ in os.walk(walk_root):
        if args.no_recurse:
            dirnames[:] = []
        dirnames.sort()
        dbfile = Path(dirpath) / 'db.db'
        if dbfile.exists():
            dbs.append(dbfile)

    # Phase A: chunk into gchunks.
    n_chunks = 0
    for dbfile in dbs:
        db = sqlite3.connect(str(dbfile))
        try:
            db.execute('DROP TABLE IF EXISTS gchunks')
            db.execute('CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode INTEGER, fname TEXT, seq INTEGER, ctext TEXT)')
            try:
                rows = db.execute('SELECT tinode, fname, wordf FROM words').fetchall()
            except sqlite3.OperationalError:
                rows = []
            ins = []
            for tinode, fname, wordf in rows:
                if fname.lower().endswith(NO_EMBED_SUFFIXES):
                    continue
                for seq, c in enumerate(chunk(wordf or '')):
                    if c.strip():
                        ins.append((tinode, fname, seq, c))
            db.executemany('INSERT INTO gchunks (tinode, fname, seq, ctext) VALUES (?, ?, ?, ?)', ins)
            db.commit()
            n_chunks += len(ins)
        finally:
            db.close()
    print(f'chunked: {n_chunks} chunks across {len(dbs)} dbs', flush=True)

    # Phase B: embeddings via gufi_sqlite3 (lembed + vec0 live there).
    # One process per directory db so a bad row cannot abort the whole pass.
    from concurrent.futures import ThreadPoolExecutor

    def has_chunks(dbfile: Path) -> bool:
        db = sqlite3.connect(str(dbfile))
        try:
            return db.execute('SELECT count(*) FROM gchunks').fetchone()[0] > 0
        except sqlite3.OperationalError:
            return False
        finally:
            db.close()

    def embed_db(dbfile: Path) -> str | None:
        p = str(dbfile).replace("'", "''")
        # sqlite-lembed does not truncate: token-dense text (code, markup)
        # can overflow the MiniLM context and segfault. 800 chars is safe for
        # prose and nearly all code; retry denser content at 400.
        for cap in (800, 400):
            script = '\n'.join([
                "INSERT INTO temp.lembed_models(name, model) SELECT 'minilm384', lembed_model_from_file('%s');" % args.model,
                f"ATTACH '{p}' AS d;",
                'DROP TABLE IF EXISTS d.gvec;',
                'CREATE VIRTUAL TABLE d.gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);',
                f"INSERT INTO d.gvec(cid, fp384) SELECT cid, lembed('minilm384', substr(ctext, 1, {cap})) FROM d.gchunks WHERE length(trim(ctext)) > 0;",
                'DETACH d;',
            ])
            proc = subprocess.run([args.gufi_sqlite3], input=script,
                                  capture_output=True, text=True, timeout=1800)
            if proc.returncode == 0 and 'Error' not in proc.stderr:
                return None
        return f'{dbfile}: {proc.stderr.strip()[-300:] or f"exit {proc.returncode}"}'

    targets = [d for d in dbs if has_chunks(d)]
    errors = []
    with ThreadPoolExecutor(max_workers=4) as pool:
        for err in pool.map(embed_db, targets):
            if err:
                errors.append(err)
    for err in errors[:10]:
        print('embed error:', err, file=sys.stderr)
    print(f'embedded: {len(targets) - len(errors)}/{len(targets)} dbs ok, {len(errors)} errors', flush=True)
    return 0 if not errors else 1


if __name__ == '__main__':
    raise SystemExit(main())
