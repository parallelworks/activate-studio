#!/usr/bin/env python3
"""Embed enriched knowledge base text into per-directory vec0 tables.

Chunk (plain sqlite3): cut each file's extracted text (from the fts5 `words`
table written by enrich.py) into a `gchunks` table, each chunk with the hash
of the text that is embedded.

Embed (gufi_sqlite3, which carries sqlite-lembed and sqlite-vec): only the
chunks whose hash is not in the embedding cache are embedded, in batches, by
--jobs gufi_sqlite3 processes side by side, each on one thread (several
single-threaded processes get through more chunks per core than fewer
multi-threaded ones). A batch that crashes the tokenizer is split until the
chunk responsible is found, retried shorter, and skipped if it still fails.

Write: each directory's `gvec` vec0 table is filled from the cache.

The cache (--cache, an SQLite file keyed by the hash) is what makes a
rebuild, or a folder pointed at again, cheap: unchanged text is never
embedded twice. Without --cache, a scratch cache serves this run only.

Chunks are capped per file; full-text search covers the tail. The 384-dim
model is sentence-oriented, so chunks are short. With --progress,
`@progress {...}` lines on stdout say how far it has got.
"""
import argparse
import hashlib
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

CHUNK_CHARS = 1000
CHUNK_OVERLAP = 120
MAX_CHUNKS_PER_FILE = 12
# sqlite-lembed does not truncate: token-dense text (code, markup) can
# overflow the MiniLM context and segfault. 800 chars is safe for prose and
# nearly all code; a chunk that still fails is retried at 400.
EMBED_CAPS = (800, 400)
BATCH = 64
# Markup files stay in full-text search but are not embedded: dense markup
# segfaults the lembed tokenizer and carries little semantic signal anyway.
NO_EMBED_SUFFIXES = ('.svg', '.xml', '.html', '.css', '.json', '.csv', '.tsv')
MODEL_NAME = 'minilm384'


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


def chunk_hash(text: str) -> str:
    """The cache key: the model and the text that is actually embedded."""
    return hashlib.sha1(f'{MODEL_NAME}\0{text[:EMBED_CAPS[0]]}'.encode('utf-8', 'replace')).hexdigest()


def sql_str(s: str) -> str:
    return "'" + s.replace("'", "''") + "'"


class Progress:
    def __init__(self, on: bool):
        self.on, self.last, self.lock = on, 0.0, threading.Lock()
        self.done, self.total, self.step = 0, 0, ''

    def emit(self, force: bool = False) -> None:
        if not self.on:
            return
        now = time.monotonic()
        if force or now - self.last >= 0.25:
            self.last = now
            print('@progress ' + json.dumps({'phase': 'meaning', 'step': self.step, 'done': self.done, 'total': self.total}), flush=True)

    def start(self, step: str, total: int, done: int = 0) -> None:
        with self.lock:
            self.step, self.total, self.done = step, total, done
            self.emit(True)

    def add(self, n: int = 1) -> None:
        with self.lock:
            self.done += n
            self.emit()


def default_jobs() -> int:
    try:
        return max(1, len(os.sched_getaffinity(0)))
    except AttributeError:
        return max(1, os.cpu_count() or 1)


def open_cache(path: Path) -> sqlite3.Connection:
    path.parent.mkdir(parents=True, exist_ok=True)
    db = sqlite3.connect(str(path), timeout=120)
    db.execute('PRAGMA synchronous=OFF')
    db.execute('CREATE TABLE IF NOT EXISTS emb (h TEXT PRIMARY KEY, v BLOB) WITHOUT ROWID')
    db.commit()
    return db


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--index', required=True, help='GUFI index tree top')
    ap.add_argument('--model', required=True, help='GGUF embedding model path')
    ap.add_argument('--gufi-sqlite3',
                    default=os.path.join(os.environ.get('GUFI_BIN', '/opt/gufi/bin'), 'gufi_sqlite3'),
                    help='gufi_sqlite3 binary; defaults under $GUFI_BIN')
    ap.add_argument('--subdir', default='', help='restrict to this KB-relative subtree (incremental indexing)')
    ap.add_argument('--no-recurse', action='store_true', help='process only the subdir itself, not its children')
    ap.add_argument('--jobs', type=int, default=0, help='embedding processes side by side (default: the CPU count)')
    ap.add_argument('--cache', default='', help='embedding cache (SQLite), reused across runs')
    ap.add_argument('--prune', action='store_true', help='drop cache entries this index no longer uses (whole-index runs only)')
    ap.add_argument('--progress', action='store_true', help='print @progress lines on stdout')
    args = ap.parse_args()

    jobs = args.jobs if args.jobs > 0 else default_jobs()
    index_root = Path(args.index)
    walk_root = index_root / args.subdir if args.subdir else index_root
    progress = Progress(args.progress)
    dbs = []
    for dirpath, dirnames, _ in os.walk(walk_root):
        if args.no_recurse:
            dirnames[:] = []
        dirnames.sort()
        dbfile = Path(dirpath) / 'db.db'
        if dbfile.exists():
            dbs.append(dbfile)

    scratch = Path(tempfile.mkdtemp(prefix='embed-', dir=os.environ.get('STUDIO_TMP') or None))
    try:
        return run(args, jobs, dbs, scratch, progress)
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


