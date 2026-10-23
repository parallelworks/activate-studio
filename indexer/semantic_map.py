#!/usr/bin/env python3
"""The semantic map of an index: every file with embeddings placed by what
it says, for the Stats page.

Reads each folder's passage vectors straight from sqlite-vec's storage in
its db.db (gvec_rowids says which chunk and offset hold a passage,
gvec_vector_chunks00 holds the float32 vectors, gvec_chunks marks the
valid slots), so nothing is dumped as text and memory stays bounded: one
mean vector per file, kept in a scratch file rather than in memory.

Then: the mean of each file's passages; PCA (an SVD of at most --fit files,
then every file projected) to three components for the map and sixteen for
grouping; k-means into 4 to 14 clusters, each named for the folder most of
its files live in. Writes, into --out:

  overview.json      every cluster (label, count, centroid) and a sample of
                     at most --sample files spread across the clusters
  cluster-<c>.json   every file of cluster c, in a random order, so its
                     first page is a fair sample of it

Coordinates are in [0, 1] on each axis. The result is written to a scratch
folder and swapped in whole, so a reader sees the old map or the new one.

Exits 3 when numpy is missing, which the server takes as its cue to draw a
smaller map its own way.
"""
from __future__ import annotations

import argparse
import json
import os
import random
import shutil
import sqlite3
import sys
import tempfile
import time
from pathlib import Path

DEPS = Path(__file__).resolve().parent / '.deps'
if DEPS.is_dir() and str(DEPS) not in sys.path:
    sys.path.insert(0, str(DEPS))
try:
    import numpy as np
except ImportError:
    print('numpy is not installed', file=sys.stderr)
    sys.exit(3)

DIMS = 384
GROUP_DIMS = 16
VECTOR_BYTES = DIMS * 4


def folder_vectors(db_path: str):
    """(file name, mean vector, model) for each file with vectors in one
    folder's db.db, read without the sqlite-vec extension."""
    try:
        c = sqlite3.connect(f'file:{db_path}?mode=ro', uri=True, timeout=30)
    except sqlite3.Error:
        return [], None
    try:
        names = {r[0] for r in c.execute("SELECT name FROM sqlite_master WHERE type = 'table'")}
        if not {'gchunks', 'gvec_rowids', 'gvec_vector_chunks00'} <= names:
            return [], None
        model = 'minilm384'
        if 'gvecmodel' in names:
            row = c.execute('SELECT model FROM gvecmodel LIMIT 1').fetchone()
            if row and row[0]:
                model = row[0]
        valid = {}
        if 'gvec_chunks' in names:
            for chunk_id, validity in c.execute('SELECT chunk_id, validity FROM gvec_chunks'):
                valid[chunk_id] = validity
        blobs = {chunk_id: blob for chunk_id, blob in c.execute('SELECT rowid, vectors FROM gvec_vector_chunks00')}
        fname_of = dict(c.execute('SELECT cid, fname FROM gchunks'))
        sums: dict[str, np.ndarray] = {}
        counts: dict[str, int] = {}
        for rowid, chunk_id, offset in c.execute('SELECT rowid, chunk_id, chunk_offset FROM gvec_rowids'):
            fname = fname_of.get(rowid)
            blob = blobs.get(chunk_id)
            if fname is None or blob is None or offset is None:
                continue
            bits = valid.get(chunk_id)
            if bits is not None and not (bits[offset >> 3] >> (offset & 7)) & 1:
                continue
            start = offset * VECTOR_BYTES
            vec = np.frombuffer(blob, dtype='<f4', count=DIMS, offset=start)
            if fname in sums:
                sums[fname] += vec
                counts[fname] += 1
            else:
                sums[fname] = vec.astype(np.float64)
                counts[fname] = 1
        return [(f, (sums[f] / counts[f]).astype(np.float32), model) for f in sums], model
    except (sqlite3.Error, ValueError, IndexError):
        return [], None
    finally:
        c.close()


