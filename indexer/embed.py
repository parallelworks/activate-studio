#!/usr/bin/env python3
"""Embed enriched knowledge base text into per-directory vec0 tables.

Chunk (plain sqlite3): cut each file's extracted text (from the fts5 `words`
table written by enrich.py) into a `gchunks` table, each chunk with the hash
of the text that is embedded.

Embed: only the chunks whose hash is not in the embedding cache are
embedded. Two backends give the same 384 dimensions:

  onnx    all-MiniLM-L6-v2, int8, mean pooling, through ONNX Runtime
          (embed_onnx.py; model id minilm-l6-mean-int8, with -t128 when
          --max-tokens 128 cuts passages shorter). Used whenever its
          packages import and --onnx names a folder with the model. The
          --jobs threads are shared by a few processes, each embedding
          batches with several ONNX Runtime threads.
  lembed  the GGUF model through sqlite-lembed in gufi_sqlite3 (model id
          minilm384), the fallback, as on a remote host without the ONNX
          packages. It takes the first token's state rather than the mean,
          which the model was not trained for, and costs more than twice
          the CPU per chunk. A batch that crashes its tokenizer is split
          until the chunk responsible is found, retried shorter, and
          skipped if it still fails.

Write: each directory's `gvec` vec0 table is filled from the cache, and its
`gvecmodel` table names the model the vectors came from, which a query has
to embed with too; a directory without one holds minilm384 vectors. A run
over one folder of an index whose vectors are from another model widens to
the whole index, so an index never mixes the two.

The cache (--cache, an SQLite file keyed by the hash) is what makes a
rebuild, or a folder pointed at again, cheap: unchanged text is never
embedded twice. Without --cache, a scratch cache serves this run only.

Chunks are capped per file (--max-chunks, 12 by default); full-text search
covers the tail. The 384-dim model is sentence-oriented, so chunks are
short. --documents-only embeds documents and images (PDF, Office, text,
Markdown, and the descriptions and OCR text of images) and leaves code,
data and the rest to name and full-text search: the Studio's "Documents"
level of search by meaning, with --max-chunks 4. With --progress,
`@progress {...}` lines on stdout say how far it has got.
"""
import argparse
import hashlib
import json
import multiprocessing
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from concurrent.futures import ProcessPoolExecutor, ThreadPoolExecutor, as_completed
from concurrent.futures.process import BrokenProcessPool
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
# What --documents-only embeds: prose documents, and images, whose text is
# a description or OCR. Spreadsheets, code and data stay in full-text search.
DOCUMENT_SUFFIXES = (
    '.pdf', '.doc', '.docx', '.odt', '.rtf', '.ppt', '.pptx', '.odp',
    '.md', '.markdown', '.txt', '.rst', '.tex', '.org', '.adoc',
    '.png', '.jpg', '.jpeg', '.gif', '.webp', '.tif', '.tiff',
)
# The model this run embeds with: MODEL_NAME, or the ONNX backend's id.
MODEL_ID = MODEL_NAME
# Rows per task handed to an ONNX worker process.
ONNX_TASK = 256


# A words row written before enrich.py dropped the converter's provenance
# line may still start with it; passages are cut from the text without it.
PROVENANCE_PREFIX = '<!-- extracted by downmark '


def index_text(text: str) -> str:
    if text.startswith(PROVENANCE_PREFIX):
        nl = text.find('\n')
        return '' if nl < 0 else text[nl + 1:].lstrip('\n')
    return text


def prose_like(c: str) -> bool:
    letters = sum(ch.isalpha() or ch.isspace() for ch in c)
    return letters / max(len(c), 1) > 0.6


def embeds_file(fname: str, documents_only: bool) -> bool:
    """Whether a file's text is embedded: never markup, and with
    --documents-only, only documents and images."""
    low = fname.lower()
    if low.endswith(NO_EMBED_SUFFIXES):
        return False
    return not documents_only or low.endswith(DOCUMENT_SUFFIXES)


def chunk(text: str, cap: int = MAX_CHUNKS_PER_FILE) -> list[str]:
    out = []
    i = 0
    while i < len(text) and len(out) < cap:
        c = text[i:i + CHUNK_CHARS]
        if prose_like(c):
            out.append(c)
        i += CHUNK_CHARS - CHUNK_OVERLAP
    return out


