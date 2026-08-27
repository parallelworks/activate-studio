#!/usr/bin/env python3
"""Generate the extraction test corpus under testdata/corpus.

The fixtures are committed, so this script only runs when the corpus needs
to change. Everything is synthesised here rather than copied from a real
document set: the corpus carries no licence of its own, every file is a few
kilobytes, and the phrases the tests assert on are chosen to be unmistakable
("VELOCITY-INLET-7734" appears nowhere else on earth).

Generation is deterministic — fixed zip timestamps, no PDF /CreationDate —
so regenerating without content changes leaves the bytes identical and git
sees nothing. The scanned fixtures need poppler's pdftoppm at generation
time; the committed PDFs do not need it at test time.

    python3 testdata/make-corpus.py
"""
import shutil
import subprocess
import sys
import tempfile
import zipfile
import zlib
from pathlib import Path

ROOT = Path(__file__).resolve().parent / 'corpus'
ZIP_DATE = (2026, 1, 1, 0, 0, 0)

# Phrases the tests look for. Kept here so the fixtures and the expectations
# file are written from one source.
MARKER_DOCX = 'VELOCITY-INLET-7734'
MARKER_PPTX = 'MESH-REFINEMENT-2261'
MARKER_XLSX = 'REYNOLDS-CASE-8891'
MARKER_PDF = 'BOUNDARY-LAYER-4417'
MARKER_PDF_P2 = 'CONVERGENCE-HISTORY-5502'
MARKER_SCAN = 'SCANNED PAGE 9931'
MARKER_MIXED_HEAD = 'TYPED HEADER 6620'
MARKER_MIXED_SCAN = 'SCANNED BODY 7742'
MARKER_IMAGE = 'DIAGRAM LABEL 3308'


def write_zip(path: Path, members: dict[str, str]) -> None:
    """Write a zip with fixed timestamps so output is byte-stable."""
    path.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(path, 'w', zipfile.ZIP_DEFLATED) as z:
        for name, text in members.items():
            info = zipfile.ZipInfo(name, date_time=ZIP_DATE)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o600 << 16
            z.writestr(info, text)


CONTENT_TYPES_DOCX = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>'''

RELS_DOCX = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>'''


def para(text: str, style: str = '') -> str:
    pr = f'<w:pPr><w:pStyle w:val="{style}"/></w:pPr>' if style else ''
    return f'<w:p>{pr}<w:r><w:t xml:space="preserve">{text}</w:t></w:r></w:p>'


