#!/usr/bin/env python3
"""Enrich a GUFI index of the knowledge base with extracted text.

For every real-content file, extract plain text and store it in an fts5
`words` table inside the per-directory db.db of the GUFI index (joined to
entries by inode, per the GUFI master document pattern). DOCX/PDF/PPTX/XLSX
text is also written to an on-disk extract cache the server uses for
previews. Every directory db gets the `words` table, empty or not, so
MATCH queries never hit a missing table.

Office documents normally arrive already converted: the server (and
reindex.sh) run `server/dist/preextract.js`, which turns .docx/.pptx/.xlsx/
.doc into Markdown through downmark and writes the cache entries this script
reuses. The standard-library OOXML reader below is the last resort for a
document that pass could not read; PDFs, OCR and image captions live here.
"""
import argparse
import os
import sqlite3
import subprocess
import sys
from pathlib import Path

EXCLUDE_DIRS = {
    '.git', 'node_modules', '.venv', 'venv', '__pycache__', 'dist', 'build',
    '.cache', '.pytest_cache', 'screenshots', '.ipynb_checkpoints',
}
TEXT_SUFFIXES = {
    '.md', '.txt', '.py', '.sh', '.yaml', '.yml', '.json', '.csv', '.tsv', '.xml',
    '.html', '.css', '.js', '.ts', '.tsx', '.svg', '.toml', '.cfg', '.ini', '.def', '.mjs', '.sql',
}
# .doc has no reader here; it indexes only through the pre-extracted cache.
EXTRACT_SUFFIXES = {'.pdf', '.docx', '.pptx', '.xlsx', '.doc'}
IMAGE_SUFFIXES = {'.png', '.jpg', '.jpeg', '.gif', '.webp'}
MAX_TEXT = 500_000
MAX_CAPTION_BYTES = 8 * 1024 * 1024
MIN_IMAGE_BYTES = 8 * 1024  # skip icons and tiny assets


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
    return 1


def _pdf_ocr(path: Path, pages: int) -> str:
    """OCR a scanned PDF, page by page, up to a sane limit.

    Signed and faxed forms carry their text as an image, so pdftotext
    returns a page number and nothing else. Rendering to PNG and running
    tesseract is slow, so it only happens when the text layer is thin.
    """
    import shutil
    import tempfile
    if not (shutil.which('pdftoppm') and shutil.which('tesseract')):
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
    return f'Text read from scanned pages (OCR):\n{body}' if body else ''


def extract_pdf(path: Path) -> str:
    text = ''
    try:
        # -layout keeps columns and table cells apart; without it, rows run
        # together into one line and read badly in a snippet.
        out = subprocess.run(['pdftotext', '-layout', '-q', str(path), '-'],
                             capture_output=True, text=True, timeout=180)
        if out.returncode == 0:
            text = out.stdout or ''
    except FileNotFoundError:
        pass
    if not text.strip():
        try:
            from pypdf import PdfReader  # type: ignore
            reader = PdfReader(str(path))
            text = '\n'.join((page.extract_text() or '') for page in reader.pages)
        except Exception:
            text = ''
    pages = _pdf_page_count(path)
    if len(text.strip()) < OCR_CHARS_PER_PAGE * pages:
        ocr = _pdf_ocr(path, pages)
        if ocr:
            # Keep both: the text layer may hold a header the scan blurs.
            return f'{text}\n\n{ocr}' if text.strip() else ocr
    return text


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


def caption_image(path: Path) -> str:
    """Describe an image with a vision model through the gateway's streaming
    Responses API. Opt-in via ADE_VISION_MODEL; failures degrade to OCR-only."""
    import base64
    import json
    import urllib.request
    model = os.environ.get('ADE_VISION_MODEL', '')
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
        print(f'  caption failed {path}: {str(exc)[:120]}', file=sys.stderr)
        return ''


