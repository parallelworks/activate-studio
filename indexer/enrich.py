#!/usr/bin/env python3
"""Enrich a GUFI index of the knowledge base with extracted text.

For every real-content file, extract plain text and store it in an fts5
`words` table inside the per-directory db.db of the GUFI index (joined to
entries by inode, per the GUFI master document pattern). DOCX/PDF/PPTX/XLSX
text is also written to an on-disk extract cache the server uses for
previews. Every directory db gets the `words` table, empty or not, so
MATCH queries never hit a missing table.

Documents normally arrive already converted: the server (and reindex.sh)
run `server/dist/preextract.js`, which turns .docx/.pptx/.xlsx/.doc, and
PDFs where downmark's native binary is installed, into Markdown and writes
the cache entries this script reuses. The readers below are the fallback:
the standard-library OOXML pass for Office files, pdftotext for PDFs on a
host without the binary. A cached PDF whose text is thin for its page count
is still OCR'd here, since downmark reads only pages with no text at all.
Image OCR and captions live here alone, and so do the formats downmark does
not read (.ppt, .xls, OpenDocument), through an installed LibreOffice.

The pass runs in two steps. Files that need extracting (no cache entry, or
one older than the file) are read in a pool of worker processes, --jobs of
them, and their text lands in the extract cache. Then each directory's
words table is written, from the text files themselves and from the cache,
in a pool of threads. With --progress, `@progress {...}` lines on stdout
say how far it has got, for the server's progress bar.
"""
import argparse
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

EXCLUDE_DIRS = {
    '.git', 'node_modules', '.venv', 'venv', '__pycache__', 'dist', 'build',
    '.cache', '.pytest_cache', 'screenshots', '.ipynb_checkpoints',
}
TEXT_SUFFIXES = {
    '.md', '.txt', '.py', '.sh', '.yaml', '.yml', '.json', '.csv', '.tsv', '.xml',
    '.html', '.css', '.js', '.ts', '.tsx', '.svg', '.toml', '.cfg', '.ini', '.def', '.mjs', '.sql',
}
# .doc is read through the pre-extracted cache, or LibreOffice when there is one.
EXTRACT_SUFFIXES = {'.pdf', '.docx', '.pptx', '.xlsx', '.doc'}
# Read only through LibreOffice, which converts them to the OOXML format
# whose reader is below (.doc only when downmark could not convert it).
LEGACY_SUFFIXES = {'.ppt': 'pptx', '.odp': 'pptx', '.xls': 'xlsx', '.ods': 'xlsx',
                   '.odt': 'docx', '.doc': 'docx'}
IMAGE_SUFFIXES = {'.png', '.jpg', '.jpeg', '.gif', '.webp'}
MAX_TEXT = 500_000
MAX_CAPTION_BYTES = 8 * 1024 * 1024
MIN_IMAGE_BYTES = 8 * 1024  # skip icons and tiny assets

# What the worker processes may use, set by main() and passed to each.
OPTIONS = {'ocr': True, 'pdf_reader': 'auto', 'soffice': ''}


def _ooxml_text(path: Path, members: str, break_tags: tuple) -> str:
    """Text out of an OOXML file with the standard library alone.

    docx, pptx and xlsx are zipped XML, so their text is reachable without
    any library. This only runs for a document downmark could not convert
    (see the module docstring): a rough extraction beats indexing a Word
    document as its filename.
    """
    import re
    import zipfile
    out = []
    try:
        with zipfile.ZipFile(path) as z:
            names = sorted(n for n in z.namelist() if re.fullmatch(members, n))
            for name in names:
                xml = z.read(name).decode('utf-8', errors='replace')
                for tag in break_tags:
                    xml = xml.replace(tag, '\n')
                text = re.sub(r'<[^>]+>', '', xml)
                text = (text.replace('&amp;', '&').replace('&lt;', '<').replace('&gt;', '>')
                            .replace('&quot;', '"').replace('&apos;', "'"))
                out.append('\n'.join(line.strip() for line in text.split('\n') if line.strip()))
    except Exception:
        return ''
    return '\n'.join(x for x in out if x)


def extract_docx(path: Path) -> str:
    return _ooxml_text(path, r'word/document\.xml', ('</w:p>', '</w:tr>', '</w:tc>'))


# A PDF with almost no extractable text is a scan; below this many
# characters per page, try reading the pages as images instead.
OCR_CHARS_PER_PAGE = 120


