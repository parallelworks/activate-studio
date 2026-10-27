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
grouping; k-means into 4 to 14 clusters. Each cluster is named for the words
that set it apart: the two or three terms of its members' passages (the text
the vectors were made from) with the highest TF-IDF against the whole map,
no two clusters named alike. A cluster without such terms is named for the
folders most of its files live in, with the parent folder where two names
would read the same. Each cluster is also linked to the two clusters nearest
it in the full embedding space (the cosine of their mean vectors). Writes,
into --out:

  overview.json      every cluster (label, folder name, terms, count,
                     centroid), the links between clusters, and a sample of
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
import math
import os
import random
import re
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
# How many clusters each cluster is linked to, by the cosine of their means.
LINKS_PER_CLUSTER = 2

# Naming clusters by their words: each file's passages give at most
# TEXT_CHARS characters and keep their FILE_TERMS commonest terms.
TEXT_CHARS = 20_000
FILE_TERMS = 30
# embed.py's passages: CHUNK_CHARS characters, one every CHUNK_STEP, cut
# wherever that falls.
CHUNK_CHARS = 1000
CHUNK_STEP = 880
TERM = re.compile(r'[a-z][a-z0-9]{2,23}')
STOPWORDS = frozenset('''
a about above after again against all also am an and any are as at be because been before being below between both
but by can cannot could did do does doing down during each else etc even ever every few for from further get gets got
had has have having he her here hers him his how however if in into is it its itself just like made make makes many
may me might more most much must my no nor not now of off on once one only or other our ours out over own per same
see shall she should since so some such than that the their theirs them then there these they this those though
through thus to too two under until up upon us use used uses using very via was way we well were what when where
whether which while who whom whose why will with within without would yet you your yours three four five first
second new old also within non way ways need needs based include includes including example examples following
given within page pages figure fig table tables section chapter file files http https www com org net html htm pdf
png jpg jpeg gif svg doc docx ppt pptx xls xlsx txt csv json xml yaml yml true false null none nan
'''.split())


def file_terms(text: str) -> list[list]:
    """The FILE_TERMS commonest terms of a file's text, with their counts:
    lower-case words of 3 to 24 letters and digits, starting with a letter,
    no stop words, and no runs of digits (versions, ids and hashes)."""
    counts: dict[str, int] = {}
    for t in TERM.findall(text.lower()):
        if t in STOPWORDS or sum(ch.isdigit() for ch in t) > 2:
            continue
        counts[t] = counts.get(t, 0) + 1
    return [[t, n] for t, n in sorted(counts.items(), key=lambda kv: (-kv[1], kv[0]))[:FILE_TERMS]]


def whole_words(parts: list[str]) -> str:
    """A file's passages, in order, as one text without the words their cuts
    split. A passage after the first can start inside a word (the "eef" of
    "reef") and a full-length one can end inside one. Where the passages on
    either side overlap it, as consecutive passages do, the characters around
    the cut say whether it split a word, which is then whole in the neighbor;
    otherwise a word at the cut is taken off."""
    over = CHUNK_CHARS - CHUNK_STEP
    out = []
    for k, t in enumerate(parts):
        start, end = 0, len(t)
        prev = parts[k - 1] if k else None
        nxt = parts[k + 1] if k + 1 < len(parts) else None
        if k and t[:1].isalnum():
            joined = prev is not None and len(prev) == CHUNK_CHARS and prev[CHUNK_STEP:] == t[:over]
            if not joined or prev[CHUNK_STEP - 1].isalnum():
                while start < end and t[start].isalnum():
                    start += 1
        if len(t) >= CHUNK_CHARS and t[-1:].isalnum():
            joined = nxt is not None and t[CHUNK_STEP:] == nxt[:over]
            if not joined or (len(nxt) > over and nxt[over].isalnum()):
                while end > start and t[end - 1].isalnum():
                    end -= 1
        out.append(t[start:end])
    return ' '.join(out)


