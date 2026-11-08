"""Per-source summary of a compare run: python sets.py RUN_DIR"""
import sys, os, json, re, collections
run = sys.argv[1]
PURE = re.compile(r'(?:1899-12-31|1904-01-01) (\d\d:\d\d:\d\d(?:\.\d{6})?)(?=\t|\n|$)')
S = collections.defaultdict(collections.Counter)
for f in sorted(os.listdir(run)):
    if not f.endswith('.go.json'): continue
    fid = f[:-8]; st = fid.split('__')[0]
    g = json.load(open(os.path.join(run, f))); o = json.load(open(os.path.join(run, fid + '.oracle.json')))
    c = S[st]; c['files'] += 1
    xt = o.get('xlrd_text')
    if o.get('xlrd_error'): c['xlrd fails'] += 1
    if g.get('error'): c['go error'] += 1
    if g.get('panic'): c['panic'] += 1
    if xt is not None and g['text'] == xt: c['text same'] += 1
    elif xt is not None and g['text'] == PURE.sub(r'\1', xt): c['same but pure times'] += 1
    elif o.get('xlrd_error') and not g.get('error') and g['text']: c['go reads, xlrd fails'] += 1
    else: c['other'] += 1
print('| source | files | text = extract_xls | = except pure times | Go reads, xlrd fails | other | Go errors | xlrd errors |')
print('|---|---|---|---|---|---|---|---|')
for st, c in sorted(S.items()):
    print('| %s | %d | %d | %d | %d | %d | %d | %d |' % (st, c['files'], c['text same'], c['same but pure times'], c['go reads, xlrd fails'], c['other'], c['go error'], c['xlrd fails']))
