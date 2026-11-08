"""Compare <id>.go.json with <id>.oracle.json in a run dir; print the
per-fixture and per-category tables and write diffs.txt."""
import sys, os, json, re, collections
run = sys.argv[1]
ids = sorted(f[:-8] for f in os.listdir(run) if f.endswith('.go.json'))
TIME = re.compile(r'^\d\d:\d\d:\d\d(\.\d{6})?$')
cat = collections.defaultdict(lambda: collections.Counter())
fix_rows = []
diffs = []
lo_why = collections.Counter()

def sheets_map(sheets):
    return [(s['name'], {(c[0], c[1]): c[2] for c in (s['cells'] or [])}) for s in (sheets or [])]

PURE = re.compile(r'(?:1899-12-31|1904-01-01) (\d\d:\d\d:\d\d(?:\.\d{6})?)(?=\t|\n|$)')

def fnum(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return None

def explain_lo(k, g, o, xg):
    """Why LibreOffice's view differs, when Go agrees with xlrd (xg)."""
    if not xg:
        return 'go differs from xlrd too'
    if k == 'formula-bool' and (g, o) in (('TRUE', '1'), ('FALSE', '0')):
        return 'LO writes boolean formula results to xlsx as 1/0'
    a, b = fnum(g), fnum(o)
    if a is not None and b is not None and abs(a - b) <= 1e-14 * max(abs(a), abs(b)):
        return 'LO keeps 15 significant digits'
    if k in ('date', 'formula-date') and a is None and b is not None:
        return 'openpyxl does not read the format as a date'
    if k.startswith('formula') or k == 'only-in-lo':
        return 'LO recalculates formulas on load'
    if k == 'date' and o is None:
        return 'LO/openpyxl drops out-of-range dates'
    if k == 'date' and o == '00:00:00':
        return 'serial 0: LO prints a time'
    if g and o and len(o) == 32767 and g.startswith(o):
        return 'LO cuts text at 32767 characters'
    return None

def explain_xlrd(g, x):
    if g is not None and x is not None and TIME.match(g) and (x == '1899-12-31 ' + g or x == '1904-01-01 ' + g):
        return 'pure time: Go prints the time alone'
    return None

for fid in ids:
    go = json.load(open(os.path.join(run, fid + '.go.json')))
    po = os.path.join(run, fid + '.oracle.json')
    if not os.path.exists(po):
        continue
    orc = json.load(open(po))
    gs = sheets_map(go.get('sheets'))
    kinds = {}
    for si, s in enumerate(go.get('sheets') or []):
        for c in (s['cells'] or []):
            kinds[(si, c[0], c[1])] = c[3]
    xs = sheets_map(orc.get('xlrd'))
    ls = sheets_map(orc.get('lo'))
    st = collections.Counter()
    st['text_same'] = int(orc.get('xlrd_text') is not None and go['text'] == orc['xlrd_text'])
    st['text_same_mod_time'] = int(orc.get('xlrd_text') is not None and go['text'] == PURE.sub(r'\1', orc['xlrd_text']))
    xmap = {n: d for n, d in xs}
    for oname, osheets in (('xlrd', xs), ('lo', ls)):
        if not osheets:
            st[oname + '_na'] = 1
            continue
        byname = {n: d for n, d in osheets}
        for si, (name, gd) in enumerate(gs):
            od = byname.get(name)
            if od is None and si < len(osheets):
                od = osheets[si][1]
            if od is None:
                od = {}
            for key in set(gd) | set(od):
                g, o = gd.get(key), od.get(key)
                k = kinds.get((si,) + key, 'only-in-' + oname)
                cat[k][oname + '_n'] += 1
                st[oname + '_n'] += 1
                if g == o:
                    cat[k][oname + '_same'] += 1
                    st[oname + '_same'] += 1
                    continue
                if oname == 'xlrd':
                    why = explain_xlrd(g, o)
                else:
                    xd = xmap.get(name, {})
                    why = explain_lo(k, g, o, g == xd.get(key) or explain_xlrd(g, xd.get(key)))
                    if why:
                        lo_why[why] += 1
                if why:
                    cat[k][oname + '_known'] += 1
                    st[oname + '_known'] += 1
                else:
                    cat[k][oname + '_diff'] += 1
                    st[oname + '_diff'] += 1
                diffs.append('%s\t%s\t%s!%s\t%s\tgo=%r\t%s=%r\t%s' % (fid, k, name, key, oname, g, oname, o, why or ''))
        # sheets the oracle has and Go does not
        gnames = {n for n, _ in gs}
        for n, od in osheets:
            if n not in gnames and len(osheets) > len(gs):
                for key, o in od.items():
                    cat['sheet-missing-in-go'][oname + '_n'] += 1
                    cat['sheet-missing-in-go'][oname + '_diff'] += 1
                    st[oname + '_diff'] += 1
                    diffs.append('%s\tsheet-missing\t%s!%s\t%s\tgo=None\t%r' % (fid, n, key, oname, o))
    fix_rows.append((fid, go.get('error', ''), go.get('panic', ''), orc.get('xlrd_error'), orc.get('lo_error'), st))

def pct(a, b):
    return '-' if not b else '%d/%d' % (a, b)

print('| fixture | Go error | text = extract_xls | cells = xlrd (known) | cells = LO |')
print('|---|---|---|---|---|')
for fid, gerr, pan, xerr, lerr, st in fix_rows:
    xl = 'n/a (%s)' % (xerr or '')[:40] if st['xlrd_na'] else '%s%s' % (pct(st['xlrd_same'], st['xlrd_n']), ' (+%d)' % st['xlrd_known'] if st['xlrd_known'] else '')
    lo = 'n/a (%s)' % (lerr or '')[:30] if st['lo_na'] else pct(st['lo_same'], st['lo_n'])
    tx = 'yes' if st['text_same'] else ('yes, but pure times' if st['text_same_mod_time'] else 'no')
    print('| %s | %s | %s | %s | %s |' % (fid, (pan and 'PANIC ' + pan[:40]) or gerr[:40], tx, xl, lo))
print()
print('| category | cells vs xlrd | = xlrd | known | differ | cells vs LO | = LO | explained | differ |')
print('|---|---|---|---|---|---|---|---|---|')
for k in sorted(cat):
    c = cat[k]
    print('| %s | %d | %d | %d | %d | %d | %d | %d | %d |' % (k, c['xlrd_n'], c['xlrd_same'], c['xlrd_known'], c['xlrd_diff'], c['lo_n'], c['lo_same'], c['lo_known'], c['lo_diff']))
print()
print('| LO difference, Go = xlrd | cells |')
print('|---|---|')
for k, v in lo_why.most_common():
    print('| %s | %d |' % (k, v))
open(os.path.join(run, 'diffs.txt'), 'w').write('\n'.join(diffs) + '\n')
