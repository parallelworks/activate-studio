# Extracting text from a list of files

`index-extract` reads the text of files with the readers the Studio's own indexing uses, and writes it in GUFI's common external database schema, keyed by each file's inode and mtime. It is a program of its own (`server-go/cmd/index-extract`), separate from the server, so it can run where the files are: on this computer, on a node next to the storage, or as the jobs of a scheduler array, each taking a shard of the list. Nothing in it is specific to one kind of storage. An index of a local folder, of an object store or of a parallel file system gives the same list, and the output loads into any index of the same files.

The apps carry it beside `studio-server` in `app/bin`. Each release also has it as static Linux programs for a cluster job to fetch: `index-extract-linux-x86_64.tar.gz` and `index-extract-linux-aarch64.tar.gz`, each with a `.sha256`. From a clone, `go build ./cmd/index-extract` in `server-go`.

## The list

One line per file: its inode, mtime (seconds) and size, then its path, separated by GUFI's delimiter (`\x1e`, ASCII record separator). The path comes last, so it may hold any character but a line end. The size may be left empty. This is what `gufi_query` prints for an index:

```sh
gufi_query -E "SELECT inode, mtime, size, rpath(sname, sroll)||'/'||name FROM vrpentries WHERE type = 'f';" /index/top > files.list
```

The listed paths begin with the index's path. `--strip /index/top` removes that, and `--root /data/top` names the folder the rest is found in. Any other walk that knows the four values can write the list, with another delimiter given by `--delim` (`\t`, `,` and so on).

## Running it

```sh
index-extract files --list files.list --out text.db --strip /index/top --root /data/top
```

Each file is read only when it is the version the list names: a file whose mtime or size differs is recorded as `changed` and not read, since its text would be keyed to the wrong version. A file of a kind nothing reads (`.bin`, say) is not opened at all. The summary is one JSON line on standard output: lines, bad lines, files by state, chunks, bytes read, seconds, the helper programs found (`helpers`) and what this run's files lost for want of one (`missing`). With `--progress`, `@progress {"phase":"files","done":N,"total":M,"bytes":B}` lines come first. Problems with single files go to standard error, one line each, and do not fail the run. The exit status is 0, 1 when the artifact could not be written or the run was stopped, and 2 for a bad command line.

`--list -` reads standard input, so `gufi_query ... | index-extract files --out text.db ...` works. The extractor's own options are those of the Studio's pass: `--jobs`, `--no-ocr`, `--no-documents`, `--pdf-reader`, `--soffice`, `--extractors` (contributed extractors, as in Settings), and `--report FILE` to keep the summary. `--extract-cache DIR` keeps the converted text in a cache of the Studio's layout; by default a temporary one is used and removed.

## Shards and job arrays

`--shard K/N` reads only the files whose inode modulo N is K. N jobs given the same list, with K from 0 to N-1, read every file exactly once, each into its own artifact. With Slurm:

```sh
#SBATCH --array=0-31
index-extract files --list files.list --out "text-$SLURM_ARRAY_TASK_ID.db" \
  --shard "$SLURM_ARRAY_TASK_ID/32" --strip /index/top --root /data/top --jobs "$SLURM_CPUS_PER_TASK"
```

Only the artifacts need to come back, which hold text and no copies of the files.

## Starting again

A run goes on from what its artifact holds. Every file the artifact has a row for is passed by, so a job that ran out of time, or ran twice, reads only what it had not. `--retry` reads again the files held without text (skipped, empty, missing, changed or failed), for example after tesseract is installed. A stopped run records nothing for a file it was reading.

## The artifact

An SQLite file with GUFI's common schema (`index-extract schema` prints it): `ext_assets`, one row per file with text, its `asset_id` the file's `outer_file_uuid`, its route the reader that read it (`text`, `downmark`, `native`, `image` or `external`) and its `indexing_provenance` a JSON note of the extractor and its settings; the fts5 table `ext_bm25`, the text in chunks of at most 2,000 characters that do not overlap, cut at blank lines where there are some; and the views `ext_bm25_v`, `ext_assets_by_file_v` and `ext_assets_by_file_flat_v`. Every record carries `inode`, `mtime` and `extra_info` (`--extra-info`, empty by default; a source whose inode and mtime are not unique on their own, such as several devices in one index, puts the device or another key there). `outer_file_uuid` is derived from those three, so the same version of a file gets the same id in every run.

There are no paths in it. A file renamed keeps its text; a file changed gets read again. Beside the schema's tables, `studio_extract_files` has one row per file looked at: its key, size, state (`text`, `empty`, `skipped`, `changed`, `missing` or `failed`), reader, chunk and character counts, and the error.

Placed beside a directory's `db.db` under any name matching `STUDIO_EXTERNAL_DB_PATTERN` (default `\.db$`), such as the permission shard name `studio_UR_<uid>_<gid>.db`, an artifact is searched by the Studio and by GUFI's example queries: rows join to that directory's entries by inode and mtime. Loading artifacts into a spread tree and from there into each directory's permission shards is the next part of this work.

## What it needs at run time

Text files, Office documents (`.docx`, `.pptx`, `.xlsx`, `.doc`), PDFs with a text layer, OpenDocument, `.xls`, `.ppt` and mail are read by the program itself, with nothing installed. The rest is optional, and a missing helper never fails a run; the summary's `missing` names what was lost.

| Helper | Used for | Without it |
|---|---|---|
| `tesseract` | OCR of images and of scanned PDF pages | images get text only from a vision model, where one is set; scanned pages give none |
| `pdftoppm` (poppler) | rendering a thin PDF's pages for OCR | the built-in renderer |
| `pdftotext`, `pdfinfo` (poppler) | a PDF the converter cannot open; the page count | the built-in reader and page count |
| `soffice` (LibreOffice, `--soffice`) | a legacy file the built-in readers get nothing from | those files stay without text |
| a vision model (`ADE_VISION_MODEL` and a gateway key) | describing images | images are read by OCR alone |

Vectors are not written yet. When they are, they will need ONNX Runtime (`libonnxruntime` beside the program, or `STUDIO_ORT_LIB`) and the MiniLM model, as the Studio's search by meaning does; without them a run will write text only and say so in its summary.
