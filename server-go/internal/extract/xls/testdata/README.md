# xls test data

Each `.xls` here has a `.txt` beside it: the reader's text for it, written by `go test -run TestFixtures -update` and checked cell by cell against xlrd (through `indexer/enrich.py`'s `extract_xls`) and LibreOffice before it was committed. `TestFixtureCells` pins the lines each file is here for.

| File | Written by | Covers |
|---|---|---|
| `lo_cases.xls`, `lo_cases_1904.xls` | LibreOffice 24.2 (`soffice --convert-to "xls:MS Excel 97"`) from xlsx written by `conformance/gen_lo_src.py` | dates and times in built-in and custom formats, 1900 and 1904 systems, text formula results in STRING records (one of 9000 characters split by CONTINUE), shared (SHRFMLA) and array (ARRAY) formulas, boolean and error formulas, SST past one record |
| `xlwt_cases.xls`, `xlwt_cases_1904.xls` | xlwt 1.3.0, `conformance/gen_xlwt.py` | RK and MULRK, NUMBER, custom date formats, 1904, Unicode sheet names, long SST with CONTINUE, booleans |
| `poi/*.xls` | Microsoft Excel; from Apache POI's `test-data/spreadsheet` | `1904DateWindowing` (a 1904 Mac workbook), `testEXCEL_95` (BIFF5 with a codepage), `SharedFormulaTest`, `testArraysAndTables`, `DateFormats` |

The files under `poi/` are from the Apache POI project (https://github.com/apache/poi, `test-data/spreadsheet`), Copyright The Apache Software Foundation, under the Apache License, Version 2.0; see `poi/NOTICE`. They are unchanged. The other workbooks and all scripts were written for this package.

Cases no writer at hand produces on demand (STRING and SST records split at chosen points with the width flag flipping, rich-text runs across CONTINUE, BIFF5 in Windows-1251, an embedded chart substream, damaged records) are built record by record in `build_test.go` and checked in `xls_test.go`.

## Conformance run

`conformance/` holds the scripts for a local run against both oracles over any set of `.xls` files, such as the Excel-written fixtures in xlrd's `tests/samples` and Apache POI's `test-data/spreadsheet`, which are too many to commit. Downloaded files are untrusted input: keep each in its own directory and run Python with `-I`.

```sh
python3 -m venv $S/venv && $S/venv/bin/pip install xlrd xlwt openpyxl
# list.tsv lines: id<TAB>/abs/path.xls
XLS_LO_PROFILE=$S/lo-profile conformance/lo_convert.sh list.tsv $S/lo
awk -F'\t' -v d=$S/lo '{print $1"\t"$2"\t"d"/"$1"/out.xlsx"}' list.tsv > olist.tsv
XLS_DUMP_LIST=$PWD/list.tsv XLS_DUMP_OUT=$S/out go test -run TestDumpCorpus .
$S/venv/bin/python -I conformance/oracle.py $S/out olist.tsv
$S/venv/bin/python -I conformance/compare.py $S/out   # per fixture and per category
$S/venv/bin/python -I conformance/sets.py $S/out      # per source
```

LibreOffice's view is the file converted to xlsx by `soffice` and read back with openpyxl. It is a weaker oracle for formulas: LibreOffice recalculates them on load, where xlrd and this reader report the result the writing program saved.

Results on 441 workbooks (3 LibreOffice, 2 xlwt, 8 synthetic, 12 from xlrd, 416 from POI), November 2026: every cell the Go reader produces equals xlrd's except pure times (0 < serial < 1), which Go prints as `HH:MM:SS` where xlrd prefixes the epoch day (`1899-12-31 06:30:00`). The text equals `extract_xls` exactly for 420 files (21 of them empty for both: encrypted or not a workbook) and apart from pure times for 11 more. Go reads 7 damaged files xlrd rejects (one of them synthetic). Go does not read BIFF2 to BIFF4 (3 files; xlrd does) or encrypted workbooks (6 files; neither does xlrd, LibreOffice opens the 2 that use Excel's default password). Every difference from LibreOffice where Go agrees with xlrd falls in one of: recalculated formulas, boolean formula results exported to xlsx as 1 and 0, LibreOffice's 15-significant-digit rounding, formats openpyxl does not take for dates, out-of-range dates, serial 0, and LibreOffice's 32767-character cell limit.