def chunk_hash(text: str) -> str:
    """The cache key: the model and the text that is actually embedded."""
    return hashlib.sha1(f'{MODEL_ID}\0{text[:EMBED_CAPS[0]]}'.encode('utf-8', 'replace')).hexdigest()


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


# ---- the ONNX backend's worker processes ----

_embedder = None


def _onnx_init(model_dir: str, threads: int, max_tokens: int) -> None:
    global _embedder
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    import embed_onnx
    _embedder = embed_onnx.Embedder(model_dir, threads, max_tokens)


def _onnx_task(pending_db: str, lo: int, hi: int) -> list:
    db = sqlite3.connect(pending_db, timeout=60)
    try:
        rows = db.execute('SELECT h, t FROM pending WHERE rowid BETWEEN ? AND ?', (lo, hi)).fetchall()
    finally:
        db.close()
    blobs = _embedder.embed([t for _, t in rows])
    return [(h, b) for (h, _), b in zip(rows, blobs)]


def onnx_layout(jobs: int) -> tuple:
    """Processes and threads each for a --jobs thread budget: a few
    processes of several threads, which embed as fast as one process per
    thread and keep fewer copies of the model in memory."""
    procs = max(1, min(8, jobs // 4))
    return procs, max(1, jobs // procs)


def choose_backend(args) -> tuple:
    """(backend, model id, ONNX model folder or None, why the ONNX backend
    is not used, or None)."""
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    onnx_dir = args.onnx or (str(Path(args.model).parent / 'minilm-l6') if args.model else '')
    if args.backend != 'lembed':
        try:
            import embed_onnx
            why = embed_onnx.unavailable(onnx_dir)
            if why is None:
                return 'onnx', embed_onnx.model_id(args.max_tokens), onnx_dir, None
        except Exception as exc:  # an old Python, or a broken install
            why = str(exc)
        if args.backend == 'onnx':
            raise SystemExit(f'the ONNX backend cannot run: {why}')
        return 'lembed', MODEL_NAME, None, why
    return 'lembed', MODEL_NAME, None, None


def tree_models(index_root: Path) -> dict:
    """How many directories under an index hold vectors of each model."""
    counts: dict = {}
    for dirpath, dirnames, filenames in os.walk(index_root):
        dirnames.sort()
        if 'db.db' not in filenames:
            continue
        try:
            db = sqlite3.connect((Path(dirpath).resolve() / 'db.db').as_uri() + '?mode=ro', uri=True, timeout=30)
        except sqlite3.Error:
            continue
        try:
            names = {n for (n,) in db.execute("SELECT name FROM sqlite_master WHERE name IN ('gvec', 'gvecmodel')")}
            if 'gvec' in names:
                model = MODEL_NAME
                if 'gvecmodel' in names:
                    row = db.execute('SELECT model FROM gvecmodel LIMIT 1').fetchone()
                    model = row[0] if row and row[0] else MODEL_NAME
                counts[model] = counts.get(model, 0) + 1
        except sqlite3.Error:
            pass
        finally:
            db.close()
    return counts


def main() -> int:
    global MODEL_ID
    ap = argparse.ArgumentParser()
    ap.add_argument('--index', required=True, help='GUFI index tree top')
    ap.add_argument('--model', default='', help='GGUF embedding model path, for the lembed backend')
    ap.add_argument('--onnx', default='', help='folder with model.onnx and tokenizer.json (default: minilm-l6 beside --model)')
    ap.add_argument('--backend', choices=('auto', 'onnx', 'lembed'), default='auto',
                    help='auto: ONNX when its packages and model are here, else lembed')
    ap.add_argument('--max-tokens', type=int, choices=(128, 256), default=256,
                    help='ONNX backend: tokens of each passage embedded; 128 costs about 40 percent of the CPU of 256')
    ap.add_argument('--gufi-sqlite3',
                    default=os.path.join(os.environ.get('GUFI_BIN', '/opt/gufi/bin'), 'gufi_sqlite3'),
                    help='gufi_sqlite3 binary; defaults under $GUFI_BIN')
    ap.add_argument('--subdir', default='', help='restrict to this KB-relative subtree (incremental indexing)')
    ap.add_argument('--no-recurse', action='store_true', help='process only the subdir itself, not its children')
    ap.add_argument('--jobs', type=int, default=0, help='embedding processes side by side (default: the CPU count)')
    ap.add_argument('--cache', default='', help='embedding cache (SQLite), reused across runs')
    ap.add_argument('--prune', action='store_true', help='drop cache entries this index no longer uses (whole-index runs only)')
    ap.add_argument('--max-chunks', type=int, default=MAX_CHUNKS_PER_FILE, help=f'passages embedded per file, from its start (default {MAX_CHUNKS_PER_FILE})')
    ap.add_argument('--documents-only', action='store_true', help='embed documents and images only; code and data stay in full-text search')
    ap.add_argument('--progress', action='store_true', help='print @progress lines on stdout')
    args = ap.parse_args()

    jobs = args.jobs if args.jobs > 0 else default_jobs()
    args.max_chunks = max(1, args.max_chunks)
    index_root = Path(args.index)
    backend, MODEL_ID, onnx_dir, why = choose_backend(args)
    args.backend, args.onnx = backend, onnx_dir
    if backend == 'lembed' and not args.model:
        raise SystemExit(f'no embedding model: --model names none, and the ONNX backend cannot run ({why})')
    print(f'model: {MODEL_ID} ({backend}{", " + why if why and backend == "lembed" else ""})', flush=True)
    if args.subdir or args.no_recurse:
        # One folder of an index whose vectors come from another model would
        # leave the index answering in two models: do all of it now.
        others = {m: n for m, n in tree_models(index_root).items() if m != MODEL_ID}
        if others:
            print(f'the index holds vectors of {", ".join(sorted(others))}: embedding all of it with {MODEL_ID}', flush=True)
            args.subdir, args.no_recurse = '', False
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
                if not embeds_file(fname, args.documents_only):
                    continue
                for seq, c in enumerate(chunk(index_text(wordf or ''), args.max_chunks)):
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
    if n_pending and args.backend == 'onnx':
        progress.start('embed', n_pending + len(with_chunks))
        procs, threads = onnx_layout(jobs)
        # Each process's tokenizer and numpy would otherwise start a thread
        # per core of their own, on top of ONNX Runtime's.
        os.environ.update(TOKENIZERS_PARALLELISM='false', OMP_NUM_THREADS='1', OPENBLAS_NUM_THREADS='1')
        cache = open_cache(cache_path)
        try:
            with ProcessPoolExecutor(procs, mp_context=multiprocessing.get_context('spawn'),
                                     initializer=_onnx_init, initargs=(args.onnx, threads, args.max_tokens)) as pool:
                tasks = [pool.submit(_onnx_task, str(scratch / 'pending.db'), lo, min(lo + ONNX_TASK - 1, n_pending))
                         for lo in range(1, n_pending + 1, ONNX_TASK)]
                for task in as_completed(tasks):
                    rows = task.result()
                    cache.executemany('INSERT OR REPLACE INTO emb (h, v) VALUES (?, ?)', rows)
                    cache.commit()
                    progress.add(len(rows))
        except BrokenProcessPool as exc:
            raise SystemExit(f'the ONNX embedding processes stopped: {exc}')
        finally:
            cache.close()
    elif n_pending:
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

    def dir_lines(dbfile: Path) -> list:
        return [
            f'ATTACH {sql_str(str(dbfile))} AS d;',
            'PRAGMA d.synchronous=OFF;',
            'BEGIN;',
            'DROP TABLE IF EXISTS d.gvec;',
            'CREATE VIRTUAL TABLE d.gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);',
            'INSERT INTO d.gvec(cid, fp384) SELECT g.cid, e.v FROM d.gchunks g JOIN c.emb e ON e.h = g.h;',
            # The model a query has to embed with to search these vectors.
            'DROP TABLE IF EXISTS d.gvecmodel;',
            'CREATE TABLE d.gvecmodel (model TEXT);',
            f'INSERT INTO d.gvecmodel VALUES ({sql_str(MODEL_ID)});',
            'COMMIT;',
            'DETACH d;',
        ]

    def write_group(group: list) -> None:
        lines = [f'ATTACH {cache_db} AS c;']
        for dbfile in group:
            lines += dir_lines(dbfile)
        ok, err = sqlite3_run('\n'.join(lines))
        if not ok:
            # One directory at a time, to name the one that failed.
            for dbfile in group:
                one = '\n'.join([lines[0]] + dir_lines(dbfile))
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
    print(f'embedded: {n_pending} new chunks with {MODEL_ID}, {len(with_chunks)} directory tables written, {len(errors)} errors', flush=True)
    # A chunk the tokenizer cannot take is skipped, not a failed pass: the
    # rest of the directory is searchable by meaning, and all of it by text.
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