def folder_vectors(db_path: str):
    """(file name, mean vector, model, terms) for each file with vectors in
    one folder's db.db, read without the sqlite-vec extension. The terms come
    from the file's passages (gchunks), the text its vectors were made from."""
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
        # Whole passages, about TEXT_CHARS characters of them, so the words
        # at their cuts can be told apart (whole_words).
        texts: dict[str, list[str]] = {}
        sizes: dict[str, int] = {}
        try:
            for fname, ctext in c.execute('SELECT fname, ctext FROM gchunks ORDER BY fname, seq'):
                if fname not in sums or not ctext or sizes.get(fname, 0) >= TEXT_CHARS:
                    continue
                texts.setdefault(fname, []).append(ctext)
                sizes[fname] = sizes.get(fname, 0) + len(ctext)
        except sqlite3.Error:
            texts = {}
        return [(f, (sums[f] / counts[f]).astype(np.float32), model, file_terms(whole_words(texts.get(f, [])))) for f in sums], model
    except (sqlite3.Error, ValueError, IndexError):
        return [], None
    finally:
        c.close()


def label_clusters(paths: list[str], assign: np.ndarray, k: int) -> list[str]:
    """Each cluster named for the folder most of its files live in; clusters
    that would share a name go a level deeper until they differ. A second
    folder keeps its parent when its own name is the same as the first's
    (pw/docs + gs_services/docs, not pw/docs + docs)."""
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
        first, second = ranked[0][0], ranked[1][0].split('/')
        tail = second[-1] if second[-1] != first.split('/')[-1] else '/'.join(second[-2:])
        return f'{first} + {tail}'

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


def cluster_terms(terms_path: Path, assign: np.ndarray, k: int, df: dict[str, int], n_text: int, keep: int = 8) -> list[list[str]]:
    """Each cluster's distinctive terms, best first: the share of its files
    (those with text) that use a term, times the term's inverse document
    frequency over every file of the map, so a word common to the whole map
    scores near nothing. A term must appear in at least 5% of the cluster's
    files with text (and in two, where the cluster has four or more)."""
    cdf: list[dict[str, int]] = [{} for _ in range(k)]
    with_text = [0] * k
    with terms_path.open() as f:
        for i, line in enumerate(f):
            terms = json.loads(line)
            if not terms:
                continue
            c = int(assign[i])
            with_text[c] += 1
            for t, _ in terms:
                cdf[c][t] = cdf[c].get(t, 0) + 1
    out: list[list[str]] = []
    for c in range(k):
        n_c = with_text[c]
        if not n_c:
            out.append([])
            continue
        support = max(2, math.ceil(0.05 * n_c)) if n_c >= 4 else 1
        scored = []
        for t, m in cdf[c].items():
            if m < support:
                continue
            idf = math.log((n_text + 1) / (df.get(t, 0) + 1))
            score = (m / n_c) * idf
            if score > 0.05:
                scored.append((score, t))
        scored.sort(key=lambda st: (-st[0], st[1]))
        out.append([t for _, t in scored[:keep]])
    return out


def name_clusters(candidates: list[list[str]], folders: list[str], max_chars: int = 26) -> tuple[list[str], list[list[str]]]:
    """A name from each cluster's terms, largest cluster first: two terms, or
    three where they fit in max_chars. A term another cluster's name already
    uses goes behind the terms no name uses yet, and a name another cluster
    has takes the next term instead of its last, so no two names read alike.
    A cluster with fewer than two terms takes its folder name, which says
    more than one word would."""
    names: list[str] = []
    chosen: list[list[str]] = []
    used_terms: set[str] = set()
    taken: set[str] = set()
    for c, cands in enumerate(candidates):
        if len(cands) < 2:
            name = folders[c] or f'cluster {c + 1}'
            n = 2
            while name in taken:
                name = f'{folders[c] or "cluster"} ({n})'
                n += 1
            names.append(name)
            chosen.append([])
            taken.add(name)
            continue
        ordered = [t for t in cands if t not in used_terms] + [t for t in cands if t in used_terms]
        pick = ordered[:2]
        if len(ordered) > 2 and len(', '.join(ordered[:3])) <= max_chars:
            pick = ordered[:3]
        name = ', '.join(pick)
        spare = [t for t in ordered if t not in pick]
        while name in taken and spare:
            pick = pick[:-1] + [spare.pop(0)]
            name = ', '.join(pick)
        if name in taken:
            name = f'{name} ({folders[c].split(" + ")[0].split("/")[-1] or c + 1})'
        names.append(name)
        chosen.append(pick)
        taken.add(name)
        used_terms.update(pick)
    return names, chosen