def extract_image(path: Path) -> str:
    # Icons and tiny assets are skipped, except in the chat-attachments
    # directory where the user attached the image deliberately.
    deliberate = path.parent.name == 'chat' and path.parent.parent.name == 'uploads'
    if not deliberate and path.stat().st_size < MIN_IMAGE_BYTES:
        return ''
    parts = []
    caption = caption_image(path)
    if caption:
        parts.append(f'Image description:\n{caption}')
    try:
        ocr = subprocess.run(['tesseract', str(path), 'stdout', '--psm', '3'],
                             capture_output=True, text=True, timeout=120)
        ocr_text = (ocr.stdout or '').strip()
        if len(ocr_text) > 20:
            parts.append(f'Text found in image (OCR):\n{ocr_text}')
    except Exception:
        pass
    return '\n\n'.join(parts)


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
        if suffix in IMAGE_SUFFIXES:
            return extract_image(path)[:MAX_TEXT]
    except Exception as exc:
        print(f'  extract failed {path}: {exc}', file=sys.stderr)
    return None


def ensure_words_table(db: sqlite3.Connection) -> None:
    db.execute('DROP TABLE IF EXISTS words')
    db.execute("CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--kb-root', required=True)
    ap.add_argument('--index', required=True, help='GUFI index tree top (mirrors kb-root)')
    ap.add_argument('--extract-cache', required=True)
    ap.add_argument('--subdir', default='', help='restrict to this KB-relative subtree (incremental indexing)')
    ap.add_argument('--no-recurse', action='store_true', help='process only the subdir itself, not its children')
    args = ap.parse_args()

    kb_root = Path(args.kb_root)
    index_root = Path(args.index)
    cache_root = Path(args.extract_cache)
    walk_root = index_root / args.subdir if args.subdir else index_root

    n_files = n_dirs = n_extracted = 0
    # Walk the index tree, not the source tree: every db.db the index holds
    # must carry the words table (empty or not) so MATCH queries never error.
    for dirpath, dirnames, _filenames in os.walk(walk_root):
        if args.no_recurse:
            dirnames[:] = []
        dirnames.sort()
        dbfile = Path(dirpath) / 'db.db'
        if not dbfile.exists():
            continue
        rel_dir = Path(dirpath).relative_to(index_root)
        src_dir = kb_root / rel_dir
        n_dirs += 1
        db = sqlite3.connect(str(dbfile))
        try:
            ensure_words_table(db)
            rows = []
            if src_dir.is_dir() and not any(part in EXCLUDE_DIRS for part in rel_dir.parts):
                for fpath in sorted(src_dir.iterdir()):
                    if not fpath.is_file():
                        continue
                    suffix = fpath.suffix.lower()
                    cacheable = suffix in EXTRACT_SUFFIXES or suffix in IMAGE_SUFFIXES
                    if suffix not in TEXT_SUFFIXES and not cacheable:
                        continue
                    # Expensive extractions (PDF, office, OCR, captions) are
                    # cached by mtime; a reindex pass reuses them untouched.
                    cache_file = cache_root / rel_dir / (fpath.name + '.txt')
                    if cacheable and cache_file.exists() and cache_file.stat().st_mtime >= fpath.stat().st_mtime:
                        text = cache_file.read_text(errors='replace')
                    else:
                        text = extract(fpath)
                        # Images cache even when empty: captioning and OCR are
                        # expensive and a picture with no text is a real
                        # answer. Documents do not: an empty result usually
                        # means the extractor was missing, and caching it hid
                        # the file from every later pass, including the one
                        # after the library was installed.
                        if cacheable and ((text and text.strip()) or suffix in IMAGE_SUFFIXES):
                            cache_file.parent.mkdir(parents=True, exist_ok=True)
                            cache_file.write_text(text or '')
                            n_extracted += 1
                    if not text or not text.strip():
                        continue
                    # Inodes go in as text. Some parallel filesystems
                    # issue numbers past SQLite's signed 64-bit range,
                    # which raised OverflowError
                    # and failed the whole indexing pass; every consumer joins
                    # on CAST(tinode AS TEXT) anyway.
                    rows.append((str(fpath.stat().st_ino), fpath.name, text))
                    n_files += 1
            db.executemany('INSERT INTO words (tinode, fname, wordf) VALUES (?, ?, ?)', rows)
            db.commit()
        finally:
            db.close()

    print(f'enriched {n_dirs} directories: {n_files} files indexed, {n_extracted} extract-cache entries')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
