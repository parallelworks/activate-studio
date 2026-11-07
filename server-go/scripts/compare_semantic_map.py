#!/usr/bin/env python3
"""Compare the Go semantic map (server-go/cmd/semantic-map) with
indexer/semantic_map.py on the same synthetic inputs.

Usage:
  PYTHONPATH=indexer/.deps python3 server-go/scripts/compare_semantic_map.py \
      --go <semantic-map binary> [--work <dir>] [--large 50000]

Each case writes an index the way the Studio stores vectors (sqlite-vec's
gvec_rowids, gvec_vector_chunks00 and gvec_chunks validity bits, with the
passages in gchunks), or a vectors.jsonl as server/src/remoteMap.ts fetches
it, runs both programs, and reports:

  files, clusters      must be equal
  ARI                  adjusted Rand index of the two assignments
  ARI python seeds     the same measure between two Python runs whose only
                       difference is the random seed: the spread k-means
                       itself has on that input
  names, links         compared after matching each Go cluster to the Python
                       cluster it shares most files with
  sample               the overview's sample follows the rule (at most
                       --sample files, at least min(25, size) per cluster)
  axes                 the absolute correlation of each map axis (PCA
                       components agree up to sign)
  schema               the same keys in overview.json and cluster-<c>.json
  time                 wall time of each program

The random seeds of the two programs differ, so "equivalent" means: equal
file and cluster counts, the schema identical, the sample rule kept, and
where k-means is stable (ARI between Python seeds is 1) an ARI of 1 with
identical names and links; where it is not, an ARI within the spread of
Python's own seeds.
"""
from __future__ import annotations

import argparse
import itertools
import json
import math
import os
import random
import shutil
import sqlite3
import subprocess
import sys
import time
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parents[2]
PY_SCRIPT = ROOT / 'indexer' / 'semantic_map.py'
D = 384
CHUNK_CHARS, CHUNK_STEP = 1000, 880

COMMON = ('the system data report results using model analysis project version notes value time based '
          'process design review summary method approach include work team').split()


def topic_vocab(rng: random.Random, n_topics: int) -> list[list[str]]:
    letters = 'abcdefghijklmnopqrstuvwxyz'
    seen, out = set(), []
    for _ in range(n_topics):
        words = []
        while len(words) < 10:
            w = ''.join(rng.choice(letters) for _ in range(rng.randint(5, 9)))
            if w not in seen:
                seen.add(w)
                words.append(w)
        out.append(words)
    return out


def doc_text(rng: random.Random, vocab: list[str], length: int) -> str:
    words, size = [], 0
    while size < length:
        w = rng.choice(vocab) if rng.random() < 0.35 else rng.choice(COMMON)
        words.append(w)
        size += len(w) + 1
    return ' '.join(words)


def passages(text: str) -> list[str]:
    out, i = [], 0
    while True:
        out.append(text[i:i + CHUNK_CHARS])
        if i + CHUNK_CHARS >= len(text):
            return out
        i += CHUNK_STEP


def make_files(seed: int, topics: int, per_topic: int, spread: float, subtopics: int = 1):
    """(folder, name, passage vectors, passage texts) per file: each topic a
    direction, each file's passages that direction plus noise."""
    rng = random.Random(seed)
    nrng = np.random.default_rng(seed)
    vocab = topic_vocab(rng, topics)
    axes = nrng.normal(size=(topics, subtopics, D))
    axes /= np.linalg.norm(axes, axis=2, keepdims=True)
    files = []
    for t in range(topics):
        for f in range(per_topic):
            sub = f % subtopics
            folder = f'area{t % 4}/topic{t}' + (f'/part{sub}' if subtopics > 1 else '')
            text = doc_text(rng, vocab[t], rng.randint(300, 2600))
            parts = passages(text)
            vecs = axes[t, sub] + nrng.normal(scale=spread, size=(len(parts), D))
            vecs /= np.linalg.norm(vecs, axis=1, keepdims=True)
            files.append((folder, f'doc{f}.md', vecs.astype(np.float32), parts, t))
    return files