def _pdf_page_count(path: Path) -> int:
    try:
        out = subprocess.run(['pdfinfo', str(path)], capture_output=True, text=True, timeout=60)
        for line in out.stdout.splitlines():
            if line.startswith('Pages:'):
                return int(line.split(':', 1)[1].strip())
    except Exception:
        pass
    # No poppler (the macOS app has none): pypdf counts the pages instead.
    try:
        from pypdf import PdfReader  # type: ignore
        return max(1, len(PdfReader(str(path)).pages))
    except Exception:
        pass
    return 1


def _pdf_ocr(path: Path, pages: int) -> str:
    """OCR a scanned PDF, page by page, up to a sane limit.

    Signed and faxed forms carry their text as an image, so pdftotext
    returns a page number and nothing else. Rendering to PNG and running
    tesseract is slow, so it only happens when the text layer is thin.
    """
    if not OPTIONS['ocr'] or not (shutil.which('pdftoppm') and shutil.which('tesseract')):
        return ''
    limit = min(pages, 30)
    parts = []
    with tempfile.TemporaryDirectory() as tmp:
        prefix = str(Path(tmp) / 'page')
        try:
            subprocess.run(['pdftoppm', '-r', '200', '-f', '1', '-l', str(limit), '-png', str(path), prefix],
                           capture_output=True, timeout=600)
        except Exception:
            return ''
        for png in sorted(Path(tmp).glob('page*.png')):
            try:
                out = subprocess.run(['tesseract', str(png), 'stdout', '--psm', '3'],
                                     capture_output=True, text=True, timeout=180)
                text = (out.stdout or '').strip()
                if text:
                    parts.append(text)
            except Exception:
                continue
    body = '\n\n'.join(parts)
    return f'{OCR_HEADER}\n{body}' if body else ''


# downmark marks the pages its own OCR filled in; a document carrying the
# marker has already been read as ink and a second pass adds nothing.
DOWNMARK_OCR_MARKER = 'includes OCR text'
# Our own OCR block carries this header for the same reason: a cached entry
# that already holds one has been read as ink, and re-reading it appends a
# second copy of the same text and re-runs the render-plus-tesseract pass on
# every later enrichment until the entry finally crosses the thin threshold.
OCR_HEADER = 'Text read from scanned pages (OCR):'


# OCR covers at most this many pages, so a document already holding more
# characters than that many pages' worth cannot gain enough from a pass to
# be worth one. Checking this before pdfinfo keeps a reindex over thousands
# of text PDFs from forking a process per file per pass to learn nothing.
OCR_TEXT_CEILING = OCR_CHARS_PER_PAGE * 30


def ocr_if_thin(path: Path, text: str) -> str:
    """Append OCR text when the text layer is thin for the page count."""
    if DOWNMARK_OCR_MARKER in text or OCR_HEADER in text:
        return text
    if len(text.strip()) >= OCR_TEXT_CEILING:
        return text
    pages = _pdf_page_count(path)
    if len(text.strip()) < OCR_CHARS_PER_PAGE * pages:
        ocr = _pdf_ocr(path, pages)
        if ocr:
            # Keep both: the text layer may hold a header the scan blurs.
            return f'{text}\n\n{ocr}' if text.strip() else ocr
    return text


def extract_pdf(path: Path) -> str:
    """A PDF's text layer: pdftotext where poppler is installed, then pypdf
    (bundled with the macOS app), as --pdf-reader allows."""
    text = ''
    reader = OPTIONS['pdf_reader']
    if reader in ('auto', 'pdftotext'):
        try:
            # -layout keeps columns and table cells apart; without it, rows run
            # together into one line and read badly in a snippet.
            out = subprocess.run(['pdftotext', '-layout', '-q', str(path), '-'],
                                 capture_output=True, text=True, timeout=180)
            if out.returncode == 0:
                text = out.stdout or ''
        except (FileNotFoundError, subprocess.TimeoutExpired):
            pass
    if not text.strip() and reader in ('auto', 'pypdf'):
        try:
            from pypdf import PdfReader  # type: ignore
            pdf = PdfReader(str(path))
            parts = []
            for page in pdf.pages:
                parts.append(page.extract_text() or '')
                if sum(len(p) for p in parts) > MAX_TEXT:
                    break
            text = '\n'.join(parts)
        except Exception:
            text = ''
    return ocr_if_thin(path, text)