def make_docx() -> None:
    """A Word document with a heading, prose, and a table.

    Exercises what the flat-text readers used to drop: heading level and
    table structure both have to survive into the Markdown.
    """
    rows = [
        ['Region', 'Cell count', 'Notes'],
        ['inlet', '128400', MARKER_DOCX],
        ['wake', '256800', 'refined twice'],
    ]
    table_rows = ''.join(
        '<w:tr>' + ''.join(
            f'<w:tc><w:tcPr><w:tcW w:w="2400" w:type="dxa"/></w:tcPr>{para(cell)}</w:tc>'
            for cell in row
        ) + '</w:tr>'
        for row in rows
    )
    body = (
        para('Solver Handbook', 'Heading1')
        + para('The inlet condition is fixed for every case in this study.')
        + para('Mesh sizing', 'Heading2')
        + f'<w:tbl><w:tblPr><w:tblW w:w="7200" w:type="dxa"/></w:tblPr>{table_rows}</w:tbl>'
        + para('Cases that fail to converge are rerun with a smaller step.')
    )
    document = f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>{body}<w:sectPr/></w:body></w:document>'''
    write_zip(ROOT / 'docs' / 'handbook.docx', {
        '[Content_Types].xml': CONTENT_TYPES_DOCX,
        '_rels/.rels': RELS_DOCX,
        'word/document.xml': document,
    })


def make_xlsx() -> None:
    """A workbook with two sheets, so a per-sheet heading is observable."""
    sheets = {
        'Runs': [
            ['case', 'reynolds', 'status'],
            ['a-01', '4.2e5', 'converged'],
            ['a-02', '8.1e5', MARKER_XLSX],
        ],
        'Budget': [
            ['resource', 'hours'],
            ['cpu', '1840'],
            ['gpu', '96'],
        ],
    }
    shared: list[str] = []

    def sst_index(value: str) -> int:
        if value not in shared:
            shared.append(value)
        return shared.index(value)

    sheet_parts = {}
    for n, (name, rows) in enumerate(sheets.items(), start=1):
        xml_rows = []
        for r, row in enumerate(rows, start=1):
            cells = ''.join(
                f'<c r="{chr(64 + c)}{r}" t="s"><v>{sst_index(v)}</v></c>'
                for c, v in enumerate(row, start=1)
            )
            xml_rows.append(f'<row r="{r}">{cells}</row>')
        sheet_parts[f'xl/worksheets/sheet{n}.xml'] = (
            '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">'
            f'<sheetData>{"".join(xml_rows)}</sheetData></worksheet>'
        )

    sst_items = ''.join(f'<si><t xml:space="preserve">{v}</t></si>' for v in shared)
    workbook_sheets = ''.join(
        f'<sheet name="{name}" sheetId="{n}" r:id="rId{n}"/>'
        for n, name in enumerate(sheets, start=1)
    )
    wb_rels = ''.join(
        f'<Relationship Id="rId{n}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet{n}.xml"/>'
        for n in range(1, len(sheets) + 1)
    )
    members = {
        '[Content_Types].xml': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
        '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
        '<Default Extension="xml" ContentType="application/xml"/>'
        '<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>'
        + ''.join(
            f'<Override PartName="/xl/worksheets/sheet{n}.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>'
            for n in range(1, len(sheets) + 1))
        + '<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>'
        '</Types>',
        '_rels/.rels': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>'
        '</Relationships>',
        'xl/workbook.xml': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" '
        'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">'
        f'<sheets>{workbook_sheets}</sheets></workbook>',
        'xl/_rels/workbook.xml.rels': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        f'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">{wb_rels}</Relationships>',
        'xl/sharedStrings.xml': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        f'<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="{len(shared)}" uniqueCount="{len(shared)}">{sst_items}</sst>',
    }
    members.update(sheet_parts)
    write_zip(ROOT / 'docs' / 'quarterly.xlsx', members)


def make_pptx() -> None:
    """Slides with a title, bullets, a table, and speaker notes.

    Tables, notes and slide boundaries are exactly what the old flat-text
    reader dropped, so each one is asserted on.
    """
    def text_shape(shape_id: int, name: str, text_lines: list[str], x: int, y: int, cx: int, cy: int) -> str:
        paras = ''.join(
            f'<a:p><a:r><a:rPr lang="en-US"/><a:t>{line}</a:t></a:r></a:p>' for line in text_lines)
        return f'''<p:sp><p:nvSpPr><p:cNvPr id="{shape_id}" name="{name}"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="{x}" y="{y}"/><a:ext cx="{cx}" cy="{cy}"/></a:xfrm></p:spPr>
<p:txBody><a:bodyPr/><a:lstStyle/>{paras}</p:txBody></p:sp>'''

    def table_shape(shape_id: int, rows: list[list[str]]) -> str:
        grid = ''.join('<a:gridCol w="2000000"/>' for _ in rows[0])
        trs = ''.join(
            '<a:tr h="370000">' + ''.join(
                f'<a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="en-US"/><a:t>{c}</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc>'
                for c in row) + '</a:tr>'
            for row in rows)
        return f'''<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="{shape_id}" name="Table"/><p:cNvGraphicFramePr/><p:nvPr/></p:nvGraphicFramePr>
<p:xfrm><a:off x="500000" y="2000000"/><a:ext cx="6000000" cy="1500000"/></p:xfrm>
<a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table">
<a:tbl><a:tblPr/><a:tblGrid>{grid}</a:tblGrid>{trs}</a:tbl></a:graphicData></a:graphic></p:graphicFrame>'''

    def slide(shapes: str) -> str:
        return f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/>{shapes}</p:spTree></p:cSld></p:sld>'''

    slide1 = slide(
        text_shape(2, 'Title 1', ['Kickoff Review'], 500000, 300000, 6000000, 1000000)
        + text_shape(3, 'Content 2', [
            'Three cases are queued for this week.',
            f'Tracking code {MARKER_PPTX} covers the refinement pass.',
        ], 500000, 1600000, 6000000, 2000000))
    slide2 = slide(
        text_shape(2, 'Title 1', ['Mesh budget'], 500000, 300000, 6000000, 1000000)
        + table_shape(4, [
            ['stage', 'cells'],
            ['baseline', '1.2M'],
            ['refined', '4.8M'],
        ]))

    notes = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:notes xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/>
