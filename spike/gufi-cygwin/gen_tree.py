"""Generate the smoke-test tree: about 50,000 small files, plus spaces,
non-ASCII names and paths longer than 260 characters.

    python gen_tree.py C:\\tree C:\\spike\\manifest.json
"""
import json
import os
import random
import sys
import time

root, manifest = sys.argv[1], sys.argv[2]
root = os.path.abspath(root)
random.seed(7)


def lp(p):
    """The extended-length form, so the generator itself is not limited to 260."""
    return p if p.startswith('\\\\?\\') else '\\\\?\\' + p


def make(path, size=None):
    os.makedirs(lp(os.path.dirname(path)), exist_ok=True)
    n = random.randint(0, 4096) if size is None else size
    with open(lp(path), 'wb') as f:
        f.write((os.path.basename(path) + '\n').encode('utf-8', 'replace') * 1)
        f.write(b'x' * n)


t0 = time.time()
counts = {'regular': 0, 'spaces': 0, 'unicode': 0, 'long': 0}
exts = ['txt', 'md', 'csv', 'py', 'json']
for i in range(25):
    for j in range(20):
        d = os.path.join(root, 'd%02d' % i, 's%02d' % j)
        os.makedirs(lp(d), exist_ok=True)
        for k in range(100):
            make(os.path.join(d, 'file_%04d.%s' % (k, exts[k % 5])))
            counts['regular'] += 1

spaces = []
for k in range(50):
    p = os.path.join(root, 'with spaces', 'sub folder %d' % (k % 5), 'a file with spaces %d.txt' % k)
    make(p)
    spaces.append(p)
    counts['spaces'] += 1

unicode_files = []
names = ['caf\u00e9 \u00fcn\u00efcode', '\u65e5\u672c\u8a9e\u306e\u30d5\u30a9\u30eb\u30c0', '\u043f\u0430\u043f\u043a\u0430', 'emoji \U0001F600 folder']
files = ['na\u00efve r\u00e9sum\u00e9 %d.txt', '\u6587\u6863 \u30c6\u30b9\u30c8 %d.md', '\u0444\u0430\u0439\u043b %d.txt', 'emoji \U0001F600 file %d.txt']
for dn, fn in zip(names, files):
    for k in range(20):
        p = os.path.join(root, 'unicode', dn, fn % k)
        make(p)
        unicode_files.append(p)
        counts['unicode'] += 1

long_files = []
for depth, n in ((12, 6), (20, 4), (48, 2)):
    d = os.path.join(root, 'long', 'depth%d' % depth)
    for level in range(depth):
        d = os.path.join(d, ('L%02d_' % level) + 'x' * 22)
    for k in range(n):
        p = os.path.join(d, 'deep file %d.txt' % k)
        make(p)
        long_files.append({'path': p, 'len': len(p)})
        counts['long'] += 1

dirs = sum(len(dn) for _, dn, _ in os.walk(root))
total_files = sum(counts.values())
out = {
    'root': root, 'files': total_files, 'dirs_below_root': dirs, 'counts': counts,
    'seconds': round(time.time() - t0, 1),
    'long_files': long_files, 'unicode_files': unicode_files, 'space_files': spaces,
    'regular_example': os.path.join(root, 'd03', 's04', 'file_0007.json'),
}
os.makedirs(os.path.dirname(manifest), exist_ok=True)
with open(manifest, 'w', encoding='utf-8') as f:
    json.dump(out, f, ensure_ascii=False, indent=1)
print(json.dumps({k: out[k] for k in ('root', 'files', 'dirs_below_root', 'counts', 'seconds')}))
print('longest path:', max(x['len'] for x in long_files), 'shortest long:', min(x['len'] for x in long_files))