def extract_legacy(path: Path) -> str:
    """A format downmark does not read, through LibreOffice: converted to its
    OOXML counterpart in a scratch directory, then read like one. Each worker
    process keeps its own LibreOffice profile, so conversions run side by
    side instead of fighting over one."""
    soffice = OPTIONS['soffice']
    target = LEGACY_SUFFIXES.get(path.suffix.lower())
    if not soffice or not target:
        return ''
    profile = Path(tempfile.gettempdir()) / f'studio-soffice-{os.getpid()}'
    with tempfile.TemporaryDirectory() as out:
        try:
            subprocess.run([soffice, f'-env:UserInstallation={profile.as_uri()}', '--headless',
                            '--convert-to', target, '--outdir', out, str(path)],
                           capture_output=True, timeout=180)
        except Exception:
            return ''
        made = Path(out) / (path.stem + '.' + target)
        if not made.exists():
            return ''
        return {'docx': extract_docx, 'pptx': extract_pptx, 'xlsx': extract_xlsx}[target](made)


def extract_pptx(path: Path) -> str:
    return _ooxml_text(path, r'ppt/slides/slide\d+\.xml', ('</a:p>', '</a:t>'))


def extract_xlsx(path: Path) -> str:
    # Shared strings hold most cell text; sheet XML holds the rest.
    return _ooxml_text(path, r'xl/(sharedStrings|worksheets/sheet\d+)\.xml', ('</si>', '</row>', '</c>'))


def gateway_key() -> str:
    key = os.environ.get('PW_API_KEY', '')
    if key:
        return key
    try:
        import json
        import urllib.parse
        host = urllib.parse.urlparse(os.environ.get('PW_GATEWAY_URL', 'https://activate.parallel.works/api/openai/v1')).netloc
        creds = json.load(open(os.path.expanduser('~/.config/pw/credentials')))
        for ident in creds.get('identities', {}).values():
            if ident.get('server') == host:
                return ident.get('token') or ident.get('apikey') or ''
    except Exception:
        pass
    return ''


def vision_model() -> str:
    return os.environ.get('ADE_VISION_MODEL', '')


def caption_image(path: Path) -> str | None:
    """Describe an image with a vision model through the gateway's streaming
    Responses API. Opt-in via ADE_VISION_MODEL. Returns '' when captioning
    is off or the image is too large, and None when the request failed, so
    the caller can tell a picture with nothing to say from one that was
    never described."""
    import base64
    import json
    import urllib.request
    model = vision_model()
    key = gateway_key()
    if not model or not key or path.stat().st_size > MAX_CAPTION_BYTES:
        return ''
    mime = 'image/jpeg' if path.suffix.lower() in ('.jpg', '.jpeg') else f'image/{path.suffix.lower().lstrip(".")}'
    img = base64.b64encode(path.read_bytes()).decode()
    body = json.dumps({
        'model': model,
        'stream': True,
        'input': [{'role': 'user', 'content': [
            {'type': 'input_text', 'text': 'Describe this image in two to four factual sentences for a search index. Then transcribe any visible text labels.'},
            {'type': 'input_image', 'image_url': f'data:{mime};base64,{img}'}]}],
    }).encode()
    base = os.environ.get('PW_GATEWAY_URL', 'https://activate.parallel.works/api/openai/v1').rstrip('/')
    req = urllib.request.Request(f'{base}/responses', data=body,
                                 headers={'Authorization': f'Bearer {key}', 'Content-Type': 'application/json'})
    try:
        text = ''
        with urllib.request.urlopen(req, timeout=180) as resp:
            for raw in resp:
                line = raw.decode(errors='replace').strip()
                if not line.startswith('data:'):
                    continue
                data = line[5:].strip()
                if data == '[DONE]':
                    break
                try:
                    ev = json.loads(data)
                except Exception:
                    continue
                if ev.get('type') == 'response.output_text.delta':
                    text += ev.get('delta', '')
        return text.strip()
    except Exception as exc:
        print(f'  caption failed {path}: {str(exc)[:200]}', file=sys.stderr)
        return None


