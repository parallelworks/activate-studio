"""Write the xlsx sources LibreOffice converts to .xls: dates and times in
built-in and custom formats, numbers, text, booleans, errors, numeric,
text, boolean and error formulas, filled-down (shared) formulas, an array
formula, several sheets; and a 1904 copy."""
import sys, os, datetime
from openpyxl import Workbook
from openpyxl.worksheet.formula import ArrayFormula
from openpyxl.utils.datetime import CALENDAR_MAC_1904

def build(path, d1904=False):
    wb = Workbook()
    if d1904:
        wb.epoch = CALENDAR_MAC_1904
    s = wb.active; s.title = 'Dates'
    fmts = ['yyyy-mm-dd', 'mm/dd/yyyy', 'd-mmm-yy', 'yyyy-mm-dd hh:mm:ss', 'hh:mm:ss', 'h:mm AM/PM',
            '[h]:mm:ss', 'mm:ss.0', '[$-409]mmmm d, yyyy;@', 'dddd, mmmm dd, yyyy', '0.00" days"',
            '0\\d', '[Red]0.00', '0.00%', '#,##0.00', 'General', '@', 'yyyy"年"m"月"d"日"']
    vals = [datetime.datetime(2026, 11, 7), datetime.datetime(2026, 11, 7, 13, 45, 30),
            datetime.datetime(1900, 3, 1), datetime.datetime(1999, 12, 31, 23, 59, 59),
            datetime.time(6, 30), 45000.5, 60, 61, 0.75]
    r = 1
    s.cell(r, 1, 'format'); s.cell(r, 2, 'value')
    for f in fmts:
        for v in vals:
            r += 1
            s.cell(r, 1, f)
            c = s.cell(r, 2, v)
            c.number_format = f
    s.cell(r + 1, 1, 'date formula')
    c = s.cell(r + 1, 2, '=DATE(2026,11,7)+0.5'); c.number_format = 'yyyy-mm-dd hh:mm'
    n = wb.create_sheet('Numbers')
    nums = [0, 1, -1, 42, 100, 1200, 123456789, 2**31, 0.5, 0.1, 12.34, -0.01, 1/3, 1e15, 1e16, 1e20,
            1e-5, 3.14159265358979, 0.1 + 0.2, 1234567.891]
    for i, v in enumerate(nums, 1):
        n.cell(i, 1, v); n.cell(i, 2, v); n.cell(i, 3, v * 3)
    t = wb.create_sheet('Text')
    texts = ['plain', '  padded  ', 'Ünïcödé café', '日本語のテキスト', 'emoji 🚀 here', 'line\nbreak',
             'x' * 300, ('long ' * 2000).strip(), 'mixed ascii then 中文 then ascii']
    for i, v in enumerate(texts, 1):
        t.cell(i, 1, v)
    for i in range(1, 401):
        t.cell(i, 4, 'row %d value ' % i + ('é' if i % 2 else '中') * (i % 40))
    f = wb.create_sheet('Formulas')
    f['A1'] = 10; f['B1'] = 32
    f['A2'] = '=A1+B1'
    f['A3'] = '=CONCATENATE("phase-","one")'
    f['A4'] = '=IF(A2>40,"over budget","ok")'
    f['A5'] = '=A1>B1'
    f['A6'] = '=1/0'
    f['A7'] = '=NA()'
    f['A8'] = '=""'
    f['A9'] = '=REPT("abc",3000)'  # 9000 chars: a STRING record with CONTINUE
    f['A10'] = '=UNICHAR(26085)&"本"'
    for i in range(12, 32):  # a filled-down block, written as shared formulas
        f.cell(i, 1, i); f.cell(i, 2, '=A%d*2' % i); f.cell(i, 3, '="r"&A%d' % i)
    f['E1'] = 1; f['E2'] = 2; f['E3'] = 3
    f['F1'] = ArrayFormula('F1:F3', '=E1:E3*10')
    f['G1'] = ArrayFormula('G1', '=SUM(E1:E3*E1:E3)')
    b = wb.create_sheet('Bools and errors')
    b['A1'] = True; b['B1'] = False
    b['A2'] = '=TRUE()'; b['B2'] = '=NOT(TRUE())'
    b['A3'] = '=SQRT(-1)'; b['B3'] = '=#REF!'
    b['D6'] = 'after gap'
    wb.create_sheet('Empty')
    wb.save(path)

out = sys.argv[1]
build(os.path.join(out, 'lo_cases.xlsx'))
build(os.path.join(out, 'lo_cases_1904.xlsx'), True)