def label_clusters(paths: list[str], assign: np.ndarray, k: int) -> list[str]:
    """Each cluster named for the folder most of its files live in; clusters
    that would share a name go a level deeper until they differ."""
    def label_at(c: int, depth: int) -> str:
        tally: dict[str, int] = {}
        for i in np.flatnonzero(assign == c):
            parts = paths[i].split('/')
            seg = '/'.join(parts[:depth + 1]) if len(parts) > depth + 1 else ('/'.join(parts[:-1]) if len(parts) > 1 else '(root)')
            tally[seg] = tally.get(seg, 0) + 1
        if not tally:
            return ''
        ranked = sorted(tally.items(), key=lambda kv: -kv[1])
        total = sum(tally.values())
        if ranked[0][1] / total >= 0.6 or len(ranked) == 1:
            return ranked[0][0]
        return f"{ranked[0][0]} + {ranked[1][0].split('/')[-1]}"

    labels = [label_at(c, 0) for c in range(k)]
    for depth in range(1, 4):
        seen: dict[str, int] = {}
        for l in labels:
            seen[l] = seen.get(l, 0) + 1
        collided = False
        for c in range(k):
            if seen.get(labels[c], 0) > 1:
                labels[c] = label_at(c, depth)
                collided = True
        if not collided:
            break
    return labels


def kmeans(points: np.ndarray, k: int, rng: np.random.Generator, iters: int = 30) -> np.ndarray:
    """k-means++ starts, then Lloyd's steps; returns the centroids."""
    n = len(points)
    cents = [points[rng.integers(n)]]
    d2 = ((points - cents[0]) ** 2).sum(1)
    for _ in range(1, k):
        p = d2 / d2.sum() if d2.sum() > 0 else None
        cents.append(points[rng.choice(n, p=p)])
        d2 = np.minimum(d2, ((points - cents[-1]) ** 2).sum(1))
    cents = np.array(cents)
    for _ in range(iters):
        assign = nearest(points, cents)
        moved = np.array([points[assign == c].mean(0) if (assign == c).any() else cents[c] for c in range(k)])
        if np.allclose(moved, cents):
            break
        cents = moved
    return cents


def nearest(points: np.ndarray, cents: np.ndarray) -> np.ndarray:
    out = np.empty(len(points), dtype=np.int32)
    for s in range(0, len(points), 50_000):
        block = points[s:s + 50_000]
        out[s:s + 50_000] = ((block[:, None, :] - cents[None, :, :]) ** 2).sum(2).argmin(1)
    return out


def unit(values: np.ndarray) -> np.ndarray:
    """Each axis to [0, 1], from its 0.5th to 99.5th percentile, so a few far
    files do not squeeze the rest into a corner."""
    lo = np.percentile(values, 0.5, axis=0)
    hi = np.percentile(values, 99.5, axis=0)
    span = np.where(hi - lo > 0, hi - lo, 1)
    return np.clip((values - lo) / span, 0, 1)


