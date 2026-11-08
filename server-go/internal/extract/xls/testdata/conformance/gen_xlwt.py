"""Write xlwt fixtures: dates in several formats, 1904, numbers (RK, MULRK,
NUMBER), unicode and long SST strings, booleans, errors, formulas,
several sheets."""
import sys, datetime, os
import xlwt
out = sys.argv[1]

def book(path, d1904=False):
    wb = xlwt.Workbook(encoding='utf-8')
    wb.dates_1904 = d1904
    fmts = {
        'date': 'yyyy-mm-dd', 'dmy': 'dd/mm/yyyy', 'dt': 'yyyy-mm-dd hh:mm:ss', 'time': 'hh:mm:ss',
        'elapsed': '[h]:mm:ss', 'mmss': 'mm:ss.0', 'locale': '[$-409]mmmm d, yyyy',
        'quoted': '0.00" days"', 'escaped': '0\\d', 'color': '[Red]0.00', 'pct': '0.00%',
        'sci': '0.00E+00', 'text': '@', 'general': 'General', 'thousands': '#,##0.00',
        'mdy': 'm/d/yy', 'ampm': 'h:mm AM/PM', 'month': 'mmm-yy',
    }
    st = {k: xlwt.easyxf(num_format_str=v) for k, v in fmts.items()}
    s = wb.add_sheet('Dates')
    s.write(0, 0, 'format'); s.write(0, 1, 'value')
    vals = [datetime.datetime(2026, 11, 7), datetime.datetime(2026, 11, 7, 13, 45, 30),
            datetime.datetime(1900, 3, 1), datetime.datetime(1999, 12, 31, 23, 59, 59),
            datetime.time(6, 30), datetime.datetime(2000, 2, 29, 0, 0, 0, 500000)]
    r = 1
    for k in fmts:
        for v in vals:
            s.write(r, 0, k)
            s.write(r, 1, v, st[k])
            r += 1
    for k in ('date', 'dt', 'general'):
        for serial in (1, 59, 60, 61, 0.25, 0.999988, 45000.5, 2958465.99):
            s.write(r, 0, k + ' serial')
            s.write(r, 1, serial, st[k]); r += 1
    n = wb.add_sheet('Numbers')
    nums = [0, 1, -1, 42, 1200, 100, 123456789, 2**30, 2**31, -2**29, 0.5, 0.1, 1.25, 12.34, -0.01,
            1/3, 2/3, 1e15, 1e16, 1e20, 123456789012345678, 1e-5, 1.5e-10, 3.14159265358979,
            0.1 + 0.2, 1234567.891, -1e300, 5e-324]
    for i, v in enumerate(nums):
        n.write(i, 0, v)
        n.write(i, 1, v); n.write(i, 2, v * 3)  # MULRK runs
        n.write(i, 4, str(v))
    t = wb.add_sheet(u'Текст 文字')
    texts = [u'plain', u'  padded  ', u'Ünïcödé café', u'日本語のテキスト', u'emoji 🚀 here',
             u'tab\tinside', u'line\nbreak', u'x' * 300, (u'long ' * 2000).strip(), u'',
             u'mixed ascii then 中文 then ascii', u' nbsp ']
    for i, v in enumerate(texts):
        t.write(i, 0, v)
        t.write(i, 2, v.upper())
    for i in range(400):  # enough distinct strings to push the SST over 8224 bytes
        t.write(i, 5, u'row %d value ' % i + (u'é' if i % 2 else u'中') * (i % 40))
    f = wb.add_sheet('Formulas')
    f.write(0, 0, 10); f.write(0, 1, 32)
    f.write(1, 0, xlwt.Formula('A1+B1'))
    f.write(2, 0, xlwt.Formula('"ab"&"cd"'))
    f.write(3, 0, xlwt.Formula('A1>B1'))
    f.write(4, 0, xlwt.Formula('1/0'))
    f.write(5, 0, xlwt.Formula('DATE(2026,11,7)'), st['date'])
    b = wb.add_sheet('Bools')
    b.write(0, 0, True); b.write(0, 1, False)
    b.write(1, 0, xlwt.Formula('AND(1,1)'))
    b.write_merge(3, 4, 0, 2, 'merged')
    b.write(6, 3, 'after gap')
    wb.add_sheet('Empty')
    wb.save(path)

book(os.path.join(out, 'xlwt_cases.xls'))
book(os.path.join(out, 'xlwt_cases_1904.xls'), d1904=True)