def write_index(root: Path, files, invalid_every: int = 7) -> None:
    """One db.db per folder, vectors in chunks of 1024 slots; every
    invalid_every-th extra slot holds a passage marked invalid, which must
    not count."""
    by_folder: dict[str, list] = {}
    for folder, name, vecs, parts, _ in files:
        by_folder.setdefault(folder, []).append((name, vecs, parts))
    shutil.rmtree(root, ignore_errors=True)
    root.mkdir(parents=True)
    sqlite3.connect(root / 'db.db').execute('CREATE TABLE entries (name TEXT)').connection.commit()
    for folder, items in by_folder.items():
        d = root / folder
        d.mkdir(parents=True, exist_ok=True)
        c = sqlite3.connect(d / 'db.db')
        c.executescript('''
          CREATE TABLE entries (name TEXT);
          CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT);
          CREATE TABLE gvec_rowids (rowid INTEGER PRIMARY KEY AUTOINCREMENT, id, chunk_id INTEGER, chunk_offset INTEGER);
          CREATE TABLE gvec_chunks (chunk_id INTEGER PRIMARY KEY AUTOINCREMENT, size INTEGER NOT NULL, validity BLOB NOT NULL, rowids BLOB NOT NULL);
          CREATE TABLE gvec_vector_chunks00 (rowid PRIMARY KEY, vectors BLOB NOT NULL);
          CREATE TABLE gvecmodel (model TEXT);
          INSERT INTO gvecmodel VALUES ('minilm-l6-mean-int8');
        ''')
        slots: list[np.ndarray] = []
        valid: list[bool] = []
        cid = 0
        for fi, (name, vecs, parts) in enumerate(items):
            for seq, (v, t) in enumerate(zip(vecs, parts)):
                cid += 1
                slot = len(slots)
                c.execute('INSERT INTO gchunks VALUES (?, ?, ?, ?, ?, ?)', (cid, '1', name, seq, t, f'h{cid}'))
                c.execute('INSERT INTO gvec_rowids (rowid, chunk_id, chunk_offset) VALUES (?, ?, ?)', (cid, slot // 1024 + 1, slot % 1024))
                slots.append(v)
                valid.append(True)
            if fi % invalid_every == 0:
                cid += 1
                slot = len(slots)
                c.execute('INSERT INTO gchunks VALUES (?, ?, ?, ?, ?, ?)', (cid, '1', f'deleted{fi}.md', 0, 'gone', 'hx'))
                c.execute('INSERT INTO gvec_rowids (rowid, chunk_id, chunk_offset) VALUES (?, ?, ?)', (cid, slot // 1024 + 1, slot % 1024))
                slots.append(np.ones(D, dtype=np.float32))
                valid.append(False)
        for chunk in range(0, len(slots), 1024):
            block = np.zeros((1024, D), dtype='<f4')
            part = slots[chunk:chunk + 1024]
            block[:len(part)] = np.array(part)
            bits = bytearray(128)
            for i, ok in enumerate(valid[chunk:chunk + 1024]):
                if ok:
                    bits[i >> 3] |= 1 << (i & 7)
            c.execute('INSERT INTO gvec_chunks VALUES (?, 1024, ?, ?)', (chunk // 1024 + 1, bytes(bits), b''))
            c.execute('INSERT INTO gvec_vector_chunks00 VALUES (?, ?)', (chunk // 1024 + 1, block.tobytes()))
        c.commit()
        c.close()


def write_vectors(path: Path, files, text_every: int = 2) -> None:
    """vectors.jsonl as remoteMap.ts writes it: the mean, its length, the
    mean normalized and quantized by sqlite-vec's 'unit' int8 scale, and for
    some files the first 1,000 characters of their first passage."""
    with path.open('w') as f:
        for i, (folder, name, vecs, parts, _) in enumerate(files):
            mean = vecs.astype(np.float64).mean(0)
            r = float(np.linalg.norm(mean))
            u = mean / r
            q = np.clip(np.trunc((u + 1) * 127.5 - 128), -128, 127).astype(np.int8)
            row = {'p': f'{folder}/{name}', 'r': r, 'v': q.tobytes().hex()}
            if i % text_every == 0:
                row['t'] = parts[0][:1000]
            f.write(json.dumps(row) + '\n')


def run_python(args: list[str], seed: int) -> float:
    """The Python script with its rng seeded `seed` (it uses 0)."""
    code = (
        'import sys, runpy, numpy as np\n'
        f'_r = np.random.default_rng\n'
        f'np.random.default_rng = lambda s=None: _r({seed})\n'
        f'sys.argv = [{str(PY_SCRIPT)!r}] + {args!r}\n'
        f'sys.path.insert(0, {str(PY_SCRIPT.parent)!r})\n'
        f'runpy.run_path({str(PY_SCRIPT)!r}, run_name="__main__")\n'
    )
    t = time.monotonic()
    subprocess.run([sys.executable, '-c', code], check=True, stdout=subprocess.DEVNULL)
    return time.monotonic() - t


def run_go(binary: str, args: list[str]) -> float:
    t = time.monotonic()
    subprocess.run([binary, *args], check=True, stdout=subprocess.DEVNULL)
    return time.monotonic() - t


def load(out: Path):
    ov = json.loads((out / 'overview.json').read_text())
    assign, coords, clusters = {}, {}, {}
    for c in ov['clusters']:
        cl = json.loads((out / f'cluster-{c["c"]}.json').read_text())
        clusters[c['c']] = cl
        for p in cl['points']:
            assign[p['p']] = c['c']
            coords[p['p']] = (p['x'], p['y'], p['z'])
    return ov, assign, coords, clusters


def ari(a: dict, b: dict) -> float:
    keys = sorted(a)
    x = [a[k] for k in keys]
    y = [b[k] for k in keys]
    n = len(keys)
    table: dict[tuple, int] = {}
    for i, j in zip(x, y):
        table[(i, j)] = table.get((i, j), 0) + 1
    c2 = lambda m: m * (m - 1) / 2
    sum_ij = sum(c2(v) for v in table.values())
    rows: dict = {}
    cols: dict = {}
    for (i, j), v in table.items():
        rows[i] = rows.get(i, 0) + v
        cols[j] = cols.get(j, 0) + v
    sa = sum(c2(v) for v in rows.values())
    sb = sum(c2(v) for v in cols.values())
    expected = sa * sb / c2(n)
    top = (sa + sb) / 2
    return 1.0 if top == expected else (sum_ij - expected) / (top - expected)


def match(go_assign: dict, py_assign: dict) -> dict:
    """Each Go cluster to the Python cluster it shares most files with."""
    overlap: dict = {}
    for p, g in go_assign.items():
        key = (g, py_assign[p])
        overlap[key] = overlap.get(key, 0) + 1
    best: dict = {}
    for (g, q), v in sorted(overlap.items(), key=lambda kv: -kv[1]):
        if g not in best:
            best[g] = q
    return best


def sample_ok(ov: dict, sample: int) -> bool:
    per: dict = {}
    for p in ov['points']:
        per[p['c']] = per.get(p['c'], 0) + 1
    if len(ov['points']) > max(sample, 25 * len(ov['clusters'])):
        return False
    return all(per.get(c['c'], 0) >= min(25, c['count']) for c in ov['clusters'])


def axis_corr(a: dict, b: dict) -> list[float]:
    keys = sorted(a)
    out = []
    for i in range(3):
        x = np.array([a[k][i] for k in keys])
        y = np.array([b[k][i] for k in keys])
        if x.std() == 0 or y.std() == 0:
            out.append(float('nan'))
        else:
            out.append(round(abs(float(np.corrcoef(x, y)[0, 1])), 4))
    return out


def keyset(ov: dict) -> tuple:
    return (tuple(ov.keys()), tuple(ov['clusters'][0].keys()), tuple(ov['points'][0].keys()), tuple(ov['links'][0].keys()) if ov['links'] else ())


def compare(name: str, src_args: list[str], work: Path, binary: str, sample: int = 3000) -> dict:
    outs = {k: work / f'{name}-{k}' for k in ('py0', 'py1', 'go')}
    for o in outs.values():
        shutil.rmtree(o, ignore_errors=True)
    t_py = run_python(src_args + ['--out', str(outs['py0'])], 0)
    run_python(src_args + ['--out', str(outs['py1'])], 1)
    t_go = run_go(binary, src_args + ['--out', str(outs['go'])])
    py, py_a, py_xyz, _ = load(outs['py0'])
    py1, py1_a, _, _ = load(outs['py1'])
    go, go_a, go_xyz, _ = load(outs['go'])
    m = match(go_a, py_a)
    py_names = {c['c']: c['label'] for c in py['clusters']}
    go_names = {c['c']: c['label'] for c in go['clusters']}
    names_same = sum(1 for g, q in m.items() if go_names[g] == py_names[q])
    py_links = {(min(l['a'], l['b']), max(l['a'], l['b'])) for l in py['links']}
    go_links = {tuple(sorted((m[l['a']], m[l['b']]))) for l in go['links']}
    with open(work / f'{name}-names.json', 'w') as f:
        json.dump({'python': [c['label'] for c in py['clusters']], 'go': [c['label'] for c in go['clusters']]}, f, indent=1)
    return {
        'case': name,
        'files': (py['files'], go['files']),
        'clusters': (len(py['clusters']), len(go['clusters'])),
        'ari_go_py': round(ari(go_a, py_a), 4),
        'ari_py_seeds': round(ari(py1_a, py_a), 4),
        'names_same': f'{names_same}/{len(go_names)}',
        'links_same': f'{len(go_links & py_links)}/{len(py_links)}',
        'sample': (py['shown'], go['shown'], sample_ok(py, sample), sample_ok(go, sample)),
        'axes_abs_corr': axis_corr(go_xyz, py_xyz),
        'schema_same': keyset(py) == keyset(go),
        'of': (py.get('of'), go.get('of')),
        'time_s': (round(t_py, 2), round(t_go, 2)),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--go', required=True, help='the semantic-map binary')
    ap.add_argument('--work', default=None)
    ap.add_argument('--large', type=int, default=50_000, help='files in the timing case (0 to skip)')
    args = ap.parse_args()
    work = Path(args.work or (Path.cwd() / 'semantic-map-compare'))
    work.mkdir(parents=True, exist_ok=True)
    results = []

    # A: separated topics, as many as k-means makes clusters (n = 200, k = 10).
    files = make_files(1, 10, 20, 0.02)
    write_index(work / 'a-index', files)
    results.append(compare('separated', ['--index', str(work / 'a-index')], work, args.go))

    # B: 6 topics of 3 subtopics each, more noise, k = 14 > topics.
    files = make_files(2, 6, 400, 0.05, subtopics=3)
    write_index(work / 'b-index', files)
    results.append(compare('overlapping', ['--index', str(work / 'b-index')], work, args.go))

    # C: the remote path, vectors.jsonl with int8 vectors, text on half; 14 topics, k = 14.
    files = make_files(3, 14, 400, 0.03)
    write_vectors(work / 'c-vectors.jsonl', files)
    results.append(compare('remote', ['--vectors', str(work / 'c-vectors.jsonl'), '--model', 'minilm-l6-mean-int8', '--of', '20000'], work, args.go))

    # D: timing, past the 20,000-file fit (the fit rows are a random sample).
    if args.large:
        per = args.large // 14
        files = make_files(4, 14, per, 0.03)
        write_index(work / 'd-index', files)
        results.append(compare('large', ['--index', str(work / 'd-index')], work, args.go))

    for r in results:
        print(json.dumps(r))
    return 0


if __name__ == '__main__':
    sys.exit(main())