def extract_image_status(path: Path) -> tuple[str, bool]:
    """The image's text, and whether it is complete enough to cache: False
    when a vision model is configured and the caption request failed, so the
    next pass asks again rather than keeping an empty answer for good."""
    # Icons and tiny assets are skipped, except in the chat-attachments
    # directory where the user attached the image deliberately.
    deliberate = path.parent.name == 'chat' and path.parent.parent.name == 'uploads'
    if not deliberate and path.stat().st_size < MIN_IMAGE_BYTES:
        return '', True
    parts = []
    caption = caption_image(path)
    if caption:
        parts.append(f'Image description:\n{caption}')
    if OPTIONS['ocr']:
        try:
            ocr = subprocess.run(['tesseract', str(path), 'stdout', '--psm', '3'],
                                 capture_output=True, text=True, timeout=120)
            ocr_text = (ocr.stdout or '').strip()
            if len(ocr_text) > 20:
                parts.append(f'Text found in image (OCR):\n{ocr_text}')
        except Exception:
            pass
    return '\n\n'.join(parts), caption is not None


def extract_image(path: Path) -> str:
    return extract_image_status(path)[0]


def image_cache_usable(text: str) -> bool:
    """A cached image result is reused unless it is empty while a vision
    model is configured: a successful caption is never empty, so that entry
    was written by a failed request or before captioning was switched on."""
    return bool(text.strip()) or not vision_model()


def extract(path: Path) -> str | None:
    suffix = path.suffix.lower()
    try:
        if suffix in TEXT_SUFFIXES:
            return path.read_text(errors='replace')[:MAX_TEXT]
        if suffix == '.docx':
            return extract_docx(path)[:MAX_TEXT]
        if suffix == '.pdf':
            return extract_pdf(path)[:MAX_TEXT]
        if suffix == '.pptx':
            return extract_pptx(path)[:MAX_TEXT]
        if suffix == '.xlsx':
            return extract_xlsx(path)[:MAX_TEXT]
        if suffix in LEGACY_SUFFIXES:
            return extract_legacy(path)[:MAX_TEXT]
        if suffix in IMAGE_SUFFIXES:
            return extract_image(path)[:MAX_TEXT]
    except Exception as exc:
        print(f'  extract failed {path}: {exc}', file=sys.stderr)
    return None


def ensure_words_table(db: sqlite3.Connection) -> None:
    db.execute('DROP TABLE IF EXISTS words')
    db.execute("CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)")


def write_cache(cache_file: Path, text: str) -> bool:
    """Write a cache entry whole or not at all (a temporary file renamed into
    place), so a pass stopped part way never leaves half an entry that a
    later pass would take as current. A cache that cannot be written (read
    only or full shared storage) must not abort the pass: the text is still
    indexed, and the next pass retries the write."""
    try:
        cache_file.parent.mkdir(parents=True, exist_ok=True)
        tmp = cache_file.with_name(f'{cache_file.name}.{os.getpid()}.tmp')
        tmp.write_text(text)
        os.replace(tmp, cache_file)
        return True
    except OSError as exc:
        print(f'  cache write failed {cache_file}: {exc}', file=sys.stderr)
        return False


def extract_task(task: tuple) -> tuple:
    """One file's extraction, in a worker process. Writes the cache entry
    and returns (key, text) with text only when it could not be cached and
    is still worth indexing in this pass, else (key, None)."""
    key, fpath, cache_file, suffix, kind = task
    fpath, cache_file = Path(fpath), Path(cache_file)
    if kind == 'recheck':
        # downmark's thin policy OCRs scans itself; a thin cache entry
        # without its marker means the host had no tesseract when it was
        # written, so retry here and keep the result.
        try:
            text = cache_file.read_text(errors='replace')[:MAX_TEXT]
            thick = ocr_if_thin(fpath, text)[:MAX_TEXT]
            if thick != text:
                write_cache(cache_file, thick)
        except Exception as exc:
            print(f'  extract failed {fpath}: {exc}', file=sys.stderr)
        return key, None
    complete = True
    if suffix in IMAGE_SUFFIXES:
        try:
            text, complete = extract_image_status(fpath)
            text = text[:MAX_TEXT]
        except Exception as exc:
            print(f'  extract failed {fpath}: {exc}', file=sys.stderr)
            text, complete = None, False
    else:
        text = extract(fpath)
    # Images cache even when empty: captioning and OCR are expensive and a
    # picture with no text is a real answer, unless the caption request
    # failed, which the next pass retries. Documents do not cache empty: an
    # empty result usually means the extractor was missing, and caching it
    # hid the file from every later pass, including the one after the
    # library was installed.
    if complete and ((text and text.strip()) or suffix in IMAGE_SUFFIXES):
        if write_cache(cache_file, text or ''):
            return key, None
    return key, (text if text and text.strip() else None)


def worker_init(options: dict) -> None:
    OPTIONS.update(options)
    # Many tesseract processes side by side each run best on one thread;
    # left alone, each starts one per core and they trip over each other.
    os.environ['OMP_THREAD_LIMIT'] = '1'


