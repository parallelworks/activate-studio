#!/usr/bin/env python3
"""The parts of mac-upgrade-e2e.sh that are not shell: the folder indexed, waiting for
the libraries, the battery of requests recorded before and after an update, and
the comparison. Standard library only, so it runs on a stock macOS runner.

  mac-upgrade-e2e.py corpus DIR
  mac-upgrade-e2e.py wait API SECONDS
  mac-upgrade-e2e.py battery API OUT.json
  mac-upgrade-e2e.py stamps DATA OUT.json
  mac-upgrade-e2e.py same-stamps BEFORE.json AFTER.json
  mac-upgrade-e2e.py compare BEFORE.json AFTER.json
  mac-upgrade-e2e.py format DATA
"""
import glob
import json
import os
import random
import re
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

LIBS = ['e2e-names', 'e2e-text', 'e2e-meaning']
TOPICS = {
    'cfd': 'airfoil turbulence boundary layer solver convergence mesh refinement',
    'finance': 'quarterly budget forecast invoice revenue expenses contract',
    'ops': 'cluster scheduler partition quota storage network latency outage',
    'weather': 'ocean forecast ensemble satellite radar precipitation model',
}


def corpus(dest):
    random.seed(7)
    if os.path.isdir(dest):
        return
    for i in range(150):
        topic = random.choice(list(TOPICS))
        d = os.path.join(dest, topic, f'group{i % 8:02d}')
        os.makedirs(d, exist_ok=True)
        base = TOPICS[topic].split()
        body = ' '.join(random.choice(base) for _ in range(60))
        with open(os.path.join(d, f'{topic}-note-{i:04d}.txt'), 'w', encoding='utf-8') as f:
            f.write(f'# {topic} note {i}\n{body}.\n')
    marks = {
        'cfd/group00/marker-alpha.md': '# Marker\nZEBRAFISH-4471 appears only here.\n',
        'Name With Spaces/a file with spaces.txt': 'spaced folder note about ocean forecast ensemble SPACEMARK-3310\n',
        'café/résumé-élan.txt': 'non ascii folder note, boundary layer, UNIMARK-5521\n',
        '日本語/メモ.txt': 'japanese folder note about storage cluster, JPMARK-7718\n',
        "it's quoted/o\"brien & co.txt": 'quote folder note, QUOTEMARK-1189\n',
        'README.md': '# Top\nA top level readme about solver mesh.\n',
    }
    deep = dest
    for k in range(1, 7):
        deep = os.path.join(deep, f'level{k}-' + 'x' * 28)
    marks[os.path.relpath(os.path.join(deep, 'deep-file-' + 'y' * 40 + '.txt'), dest)] = 'a very deep long path note, DEEPMARK-6604\n'
    for rel, text in marks.items():
        p = os.path.join(dest, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, 'w', encoding='utf-8') as f:
            f.write(text)
    print('files:', sum(len(f) for _, _, f in os.walk(dest)))


