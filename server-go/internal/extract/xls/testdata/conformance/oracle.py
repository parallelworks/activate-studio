"""Oracles for the Go xls reader. For each .xls given: enrich.extract_xls's
text, xlrd's cells as extract_xls renders them, and LibreOffice's cells
(the file converted to .xlsx by soffice, read back with openpyxl).
Writes <out>/<id>.oracle.json. Usage: oracle.py OUT LIST_FILE
(LIST_FILE lines: id<TAB>path<TAB>lo_xlsx_path_or_empty)."""
import sys, os, json, datetime, math
# indexer/ of this repository, six levels above this script.
sys.path.insert(0, os.environ.get('XLS_INDEXER') or str(__import__('pathlib').Path(__file__).resolve().parents[6] / 'indexer'))
import enrich  # noqa
import xlrd

def num(v):
    v = float(v)
    if math.isnan(v): return 'nan'
    if math.isinf(v): return 'inf' if v > 0 else '-inf'
    return str(int(v)) if v.is_integer() else '%.15g' % v

def dt(d):
    return d.date().isoformat() if d.time().isoformat() == '00:00:00' else d.isoformat(sep=' ')

def xlrd_cells(path):
    out = []
    quiet = open(os.devnull, 'w')
    try:
        book = xlrd.open_workbook(path, on_demand=True, logfile=quiet, verbosity=0)
        for sheet in book.sheets():
            cells = []
            for r in range(sheet.nrows):
                for ci, c in enumerate(sheet.row(r)):
                    if c.ctype in (xlrd.XL_CELL_EMPTY, xlrd.XL_CELL_BLANK, xlrd.XL_CELL_ERROR):
                        s = ''
                    elif c.ctype == xlrd.XL_CELL_NUMBER:
                        s = num(c.value)
                    elif c.ctype == xlrd.XL_CELL_DATE:
                        try:
                            s = dt(xlrd.xldate_as_datetime(c.value, book.datemode))
                        except Exception:
                            s = str(c.value)
                    elif c.ctype == xlrd.XL_CELL_BOOLEAN:
                        s = 'TRUE' if c.value else 'FALSE'
                    else:
                        s = str(c.value).strip()
                    if s:
                        cells.append([r, ci, s, c.ctype])
            out.append({'name': sheet.name, 'cells': cells})
        return out, None
    except Exception as e:
        return None, '%s: %s' % (type(e).__name__, e)
    finally:
        quiet.close()

def lo_cells(xlsx):
    import openpyxl
    try:
        wb = openpyxl.load_workbook(xlsx, data_only=True)
    except Exception as e:
        return None, '%s: %s' % (type(e).__name__, e)
    out = []
    datemode = 1 if wb.epoch.year == 1904 else 0
    for ws in wb.worksheets:
        cells = []
        for row in ws.iter_rows():
            for c in row:
                v = c.value
                if v is None:
                    continue
                if c.data_type == 'e':
                    s = ''
                elif isinstance(v, bool):
                    s = 'TRUE' if v else 'FALSE'
                elif isinstance(v, (int, float)):
                    s = num(v)
                elif isinstance(v, datetime.datetime):
                    s = dt(v)
                elif isinstance(v, datetime.date):
                    s = v.isoformat()
                elif isinstance(v, datetime.time):
                    s = v.isoformat()
                elif isinstance(v, datetime.timedelta):
                    # [h]:mm:ss and the like: openpyxl gives the serial as
                    # a duration; print it as the date it is.
                    serial = v.total_seconds() / 86400
                    if 0 < serial < 1:
                        s = (datetime.datetime(2000, 1, 1) + v).time().isoformat()
                    else:
                        s = dt(xlrd.xldate_as_datetime(serial, datemode))
                else:
                    s = str(v).strip()
                if s:
                    cells.append([c.row - 1, c.column - 1, s])
        out.append({'name': ws.title, 'cells': cells})
    return out, None

out = sys.argv[1]
for line in open(sys.argv[2]):
    fid, path, xlsx = line.rstrip('\n').split('\t')
    res = {'path': path}
    try:
        res['xlrd_text'] = enrich.extract_xls(__import__('pathlib').Path(path))
    except Exception as e:
        res['xlrd_text'] = None
    res['xlrd'], res['xlrd_error'] = xlrd_cells(path)
    if xlsx and os.path.exists(xlsx):
        res['lo'], res['lo_error'] = lo_cells(xlsx)
    else:
        res['lo'], res['lo_error'] = None, 'no conversion'
    with open(os.path.join(out, fid + '.oracle.json'), 'w') as f:
        json.dump(res, f)