def build(index: Path, out: Path, sample: int, fit: int) -> dict:
    t0 = time.monotonic()
    rng = np.random.default_rng(0)
    scratch = Path(tempfile.mkdtemp(prefix='semantic-map-', dir=str(out.parent)))
    try:
        means_path = scratch / 'means.f32'
        paths: list[str] = []
        models: dict[str, int] = {}
        dbs = 0
        with means_path.open('wb') as means:
            for dirpath, dirnames, filenames in os.walk(index):
                dirnames[:] = sorted(d for d in dirnames if not d.startswith('.'))
                if 'db.db' not in filenames:
                    continue
                dbs += 1
                rows, model = folder_vectors(os.path.join(dirpath, 'db.db'))
                if not rows:
                    continue
                models[model] = models.get(model, 0) + len(rows)
                rel = os.path.relpath(dirpath, index)
                for fname, vec, m in rows:
                    paths.append(fname if rel == '.' else f'{rel}/{fname}')
                    means.write(vec.tobytes())
        read_ms = round((time.monotonic() - t0) * 1000)
        n = len(paths)
        if n < 3:
            shutil.rmtree(scratch, ignore_errors=True)
            return {'available': False, 'files': n, 'dbs': dbs}
        # An index holds one model; should a re-embedding be part way, the
        # files on the model most of them use are mapped and the rest left out.
        model = max(models, key=models.get)
        X = np.memmap(means_path, dtype=np.float32, mode='r', shape=(n, DIMS))
        fit_rows = np.sort(rng.choice(n, size=min(n, fit), replace=False))
        Xf = np.asarray(X[fit_rows], dtype=np.float64)
        mean = Xf.mean(0)
        _, _, vt = np.linalg.svd(Xf - mean, full_matrices=False)
        comps = vt[:GROUP_DIMS].T
        proj = np.empty((n, GROUP_DIMS), dtype=np.float32)
        for s in range(0, n, 20_000):
            proj[s:s + 20_000] = (np.asarray(X[s:s + 20_000], dtype=np.float64) - mean) @ comps
        del X
        coords = unit(proj[:, :3])
        k = max(4, min(14, round((n / 2) ** 0.5)))
        cents = kmeans(proj[fit_rows], k, rng)
        assign = nearest(proj, cents)
        # Clusters numbered by size, largest first, so colors and the legend
        # follow from one build to the next.
        sizes = np.bincount(assign, minlength=k)
        order = np.argsort(-sizes, kind='stable')
        rank = np.empty(k, dtype=np.int32)
        rank[order] = np.arange(k)
        assign = rank[assign]
        k = int((sizes > 0).sum())
        labels = label_clusters(paths, assign, k)
        fix = lambda v: round(float(v), 4)
        clusters, quota_total = [], 0
        members = [np.flatnonzero(assign == c) for c in range(k)]
        # The sample: each cluster's share of --sample, at least 25 files
        # (or all it has), so small clusters still show.
        quotas = [min(len(m), max(25, round(sample * len(m) / n))) for m in members]
        while sum(quotas) > sample and max(quotas) > 25:
            big = int(np.argmax(quotas))
            quotas[big] -= 1
        points = []
        for c in range(k):
            m = members[c].copy()
            rng.shuffle(m)
            centre = coords[m].mean(0)
            clusters.append({'c': c, 'label': labels[c], 'count': int(len(m)),
                             'x': fix(centre[0]), 'y': fix(centre[1]), 'z': fix(centre[2])})
            for i in m[:quotas[c]]:
                points.append({'p': paths[i], 'x': fix(coords[i, 0]), 'y': fix(coords[i, 1]), 'z': fix(coords[i, 2]), 'c': c})
            with (scratch / f'cluster-{c}.json').open('w') as f:
                json.dump({'c': c, 'label': labels[c], 'count': int(len(m)),
                           'points': [{'p': paths[i], 'x': fix(coords[i, 0]), 'y': fix(coords[i, 1]), 'z': fix(coords[i, 2])} for i in m]},
                          f, separators=(',', ':'))
        overview = {
            'version': 2, 'available': True, 'builtAt': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
            'model': model, 'files': n, 'shown': len(points), 'clusters': clusters, 'points': points,
            'ms': round((time.monotonic() - t0) * 1000), 'readMs': read_ms, 'dbs': dbs,
        }
        with (scratch / 'overview.json').open('w') as f:
            json.dump(overview, f, separators=(',', ':'))
        means_path.unlink()
        # Swapped in whole: the old map stays readable until the new one is complete.
        old = out.with_name(out.name + '.old')
        shutil.rmtree(old, ignore_errors=True)
        if out.exists():
            out.rename(old)
        scratch.rename(out)
        shutil.rmtree(old, ignore_errors=True)
        return {k2: overview[k2] for k2 in ('available', 'files', 'shown', 'ms', 'readMs', 'dbs', 'model')} | {'clusters': k}
    except BaseException:
        shutil.rmtree(scratch, ignore_errors=True)
        raise


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    ap.add_argument('--index', required=True, help='the GUFI index tree')
    ap.add_argument('--out', required=True, help='the map folder to write (replaced whole)')
    ap.add_argument('--sample', type=int, default=3000, help='files in the overview (default 3000)')
    ap.add_argument('--fit', type=int, default=20000, help='files the projection is fitted on (default 20000)')
    args = ap.parse_args()
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    print(json.dumps(build(Path(args.index), out, max(100, args.sample), max(1000, args.fit))))
    return 0


if __name__ == '__main__':
    sys.exit(main())