def call(api, path, body=None, timeout=120):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(api + path, data, {'content-type': 'application/json'} if data else {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read() or b'null')
    except urllib.error.HTTPError as e:
        return {'__http': e.code, 'body': e.read().decode()[:300]}
    except Exception as e:  # noqa: BLE001
        return {'__err': str(e)[:200]}


def libraries(api):
    r = call(api, '/api/libraries')
    return r.get('libraries', []) if isinstance(r, dict) else []


def wait(api, seconds):
    t0 = time.time()
    while time.time() - t0 < seconds:
        libs = {l['id']: l for l in libraries(api)}
        busy = [i for i in LIBS if (libs.get(i, {}).get('build') or {}).get('state') not in ('ready',)]
        if not busy and all(i in libs for i in LIBS):
            return 0
        time.sleep(3)
    print('still building or missing:', busy)
    return 1


VOLATILE = {'elapsedMs', 'gufi', 'ms', 'builtAt', 'at', 'probedAt', 'readMs', 'startedAt', 'takenAt', 'progress', 'formatNote'}


def norm(x):
    if isinstance(x, dict):
        return {k: norm(v) for k, v in x.items() if k not in VOLATILE}
    if isinstance(x, list):
        return [norm(v) for v in x]
    return x


def hits(r):
    if not isinstance(r, dict) or 'hits' not in r:
        return norm(r)
    return {'hits': sorted([(h.get('library', {}).get('id'), h.get('path')) for h in r['hits']], key=str), 'omitted': r.get('omitted')}


def battery(api, out):
    res = {}
    for lib in LIBS:
        q = urllib.parse.quote
        s = {}
        for name, term in [('name', 'marker-alpha'), ('word', 'ZEBRAFISH-4471'), ('spaces', 'SPACEMARK-3310'), ('nonascii', 'UNIMARK-5521'),
                           ('japanese', 'JPMARK-7718'), ('deep', 'DEEPMARK-6604'), ('words', 'turbulence boundary layer'),
                           ('meaning', 'weather ocean forecast ensemble')]:
            s['search:' + name] = hits(call(api, f'/api/search?q={q(term)}&limit=200&library={lib}'))
        s['stats'] = norm(call(api, f'/api/kb/stats?library={lib}'))
        s['tree'] = norm(call(api, f'/api/kb/tree?library={lib}&path='))
        for canned in ('largest', 'by_extension', 'big_directories'):
            s['query:' + canned] = norm(call(api, '/api/query', {'canned': canned, 'n': 15, 'library': lib}))
        for name, b in {
            'ext': {'source': 'files', 'filters': [{'field': 'extension', 'op': 'eq', 'value': 'txt'}], 'groupBy': 'none', 'sort': 'name', 'dir': 'asc', 'limit': 50},
            'text': {'source': 'files', 'filters': [{'field': 'text', 'op': 'match', 'value': 'ZEBRAFISH'}], 'groupBy': 'none', 'sort': 'name', 'dir': 'asc', 'limit': 50},
            'dirs': {'source': 'files', 'filters': [], 'groupBy': 'directory', 'sort': 'size', 'dir': 'desc', 'limit': 30},
        }.items():
            s['builder:' + name] = norm(call(api, '/api/query', {'builder': b, 'library': lib}))
        for name, sql in {'count': 'SELECT count(*), sum(size) FROM vrpentries;',
                          'paths': "SELECT rpath(sname, sroll)||'/'||name FROM vrpentries WHERE name LIKE '%marker%';"}.items():
            s['sql:' + name] = norm(call(api, '/api/query', {'sql': sql, 'library': lib}))
        res[lib] = s
    ids = ','.join(LIBS)
    for name, body in {'count': {'sql': 'SELECT count(*) FROM vrpentries;'}, 'group': {'builder': {'source': 'files', 'filters': [], 'groupBy': 'extension', 'sort': 'size', 'dir': 'desc', 'limit': 30}}}.items():
        r = norm(call(api, '/api/query', dict(body, library=ids)))
        if isinstance(r, dict) and r.get('rowLibraries'):
            r['rows'] = [row + [l] for row, l in zip(r['rows'], r['rowLibraries'])]
            r.pop('rowLibraries', None)
        res.setdefault('multi', {})['query:' + name] = r
    res['multi']['search:word'] = hits(call(api, f'/api/search?q=ZEBRAFISH-4471&library={ids}'))
    res['libraries'] = [{'id': l['id'], 'caps': l['caps'], 'depth': l.get('depth'), 'state': (l.get('build') or {}).get('state')} for l in libraries(api) if l['id'] in LIBS]
    json.dump(res, open(out, 'w'), indent=1, sort_keys=True, ensure_ascii=False)
    errors = [(lib, k) for lib in LIBS for k, v in res[lib].items() if isinstance(v, dict) and ('__http' in v or '__err' in v or v.get('error'))]
    empty = [lib for lib in LIBS if lib != 'e2e-names' and not res[lib]['search:word'].get('hits')]
    print(f'{out}: {sum(len(res[l]) for l in LIBS)} answers, {len(errors)} errors, words missing in {empty}')
    return 1 if errors or empty or not res[LIBS[2]]['search:meaning'].get('hits') else 0


def stamps(data, out):
    found = {}
    for lib in LIBS:
        for p in glob.glob(os.path.join(data, '**', 'libraries', lib, 'index', '**', 'db.db'), recursive=True):
            found[os.path.relpath(p, data)] = os.stat(p).st_mtime_ns
    if not found:
        # The app keeps its data in INDEX_BASE; look one level wider.
        for p in glob.glob(os.path.expanduser('~/Library/Application Support/**/libraries/e2e-*/index/**/db.db'), recursive=True):
            found[p] = os.stat(p).st_mtime_ns
    json.dump(found, open(out, 'w'))
    print(f'{len(found)} index databases recorded')
    return 0 if found else 1


def same_stamps(a, b):
    x, y = json.load(open(a)), json.load(open(b))
    changed = [k for k in x if y.get(k) != x[k]]
    if changed:
        print(f'{len(changed)} index databases changed, for example {changed[:3]}')
    return 1 if changed or set(x) != set(y) else 0


def compare(a, b):
    x, y = json.load(open(a)), json.load(open(b))
    bad = 0

    def rows(v):
        return sorted(map(tuple, v.get('rows', []))) if isinstance(v, dict) else None

    # The Node server labels a result column it cannot name col1, col2, ...;
    # the Go server labels it with its expression (count(*)). The same
    # column at the same place is the same answer.
    def same_columns(u, v):
        p, q = u.get('columns'), v.get('columns')
        if p == q:
            return True
        return isinstance(p, list) and isinstance(q, list) and len(p) == len(q) \
            and all(a == b or re.fullmatch(r'col\d+', str(a)) for a, b in zip(p, q))

    for lib in x:
        if not isinstance(x[lib], dict):
            if x[lib] != y.get(lib):
                print(f'DIFFERENT {lib}')
                bad += 1
            continue
        for k in x[lib]:
            u, v = x[lib][k], y[lib].get(k)
            if u == v or (isinstance(u, dict) and 'rows' in u and isinstance(v, dict) and same_columns(u, v) and rows(u) == rows(v)):
                continue
            # A limited query that chooses among rows of the same size or date.
            if isinstance(u, dict) and isinstance(v, dict) and 'rows' in u and len(u['rows']) == len(v['rows']) and k.split(':')[1] in ('largest', 'ext') \
                    and sorted(r[1] for r in u['rows']) == sorted(r[1] for r in v['rows']):
                continue
            print(f'DIFFERENT {lib} {k}\n   before: {json.dumps(u, ensure_ascii=False)[:200]}\n   after:  {json.dumps(v, ensure_ascii=False)[:200]}')
            bad += 1
    print(f'{sum(len(x[l]) for l in x if isinstance(x[l], dict))} answers compared, {bad} different')
    return 1 if bad else 0


def fmt(data):
    for p in sorted(glob.glob(os.path.expanduser('~/Library/Application Support/**/libraries/e2e-*/index/*/db.db'), recursive=True))[:3]:
        cols = [r[1] for r in sqlite3.connect(p).execute('PRAGMA table_info(summary)')]
        print(f"{re.sub(r'.*/libraries/', '', p)}: summary has dev: {'dev' in cols}, isrolledup: {'isrolledup' in cols}")
    return 0


if __name__ == '__main__':
    cmd, args = sys.argv[1], sys.argv[2:]
    sys.exit({'corpus': lambda: corpus(*args) or 0, 'wait': lambda: wait(args[0], float(args[1])), 'battery': lambda: battery(*args),
              'stamps': lambda: stamps(*args), 'same-stamps': lambda: same_stamps(*args), 'compare': lambda: compare(*args),
              'format': lambda: fmt(*args)}[cmd]())