class Progress:
    """`@progress` lines for the server, at most a few a second."""

    def __init__(self, on: bool, phase: str):
        self.on, self.phase, self.last = on, phase, 0.0
        self.lock = threading.Lock()
        self.done, self.total, self.step = 0, 0, ''

    def emit(self, force: bool = False) -> None:
        if not self.on:
            return
        now = time.monotonic()
        if force or now - self.last >= 0.25:
            self.last = now
            print('@progress ' + json.dumps({'phase': self.phase, 'step': self.step, 'done': self.done, 'total': self.total}), flush=True)

    def start(self, step: str, total: int) -> None:
        with self.lock:
            self.step, self.total = step, total
            self.emit(True)

    def add(self, n: int = 1) -> None:
        with self.lock:
            self.done += n
            self.emit()


def default_jobs() -> int:
    try:
        return max(1, len(os.sched_getaffinity(0)))
    except AttributeError:
        return max(1, os.cpu_count() or 1)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--kb-root', required=True)
    ap.add_argument('--index', required=True, help='GUFI index tree top (mirrors kb-root)')
    ap.add_argument('--extract-cache', required=True)
    ap.add_argument('--subdir', default='', help='restrict to this KB-relative subtree (incremental indexing)')
    ap.add_argument('--no-recurse', action='store_true', help='process only the subdir itself, not its children')
    ap.add_argument('--jobs', type=int, default=0, help='worker processes for extraction (default: the CPU count)')
    ap.add_argument('--progress', action='store_true', help='print @progress lines on stdout')
    ap.add_argument('--no-ocr', action='store_true', help='do not run tesseract on images or scanned PDFs')
    ap.add_argument('--pdf-reader', choices=('auto', 'pdftotext', 'pypdf'), default='auto',
                    help='how PDFs downmark did not convert are read')
    ap.add_argument('--soffice', default='', help='LibreOffice soffice, for .ppt, .xls, .doc and OpenDocument files')
    ap.add_argument('--no-documents', action='store_true', help='index PDFs and Office documents by name only')
    args = ap.parse_args()

    jobs = args.jobs if args.jobs > 0 else default_jobs()
    options = {
        'ocr': not args.no_ocr and shutil.which('tesseract') is not None,
        'pdf_reader': args.pdf_reader,
        'soffice': args.soffice if args.soffice and os.path.exists(args.soffice) else '',
    }
    OPTIONS.update(options)
    # An image yields text only through OCR or a caption; with neither,
    # reading it costs a process and gives nothing.
    images_useful = options['ocr'] or bool(vision_model() and gateway_key())
    legacy = set(LEGACY_SUFFIXES) if options['soffice'] and not args.no_documents else set()
    documents = set() if args.no_documents else EXTRACT_SUFFIXES

    kb_root = Path(args.kb_root)
    index_root = Path(args.index)
    cache_root = Path(args.extract_cache)
    walk_root = index_root / args.subdir if args.subdir else index_root
    progress = Progress(args.progress, 'text')

    # Plan: every directory db, its files, and what each needs. Walk the
    # index tree, not the source tree: every db.db the index holds must carry
    # the words table (empty or not) so MATCH queries never error.
    dirs = []    # (dbfile, [(name, suffix, ino, source)]) with source 'text', 'cache' or a task key
    tasks = []
    for dirpath, dirnames, _filenames in os.walk(walk_root):
        if args.no_recurse:
            dirnames[:] = []
        dirnames.sort()
        dbfile = Path(dirpath) / 'db.db'
        if not dbfile.exists():
            continue
        rel_dir = Path(dirpath).relative_to(index_root)
        src_dir = kb_root / rel_dir
        files = []
        if src_dir.is_dir() and not any(part in EXCLUDE_DIRS for part in rel_dir.parts):
            try:
                entries = sorted(os.scandir(src_dir), key=lambda e: e.name)
            except OSError:
                entries = []
            for e in entries:
                try:
                    if not e.is_file():
                        continue
                    st = e.stat()
                except OSError:
                    continue
                suffix = os.path.splitext(e.name)[1].lower()
                image = suffix in IMAGE_SUFFIXES
                cacheable = suffix in documents or suffix in legacy or (image and images_useful)
                if suffix not in TEXT_SUFFIXES and not cacheable:
                    continue
                if suffix in TEXT_SUFFIXES:
                    files.append((e.name, suffix, st.st_ino, 'text'))
                    continue
                # Expensive extractions (PDF, office, OCR, captions) are
                # cached by mtime; a reindex pass reuses them untouched.
                cache_file = cache_root / rel_dir / (e.name + '.txt')
                fresh = False
                try:
                    fresh = cache_file.stat().st_mtime >= st.st_mtime
                except OSError:
                    pass
                key = len(tasks)
                if fresh and image and vision_model():
                    # A cached result is reused unless it is empty while a
                    # vision model is configured (image_cache_usable).
                    try:
                        fresh = cache_file.stat().st_size > 0
                    except OSError:
                        fresh = False
                if fresh:
                    files.append((e.name, suffix, st.st_ino, 'cache'))
                    # An entry of more bytes than a UTF-8 text of the OCR
                    # ceiling can take is never thin; only shorter ones are
                    # read again to see.
                    if suffix == '.pdf' and options['ocr'] and cache_file.stat().st_size < OCR_TEXT_CEILING * 4:
                        tasks.append((key, str(src_dir / e.name), str(cache_file), suffix, 'recheck'))
                    continue
                tasks.append((key, str(src_dir / e.name), str(cache_file), suffix, 'extract'))
                files.append((e.name, suffix, st.st_ino, key))
        dirs.append((dbfile, rel_dir, files))

    progress.start('extract', len(tasks) + len(dirs))
    texts: dict = {}
    n_extracted = 0
    if tasks:
        # Few files are not worth starting processes for.
        if jobs == 1 or len(tasks) < 4:
            for t in tasks:
                key, text = extract_task(t)
                if text is not None:
                    texts[key] = text
                n_extracted += t[4] == 'extract'
                progress.add()
        else:
            from concurrent.futures import ProcessPoolExecutor, as_completed
            with ProcessPoolExecutor(max_workers=min(jobs, len(tasks)), initializer=worker_init, initargs=(options,)) as pool:
                futures = [pool.submit(extract_task, t) for t in tasks]
                for fut in as_completed(futures):
                    try:
                        key, text = fut.result()
                        if text is not None:
                            texts[key] = text
                    except Exception as exc:
                        print(f'  extract failed: {exc}', file=sys.stderr)
                    progress.add()
            n_extracted = sum(1 for t in tasks if t[4] == 'extract')

    counts = {'files': 0}
    count_lock = threading.Lock()

    def write_dir(item) -> None:
        dbfile, rel_dir, files = item
        rows = []
        for name, suffix, ino, source in files:
            text = None
            try:
                if source == 'text':
                    with open(kb_root / rel_dir / name, 'r', errors='replace') as fh:
                        text = fh.read(MAX_TEXT)
                elif isinstance(source, int) and source in texts:
                    text = texts[source]
                else:
                    cache_file = cache_root / rel_dir / (name + '.txt')
                    if cache_file.exists():
                        text = cache_file.read_text(errors='replace')[:MAX_TEXT]
                        if suffix in IMAGE_SUFFIXES and not image_cache_usable(text):
                            text = None
            except OSError:
                text = None
            if not text or not text.strip():
                continue
            # Inodes go in as text. Some parallel filesystems issue numbers
            # past SQLite's signed 64-bit range, which raised OverflowError and
            # failed the whole indexing pass; every consumer joins on
            # CAST(tinode AS TEXT) anyway.
            rows.append((str(ino), name, text))
        db = sqlite3.connect(str(dbfile), timeout=60)
        try:
            # The index is rebuilt from the files at will, so a crash of the
            # machine mid-write costs a rebuild, not data; waiting for the
            # disk on every directory's commit made this pass take minutes.
            db.execute('PRAGMA synchronous=OFF')
            ensure_words_table(db)
            db.executemany('INSERT INTO words (tinode, fname, wordf) VALUES (?, ?, ?)', rows)
            db.commit()
        finally:
            db.close()
        with count_lock:
            counts['files'] += len(rows)
        progress.add()

    progress.step = 'write'
    if jobs == 1 or len(dirs) < 8:
        for d in dirs:
            write_dir(d)
    else:
        from concurrent.futures import ThreadPoolExecutor
        with ThreadPoolExecutor(max_workers=min(jobs, 8)) as pool:
            for _ in pool.map(write_dir, dirs):
                pass
    progress.emit(True)

    print(f'enriched {len(dirs)} directories: {counts["files"]} files indexed, {n_extracted} extract-cache entries')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
