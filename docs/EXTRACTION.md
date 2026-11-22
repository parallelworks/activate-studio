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

### Reading from an object store

`--rclone REMOTE:PATH` reads each listed path from an rclone remote instead of a folder, for example `--rclone store:bucket/prefix`, with `--rclone-bin` naming the program and `--rclone-config` a configuration file other than rclone's own. Each object is one request (`rclone cat`), written to a temporary file beside the artifact, read, and removed. A store gives no modification time without a second request, so the listed mtime is used, and an object whose size is not the listed one is recorded as `changed`. `--max-size BYTES` leaves a file listed as larger than that unread, recorded as `skipped`, with no request for it. This is how the Studio reads a bucket library's text (docs/LIBRARIES.md, "Reading text from a bucket").

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

An SQLite file with GUFI's common external database schema as GUFI main's queries read it, table for table and view for view (`index-extract schema` prints it): `ext_assets`, one row per file with text, the file's own asset (`component_type` `file`, its `asset_id` the file's `outer_file_uuid`), its route the reader that read it (`text`, `downmark`, `native`, `image` or `external`), its `file_metadata` what was read (bytes, characters, chunks, reader and extension, JSON, with no name or path) and its `indexing_provenance` a JSON note of the extractor and its settings; the fts5 table `ext_bm25`, the text in chunks of at most 2,000 characters that do not overlap, cut at blank lines where there are some, each joined to its asset by `asset_id` and `outer_file_uuid`; and the views `ext_bm25_v`, `ext_assets_by_file_v` (one row per file, its assets nested in `assets_json`), `ext_assets_by_file_sv` and `ext_assets_by_file_flat_sv`. An artifact written before has its old views replaced when it is next opened.

Every record carries `inode`, `mtime` and `extra_info`, the id of the file system the inode belongs to (fsid), the same for every file of one file system at a site: by default the site's id in `STUDIO_FSID`, or else this computer's name and the mount point of the files (`host:/mnt/data`), or for `--rclone` the remote and path; `--extra-info` sets it. `outer_file_uuid` is derived from those three, so the same version of a file gets the same id in every run.

There are no paths in it. A file renamed keeps its text; a file changed gets read again. Beside the schema's tables, `studio_extract_files` has one row per file looked at: its key, size, state (`text`, `empty`, `skipped`, `changed`, `missing` or `failed`), reader, chunk and character counts, and the error.

## Vectors

```sh
index-extract vectors --artifact text.db --model minilm384.gguf --gufi-sqlite3 /opt/gufi/bin/gufi_sqlite3
```

`vectors` gives each chunk of an artifact that has none a vector, with a GGUF model through `gufi_sqlite3` (GUFI's SQLite, which carries sqlite-vec and sqlite-lembed): the first `--max-chars` characters of the chunk (800 by default), made unit length, at the model's own dimensions (`--dims`, 384), named `--model-name` (`sentence-transformers/all-MiniLM-L6-v2` by default, as a query's model list names it). They are kept in the artifact's `studio_vectors` until the shards are written, since a vec0 table needs sqlite-vec. A chunk with a vector is passed by, so it can run again after every `files`.

## Loading it into an index

Two more commands follow GUFI's plan for system-level external databases.

```sh
index-extract spread --spread /data/spread --fsid store1 text-*.db
index-extract shards --spread /data/spread --fsid store1 --extra-info host:/mnt/data --index /index/top --gufi-sqlite3 /opt/gufi/bin/gufi_sqlite3
```

`spread` puts the records of one or more artifacts, with their vectors, into a spread tree, the pristine copy that outlives any index: `<spread>/ext/<fsid>/89/67/45.db` holds the records of inode 123456789, two digits a level, the layout GUFI's `gufi_spread_shard_path.sh` gives. Each file's records replace those of the same inode, mtime and extra_info under that db's write lock, and other versions of the file are kept. The top folder gets GUFI's two configuration databases, `fsid.db` and `extdbprefix.db`, and the prefix's SQL as `ext.sql`. `--fsid` names the file system or store, one folder of the tree, so several can share a spread tree.

`shards` writes each folder's permission shards in the index from the spread tree, the records of `--extra-info` (by default `--fsid`), named and classed as GUFI's `spread_to_external` names them, judged against the folder's owner: `ext_ur.db` when the folder's owner owns the file and may read it, `ext_gr.db` when the folder's group is the file's and may read it, `ext_or.db` when others may read it, and `ext_<uid>_<gid>.db`, of the file's own owner and group, for anything else. A shard is readable by those its name says (0600, 0640, 0644, or the narrowest mode of its files with the owner's read and write), so attaching it is the permission check; unlike GUFI's, its owner can always read it, so a Studio that is not root reads the shards it writes. Run as root, it is given to the files' owner. With `--gufi-sqlite3`, a shard whose files have vectors gets `ext_vectors`, a vec0 table of `FLOAT32[4096]` declared `distance_metric=cosine`, each vector unit length in its first native dimensions and zeros after, and its view `ext_vectors_v`; without it, or where that fails, shards hold text alone. A folder's shards are written whole each time, so the records of a file that changed or went are dropped, a shard no file needs any more is removed, and the earlier Studio's `studio_XX_<uid>_<gid>.db` shards are removed wherever they are found. After the index is rebuilt, for example by a new listing of a bucket, `shards` alone puts the text back: nothing is read again.

GUFI main's queries attach the shards with `--external-copy ext` (any file beside `db.db` whose name holds `ext`), and the Studio's search with `STUDIO_EXTERNAL_DB_PATTERN` (default `\.db$`), so the shards are searched by text and by meaning with nothing more to set up.

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