def run(args, jobs: int, dbs: list, scratch: Path, progress: Progress) -> int:
    cache_path = Path(args.cache) if args.cache else scratch / 'cache.db'
    cache = open_cache(cache_path)
    known = {h for (h,) in cache.execute('SELECT h FROM emb')}
    cache.close()

    # Chunk into gchunks, each directory on its own, several at a time.
    # Chunks whose hash the cache lacks go to a pending table, once each.
    pending = sqlite3.connect(str(scratch / 'pending.db'), check_same_thread=False)
    pending.execute('PRAGMA synchronous=OFF')
    pending.execute('PRAGMA journal_mode=OFF')
    pending.execute('CREATE TABLE pending (h TEXT PRIMARY KEY, t TEXT)')
    queued: set = set()
    used: set = set()
    with_chunks: list = []
    lock = threading.Lock()

    def chunk_db(dbfile: Path):
        db = sqlite3.connect(str(dbfile), timeout=60)
        try:
            db.execute('PRAGMA synchronous=OFF')
            has_vec = db.execute("SELECT 1 FROM sqlite_master WHERE name = 'gvec'").fetchone() is not None
            db.execute('DROP TABLE IF EXISTS gchunks')
            db.execute('CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT)')
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
                        ins.append((str(tinode), fname, seq, c, chunk_hash(c)))
            db.executemany('INSERT INTO gchunks (tinode, fname, seq, ctext, h) VALUES (?, ?, ?, ?, ?)', ins)
            db.commit()
        finally:
            db.close()
        return dbfile, has_vec, ins

    progress.start('chunk', 0)
    n_chunks = 0
    with ThreadPoolExecutor(max_workers=min(jobs, 8)) as pool:
        for dbfile, has_vec, ins in pool.map(chunk_db, dbs):
            n_chunks += len(ins)
            if ins or has_vec:
                # A directory with no chunks keeps no vector table: a stale
                # one from an earlier pass is dropped in the write step.
                with_chunks.append(dbfile)
            new = []
            for _, _, _, c, h in ins:
                used.add(h)
                if h not in known and h not in queued:
                    queued.add(h)
                    new.append((h, c[:EMBED_CAPS[0]]))
            if new:
                with lock:
                    pending.executemany('INSERT OR IGNORE INTO pending (h, t) VALUES (?, ?)', new)
    pending.commit()
    n_pending = pending.execute('SELECT count(*) FROM pending').fetchone()[0]
    pending.close()
    print(f'chunked: {n_chunks} chunks across {len(dbs)} dbs, {n_pending} to embed, {n_chunks - n_pending} from the cache', flush=True)

    env = {**os.environ, 'OMP_THREAD_LIMIT': '1'}
    model = sql_str(args.model)
    pending_db = sql_str(str(scratch / 'pending.db'))

    def sqlite3_run(script: str, timeout: int = 3600) -> tuple[bool, str]:
        try:
            proc = subprocess.run([args.gufi_sqlite3], input=script, capture_output=True, text=True, timeout=timeout, env=env)
        except subprocess.TimeoutExpired:
            return False, 'timed out'
        ok = proc.returncode == 0 and 'Error' not in proc.stderr
        return ok, proc.stderr.strip()[-300:] or f'exit {proc.returncode}'

    # Embed the pending chunks: rowid ranges of BATCH, one gufi_sqlite3 per
    # range, writing to its own output file so the processes never wait on
    # one another's locks.
    errors: list = []
    if n_pending:
        progress.start('embed', n_pending + len(with_chunks))
        slots = list(range(jobs))
        slot_lock = threading.Lock()

        def embed_range(lo: int, hi: int, cap: int, out: Path) -> bool:
            script = '\n'.join([
                f"INSERT INTO temp.lembed_models(name, model) SELECT '{MODEL_NAME}', lembed_model_from_file({model});",
                f'ATTACH {pending_db} AS p;',
                f'ATTACH {sql_str(str(out))} AS o;',
                'PRAGMA o.synchronous=OFF;',
                'CREATE TABLE IF NOT EXISTS o.e (h TEXT PRIMARY KEY, v BLOB);',
                f"INSERT OR REPLACE INTO o.e SELECT h, lembed('{MODEL_NAME}', substr(t, 1, {cap})) FROM p.pending WHERE rowid BETWEEN {lo} AND {hi};",
            ])
            ok, err = sqlite3_run(script)
            if ok:
                return True
            if lo < hi:
                mid = (lo + hi) // 2
                a = embed_range(lo, mid, cap, out)
                b = embed_range(mid + 1, hi, cap, out)
                return a and b
            if cap != EMBED_CAPS[-1]:
                return embed_range(lo, hi, EMBED_CAPS[-1], out)
            errors.append(f'chunk {lo}: {err}')
            return False

        def batch(lo: int) -> None:
            with slot_lock:
                slot = slots.pop()
            try:
                hi = min(lo + BATCH - 1, n_pending)
                embed_range(lo, hi, EMBED_CAPS[0], scratch / f'out-{slot}.db')
                progress.add(hi - lo + 1)
            finally:
                with slot_lock:
                    slots.append(slot)

        with ThreadPoolExecutor(max_workers=jobs) as pool:
            for _ in pool.map(batch, range(1, n_pending + 1, BATCH)):
                pass

        cache = open_cache(cache_path)
        for out in sorted(scratch.glob('out-*.db')):
            cache.execute(f'ATTACH {sql_str(str(out))} AS o')
            try:
                cache.execute('INSERT OR IGNORE INTO emb (h, v) SELECT h, v FROM o.e')
                cache.commit()
            except sqlite3.OperationalError as exc:
                errors.append(f'{out.name}: {exc}')
            cache.execute('DETACH o')
        cache.close()
    else:
        progress.start('write', len(with_chunks))

    # Write each directory's vector table from the cache, a group of
    # directories per gufi_sqlite3 process.
    cache_db = sql_str(str(cache_path))
    groups = [with_chunks[i:i + 25] for i in range(0, len(with_chunks), 25)]

    def write_group(group: list) -> None:
        lines = [f'ATTACH {cache_db} AS c;']
        for dbfile in group:
            lines += [
                f'ATTACH {sql_str(str(dbfile))} AS d;',
                'PRAGMA d.synchronous=OFF;',
                'BEGIN;',
                'DROP TABLE IF EXISTS d.gvec;',
                'CREATE VIRTUAL TABLE d.gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);',
                'INSERT INTO d.gvec(cid, fp384) SELECT g.cid, e.v FROM d.gchunks g JOIN c.emb e ON e.h = g.h;',
                'COMMIT;',
                'DETACH d;',
            ]
        ok, err = sqlite3_run('\n'.join(lines))
        if not ok:
            # One directory at a time, to name the one that failed.
            for dbfile in group:
                one = '\n'.join([lines[0]] + lines[1 + 8 * group.index(dbfile):1 + 8 * (group.index(dbfile) + 1)])
                ok1, err1 = sqlite3_run(one)
                if not ok1:
                    errors.append(f'{dbfile}: {err1}')
        progress.add(len(group))

    with ThreadPoolExecutor(max_workers=max(1, min(jobs, 8))) as pool:
        for _ in pool.map(write_group, groups):
            pass

    if args.prune and args.cache and not args.subdir and not args.no_recurse:
        cache = open_cache(cache_path)
        cache.execute('CREATE TEMP TABLE used (h TEXT PRIMARY KEY)')
        cache.executemany('INSERT OR IGNORE INTO temp.used VALUES (?)', ((h,) for h in used))
        gone = cache.execute('DELETE FROM emb WHERE h NOT IN (SELECT h FROM temp.used)').rowcount
        cache.commit()
        cache.close()
        if gone:
            print(f'pruned {gone} unused cache entries', flush=True)

    progress.emit(True)
    for err in errors[:10]:
        print('embed error:', err, file=sys.stderr)
    print(f'embedded: {n_pending} new chunks, {len(with_chunks)} directory tables written, {len(errors)} errors', flush=True)
    # A chunk the tokenizer cannot take is skipped, not a failed pass: the
    # rest of the directory is searchable by meaning, and all of it by text.
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
