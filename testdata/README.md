# Extraction test corpus

A small synthetic corpus and an end-to-end test over it, so a change to the
extraction pipeline — a downmark bump, an `enrich.py` edit, a new format —
shows what it broke instead of being discovered by a reader who cannot find
a document they know is there.

```sh
pnpm test                      # builds the server, then runs both suites
node --test testdata/extraction.test.mjs   # converters and the index text
node --test testdata/server.test.mjs       # the server's own indexing path
node testdata/check-packaging.mjs DIR      # a `pnpm deploy` tree can convert
```

## What it runs

The pipeline as deployed, not a stand-in for it: `server/dist/preextract.js`
converts documents into an extract cache, `indexer/enrich.py` indexes that
cache into per-directory fts5 `words` tables, and the assertions read the
rows that land there — the text search, chat and the viewer actually see.
Checking the cache files alone would miss `enrich.py` declining to reuse an
entry, which has been a real failure.

`index-helper.py` supplies the one thing GUFI would otherwise provide: an
empty `db.db` per directory. Building GUFI for a text-extraction test is not
worth it, and it does not build on macOS at all.

## Why properties instead of golden files

`expectations.json` says what must remain true of each file's indexed text —
the content is there, and the structure survived — rather than pinning exact
bytes. Converter output legitimately shifts between downmark releases
(table padding, a heading level, spacing); golden files would churn on every
bump and train everyone to re-bless them unread. `absent` entries pin
regressions we have already had.

The fixtures carry unmistakable markers (`VELOCITY-INLET-7734`,
`SCANNED BODY 7742`) so an assertion failure names the missing content
rather than reporting a diff of prose.

## Coverage

| Fixture | Guards |
|---|---|
| `docs/handbook.docx` | Headings and tables survive (the flat-text readers destroyed both) |
| `docs/quarterly.xlsx` | Per-sheet headings, cells as a Markdown table |
| `docs/kickoff.pptx` | Slide boundaries, slide tables, speaker notes |
| `docs/report.pdf` | Typeset PDF text, both pages, and that OCR did **not** run on it |
| `scans/invoice-scan.pdf` | A pure scan is OCR'd and the OCR text is marked |
| `scans/form-mixed.pdf` | A scanned body under a typed header is still OCR'd — the `thin` policy |
| `images/diagram.png` | Text inside an image reaches the index |
| `notes/*` | Markdown, CSV, YAML, JSON and source files keep flowing through untouched |
| `broken/truncated.docx` | An unreadable file is logged and counted, never fatal, and indexes nothing |

Two behaviours are also asserted directly: a second pass reuses the cache
instead of reconverting, and with `DOWNMARK_FORCE_WASM=1` (a platform with
no native binary) PDFs fall through to `enrich.py`'s pdftotext path while
Office documents still convert.

`server.test.mjs` boots the real server against a throwaway knowledge base,
indexes a directory through `POST /api/index/dir`, and reads the document
back through the API the viewer uses — covering the call sites in
`indexing.ts` that the converter tests never reach. `gufi_dir2index` is
stubbed (`fake-gufi/`), since GUFI is a metadata indexer, does not build on
macOS, and is not the subject; everything else is the real server.

`check-packaging.mjs` asserts that what `pnpm deploy --prod --legacy`
produces can still convert: the native binary resolves, is executable, and
runs, and the wasm sits where `deploy/app.def`'s proof step looks for it.
CI runs it, because that tree — not the workspace — is what ships.

Tests skip with a stated reason when a dependency is absent — tesseract for
OCR, python 3.10+ for the indexing half — so a bare checkout still verifies
what it can rather than failing.

## Regenerating the fixtures

```sh
python3 testdata/make-corpus.py     # needs poppler (pdftoppm) for the scans
```

Every fixture is synthesised: the corpus carries no third-party licence, the
whole thing is ~75 KiB, and generation is deterministic (fixed zip
timestamps, no PDF `/CreationDate`), so regenerating without a content
change leaves the bytes identical and git sees nothing.

The OOXML files are written as minimal parts by hand rather than through
python-docx and friends — those libraries were removed from this project,
and a fixture generator is not a reason to bring them back.