def cluster_links(means_path: Path, n: int, assign: np.ndarray, k: int, per: int = LINKS_PER_CLUSTER) -> list[dict]:
    """Each cluster linked to the `per` clusters nearest it in the full
    embedding space: the cosine of the clusters' mean vectors, read from the
    scratch file of file means a block at a time. Pairs are listed once."""
    if k < 2:
        return []
    sums = np.zeros((k, DIMS), dtype=np.float64)
    X = np.memmap(means_path, dtype=np.float32, mode='r', shape=(n, DIMS))
    for s in range(0, n, 20_000):
        np.add.at(sums, assign[s:s + 20_000], np.asarray(X[s:s + 20_000], dtype=np.float64))
    del X
    norms = np.linalg.norm(sums, axis=1, keepdims=True)
    cents = sums / np.where(norms > 0, norms, 1)
    sim = cents @ cents.T
    pairs: dict[tuple[int, int], float] = {}
    for a in range(k):
        others = [b for b in np.argsort(-sim[a], kind='stable') if b != a][:per]
        for b in others:
            key = (min(a, int(b)), max(a, int(b)))
            pairs[key] = round(float(sim[a, b]), 4)
    return [{'a': a, 'b': b, 'sim': s} for (a, b), s in sorted(pairs.items(), key=lambda kv: (-kv[1], kv[0]))]


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
        # Each file's terms, one JSON line per file in the order of `paths`,
        # and how many files use each term, for naming the clusters.
        terms_path = scratch / 'terms.jsonl'
        df: dict[str, int] = {}
        n_text = 0
        paths: list[str] = []
        models: dict[str, int] = {}
        dbs = 0
        with means_path.open('wb') as means, terms_path.open('w') as terms_out:
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
                for fname, vec, m, terms in rows:
                    paths.append(fname if rel == '.' else f'{rel}/{fname}')
                    means.write(vec.tobytes())
                    terms_out.write(json.dumps(terms, separators=(',', ':')) + '\n')
                    if terms:
                        n_text += 1
                        for t, _ in terms:
                            df[t] = df.get(t, 0) + 1
                # A term seen in one file only cannot name a cluster; on a very
                # large map those are let go so the tally stays bounded.
                if len(df) > 1_500_000:
                    df = {t: v for t, v in df.items() if v > 1}
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
        # Fewer files than GROUP_DIMS span fewer directions; keep those.
        comps = vt[:GROUP_DIMS].T
        proj = np.empty((n, comps.shape[1]), dtype=np.float32)
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
        folders = label_clusters(paths, assign, k)
        labels, label_terms = name_clusters(cluster_terms(terms_path, assign, k, df, n_text), folders)
        links = cluster_links(means_path, n, assign, k)
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
            clusters.append({'c': c, 'label': labels[c], 'folder': folders[c], 'terms': label_terms[c], 'count': int(len(m)),
                             'x': fix(centre[0]), 'y': fix(centre[1]), 'z': fix(centre[2])})
            for i in m[:quotas[c]]:
                points.append({'p': paths[i], 'x': fix(coords[i, 0]), 'y': fix(coords[i, 1]), 'z': fix(coords[i, 2]), 'c': c})
            with (scratch / f'cluster-{c}.json').open('w') as f:
                json.dump({'c': c, 'label': labels[c], 'count': int(len(m)),
                           'points': [{'p': paths[i], 'x': fix(coords[i, 0]), 'y': fix(coords[i, 1]), 'z': fix(coords[i, 2])} for i in m]},
                          f, separators=(',', ':'))
        overview = {
            'version': 3, 'available': True, 'builtAt': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
            'model': model, 'files': n, 'shown': len(points), 'clusters': clusters, 'links': links, 'points': points,
            'ms': round((time.monotonic() - t0) * 1000), 'readMs': read_ms, 'dbs': dbs,
        }
        with (scratch / 'overview.json').open('w') as f:
            json.dump(overview, f, separators=(',', ':'))
        means_path.unlink()
        terms_path.unlink()
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