<p:sp><p:nvSpPr><p:cNvPr id="2" name="Notes Placeholder"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="body" idx="1"/></p:nvPr></p:nvSpPr>
<p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="en-US"/><a:t>Speaker note: mention the rerun policy.</a:t></a:r></a:p></p:txBody></p:sp>
</p:spTree></p:cSld></p:notes>'''

    write_zip(ROOT / 'docs' / 'kickoff.pptx', {
        '[Content_Types].xml': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
        '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
        '<Default Extension="xml" ContentType="application/xml"/>'
        '<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>'
        '<Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>'
        '<Override PartName="/ppt/slides/slide2.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>'
        '<Override PartName="/ppt/notesSlides/notesSlide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesSlide+xml"/>'
        '</Types>',
        '_rels/.rels': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>'
        '</Relationships>',
        'ppt/presentation.xml': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" '
        'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" '
        'xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">'
        '<p:sldIdLst><p:sldId id="256" r:id="rId1"/><p:sldId id="257" r:id="rId2"/></p:sldIdLst>'
        '<p:sldSz cx="9144000" cy="6858000"/><p:notesSz cx="6858000" cy="9144000"/></p:presentation>',
        'ppt/_rels/presentation.xml.rels': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>'
        '<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/>'
        '</Relationships>',
        'ppt/slides/slide1.xml': slide1,
        'ppt/slides/slide2.xml': slide2,
        'ppt/slides/_rels/slide1.xml.rels': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/>'
        '</Relationships>',
        'ppt/slides/_rels/slide2.xml.rels': '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>',
        'ppt/notesSlides/notesSlide1.xml': notes,
    })


# ---------------------------------------------------------------- PDF ------
# Written by hand rather than through a library: the app has no PDF writer,
# and a few hundred bytes of PDF syntax is cheaper than a build dependency.

def pdf_document(pages: list[str], extra_objects: str = '', page_extra: str = '') -> bytes:
    """Assemble a PDF from per-page content streams (Helvetica text)."""
    objects: list[bytes] = []

    def add(body: str) -> int:
        objects.append(body.encode('latin-1'))
        return len(objects)

    font_id = add('<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>')
    page_ids: list[int] = []
    content_ids: list[int] = []
    for stream in pages:
        data = stream.encode('latin-1')
        content_ids.append(add(f'<< /Length {len(data)} >>\nstream\n{stream}\nendstream'))
    pages_id = len(objects) + len(pages) + 1
    for content_id in content_ids:
        page_ids.append(add(
            f'<< /Type /Page /Parent {pages_id} 0 R /MediaBox [0 0 612 792] '
            f'/Resources << /Font << /F1 {font_id} 0 R >> {page_extra} >> /Contents {content_id} 0 R >>'))
    kids = ' '.join(f'{pid} 0 R' for pid in page_ids)
    add(f'<< /Type /Pages /Kids [{kids}] /Count {len(page_ids)} >>')
    catalog_id = add(f'<< /Type /Catalog /Pages {pages_id} 0 R >>')

    out = bytearray(b'%PDF-1.4\n')
    offsets = [0]
    for n, body in enumerate(objects, start=1):
        offsets.append(len(out))
        out += f'{n} 0 obj\n'.encode() + body + b'\nendobj\n'
    xref_at = len(out)
    out += f'xref\n0 {len(objects) + 1}\n'.encode()
    out += b'0000000000 65535 f \n'
    for off in offsets[1:]:
        out += f'{off:010d} 00000 n \n'.encode()
    # No /ID and no /CreationDate: the bytes must not change between runs.
    out += f'trailer\n<< /Size {len(objects) + 1} /Root {catalog_id} 0 R >>\nstartxref\n{xref_at}\n%%EOF\n'.encode()
    return bytes(out)


def text_stream(lines: list[tuple[int, int, int, str]]) -> str:
    parts = ['BT']
    for size, x, y, text in lines:
        escaped = text.replace('\\', r'\\').replace('(', r'\(').replace(')', r'\)')
        parts.append(f'/F1 {size} Tf 1 0 0 1 {x} {y} Tm ({escaped}) Tj')
    parts.append('ET')
    return '\n'.join(parts)


def make_text_pdf() -> Path:
    """A two-page typeset PDF: the ordinary, no-OCR-needed case."""
    page1 = text_stream([
        (20, 72, 720, 'Boundary Layer Report'),
        (12, 72, 690, 'Case study for the inlet refinement pass.'),
        (12, 72, 670, f'Reference code {MARKER_PDF} identifies this run.'),
        (12, 72, 650, 'Each case is rerun when residuals stall.'),
    ])
    page2 = text_stream([
        (16, 72, 720, 'Appendix A'),
        (12, 72, 690, f'Convergence marker {MARKER_PDF_P2} closes the set.'),
        (12, 72, 670, 'No further passes were required.'),
    ])
    path = ROOT / 'docs' / 'report.pdf'
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(pdf_document([page1, page2]))
    return path


def jpeg_page_pdf(jpegs: list[tuple[bytes, int, int]], typed_first_page: str | None = None) -> bytes:
    """A PDF whose pages are full-page JPEG images.

    JPEG rather than raw samples because DCTDecode takes the encoded bytes
    as they are: no PNG decoding, no zlib round-trip, and a fixture that
    stays a few kilobytes.
    """
    objects: list[bytes] = []

    def add_raw(body: bytes) -> int:
        objects.append(body)
        return len(objects)

    def add(body: str) -> int:
        return add_raw(body.encode('latin-1'))

    font_id = add('<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>')
    page_ids: list[int] = []
    page_specs: list[tuple[int, int, str]] = []  # image id, content id, resources
    for data, w, h in jpegs:
        img_id = add_raw(
            f'<< /Type /XObject /Subtype /Image /Width {w} /Height {h} /ColorSpace /DeviceGray '
            f'/BitsPerComponent 8 /Filter /DCTDecode /Length {len(data)} >>\nstream\n'.encode('latin-1')
            + data + b'\nendstream')
        stream = 'q\n612 0 0 792 0 0 cm\n/Im0 Do\nQ'
        if typed_first_page and not page_specs:
            stream = (f'BT /F1 14 Tf 1 0 0 1 60 750 Tm ({typed_first_page}) Tj ET\n'
                      'q\n612 0 0 700 0 0 cm\n/Im0 Do\nQ')
        content_id = add(f'<< /Length {len(stream)} >>\nstream\n{stream}\nendstream')
        page_specs.append((img_id, content_id, ''))

    pages_id = len(objects) + len(page_specs) + 1
    for img_id, content_id, _ in page_specs:
        page_ids.append(add(
            f'<< /Type /Page /Parent {pages_id} 0 R /MediaBox [0 0 612 792] '
            f'/Resources << /XObject << /Im0 {img_id} 0 R >> /Font << /F1 {font_id} 0 R >> >> '
            f'/Contents {content_id} 0 R >>'))
    kids = ' '.join(f'{pid} 0 R' for pid in page_ids)
    add(f'<< /Type /Pages /Kids [{kids}] /Count {len(page_ids)} >>')
    catalog_id = add(f'<< /Type /Catalog /Pages {pages_id} 0 R >>')

    out = bytearray(b'%PDF-1.4\n')
    offsets = [0]
    for n, body in enumerate(objects, start=1):
        offsets.append(len(out))
        out += f'{n} 0 obj\n'.encode() + body + b'\nendobj\n'
    xref_at = len(out)
    out += f'xref\n0 {len(objects) + 1}\n'.encode()
    out += b'0000000000 65535 f \n'
    for off in offsets[1:]:
        out += f'{off:010d} 00000 n \n'.encode()
    out += f'trailer\n<< /Size {len(objects) + 1} /Root {catalog_id} 0 R >>\nstartxref\n{xref_at}\n%%EOF\n'.encode()
    return bytes(out)


def rasterise(pdf: Path, dpi: int = 110) -> list[tuple[bytes, int, int]]:
    """Render a PDF's pages to greyscale JPEG through poppler."""
    if not shutil.which('pdftoppm'):
        sys.exit('pdftoppm (poppler-utils) is needed to regenerate the scanned fixtures')
    out = []
    with tempfile.TemporaryDirectory() as tmp:
        prefix = str(Path(tmp) / 'page')
        subprocess.run(['pdftoppm', '-r', str(dpi), '-gray', '-jpeg', '-jpegopt', 'quality=55',
                        pdf.name and str(pdf), prefix], check=True, capture_output=True)
        for jpg in sorted(Path(tmp).glob('page*.jpg')):
            data = jpg.read_bytes()
            w, h = jpeg_size(data)
            out.append((data, w, h))
    return out


def jpeg_size(data: bytes) -> tuple[int, int]:
    i = 2
    while i < len(data):
        if data[i] != 0xFF:
            i += 1
            continue
        marker = data[i + 1]
        if marker in (0xC0, 0xC1, 0xC2):
            h = int.from_bytes(data[i + 5:i + 7], 'big')
            w = int.from_bytes(data[i + 7:i + 9], 'big')
            return w, h
        if marker in (0xD8, 0xD9) or 0xD0 <= marker <= 0xD7:
            i += 2
            continue
        i += 2 + int.from_bytes(data[i + 2:i + 4], 'big')
    raise ValueError('no JPEG frame header')


def make_scans() -> None:
    """A fully scanned document, and one with a typed header over a scan.

    The second is the case downmark's "thin" OCR policy exists for: the
    page has real text, so a textless policy would skip its scanned body.
    """
    with tempfile.TemporaryDirectory() as tmp:
        src = Path(tmp) / 'src.pdf'

        src.write_bytes(pdf_document([text_stream([
            (22, 72, 700, 'INVOICE'),
            (14, 72, 660, f'Reference {MARKER_SCAN}'),
            (14, 72, 630, 'Total due on receipt.'),
            (14, 72, 600, 'Remit to the address above.'),
        ])]))
        (ROOT / 'scans').mkdir(parents=True, exist_ok=True)
        (ROOT / 'scans' / 'invoice-scan.pdf').write_bytes(jpeg_page_pdf(rasterise(src)))

        src.write_bytes(pdf_document([text_stream([
            (18, 72, 700, 'REQUEST FORM'),
            (14, 72, 660, f'Body reference {MARKER_MIXED_SCAN}'),
            (14, 72, 630, 'Signed and returned by the operator.'),
        ])]))
        (ROOT / 'scans' / 'form-mixed.pdf').write_bytes(
            jpeg_page_pdf(rasterise(src), typed_first_page=f'{MARKER_MIXED_HEAD} - filed 2026-01-04'))


def make_image() -> None:
    """A PNG carrying text, for the indexer's image OCR path."""
    with tempfile.TemporaryDirectory() as tmp:
        src = Path(tmp) / 'src.pdf'
        src.write_bytes(pdf_document([text_stream([
            (24, 60, 700, 'SCHEMATIC'),
            (18, 60, 660, MARKER_IMAGE),
            (18, 60, 620, 'inlet -> mesh -> solver'),
        ])]))
        if not shutil.which('pdftoppm'):
            sys.exit('pdftoppm (poppler-utils) is needed to regenerate the image fixture')
        prefix = str(Path(tmp) / 'img')
        subprocess.run(['pdftoppm', '-r', '80', '-gray', '-png', '-f', '1', '-l', '1', str(src), prefix],
                       check=True, capture_output=True)
        png = sorted(Path(tmp).glob('img*.png'))[0]
        target = ROOT / 'images' / 'diagram.png'
        target.parent.mkdir(parents=True, exist_ok=True)
        # Crop nothing, just carry it across; pdftoppm's PNG is deterministic.
        target.write_bytes(png.read_bytes())


def make_plain_files() -> None:
    """The formats enrich.py reads directly, which must keep working."""
    (ROOT / 'notes').mkdir(parents=True, exist_ok=True)
    (ROOT / 'notes' / 'readme.md').write_text(
        '# Study notes\n\n'
        'The refinement pass follows the handbook.\n\n'
        '- inlet fixed\n- wake refined\n\n'
        '| stage | cells |\n| --- | --- |\n| baseline | 1.2M |\n')
    (ROOT / 'notes' / 'runs.csv').write_text(
        'case,reynolds,status\na-01,4.2e5,converged\na-02,8.1e5,stalled\n')
    (ROOT / 'notes' / 'solver.yaml').write_text(
        'solver:\n  scheme: implicit\n  steps: 400\n  tolerance: 1.0e-6\n')
    (ROOT / 'notes' / 'settings.json').write_text(
        '{\n  "mesh": {"cells": 128400, "refined": true},\n  "notes": "queued"\n}\n')
    (ROOT / 'notes' / 'postprocess.py').write_text(
        '"""Summarise a run."""\n\n\ndef summarise(residuals):\n    return min(residuals)\n')


def make_broken() -> None:
    """A file whose extension lies, so the failure path stays exercised.

    downmark rejects it; the pass must log and carry on rather than abort,
    and enrich.py's standard-library reader must not produce text either.
    """
    (ROOT / 'broken').mkdir(parents=True, exist_ok=True)
    (ROOT / 'broken' / 'truncated.docx').write_bytes(
        bytes(range(256)) * 4)


def main() -> int:
    # Before anything is deleted: the scanned and image fixtures need
    # pdftoppm, and exiting halfway through would leave the committed corpus
    # wiped and only partly regenerated.
    if not shutil.which('pdftoppm'):
        sys.exit('pdftoppm (poppler-utils) is needed to regenerate the corpus')
    if ROOT.exists():
        shutil.rmtree(ROOT)
    make_docx()
    make_xlsx()
    make_pptx()
    make_text_pdf()
    make_scans()
    make_image()
    make_plain_files()
    make_broken()
    total = sum(p.stat().st_size for p in ROOT.rglob('*') if p.is_file())
    count = sum(1 for p in ROOT.rglob('*') if p.is_file())
    print(f'corpus: {count} files, {total / 1024:.0f} KiB under {ROOT}')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
