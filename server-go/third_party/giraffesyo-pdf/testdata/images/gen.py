#!/usr/bin/env python3
"""Regenerates the scanned-page fixtures in this directory.

Each PDF is one page holding one image of the same rendered text, encoded
the way real scanning pipelines encode it:

  scan-jpeg.pdf    DCTDecode, 8-bit grey (Pillow)
  scan-g4.pdf      CCITTFaxDecode K=-1 (Pillow, libtiff Group 4)
  scan-jbig2.pdf   JBIG2Decode, symbol dictionary in /JBIG2Globals plus a
                   text region (jbig2enc: jbig2 -s -p)
  scan-jbig2-generic.pdf  JBIG2Decode, a single generic region (jbig2 -p)

Requires Pillow, a TrueType font, and jbig2enc (brew install jbig2enc).
Run from this directory: python3 gen.py
"""
import os
import subprocess
import tempfile

from PIL import Image, ImageDraw, ImageFont

Image.init()  # register every codec, including the JPEG writer the PDF plugin uses

TEXT = ["The quick brown fox", "jumps over 13 lazy dogs."]
W, H = 620, 170

font = None
for path in (
    "/System/Library/Fonts/Supplemental/Arial.ttf",
    "/System/Library/Fonts/Helvetica.ttc",
    "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
):
    if os.path.exists(path):
        font = ImageFont.truetype(path, 40)
        break
if font is None:
    raise SystemExit("no TrueType font found")

img = Image.new("L", (W, H), 255)
draw = ImageDraw.Draw(img)
for i, line in enumerate(TEXT):
    draw.text((24, 24 + i * 60), line, font=font, fill=0)

img.save("scan-jpeg.pdf", resolution=150.0, quality=70)
img.convert("1").save("scan-g4.pdf", resolution=150.0)


def pdf(objs, root=1):
    out = bytearray(b"%PDF-1.5\n")
    offsets = []
    for i, obj in enumerate(objs, 1):
        offsets.append(len(out))
        out += b"%d 0 obj\n" % i + obj + b"\nendobj\n"
    xref = len(out)
    out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1)
    for off in offsets:
        out += b"%010d 00000 n \n" % off
    out += b"trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objs) + 1, root, xref)
    return bytes(out)


def stream(dict_, data):
    return b"<< " + dict_ + b" /Length %d >>\nstream\n" % len(data) + data + b"\nendstream"


def jbig2_pdf(name, symbol_mode):
    with tempfile.TemporaryDirectory() as tmp:
        src = os.path.join(tmp, "page.png")
        img.convert("1").save(src)
        globals_ = None
        if symbol_mode:
            # Symbol mode writes the shared dictionary and one embedded
            # page stream per input file.
            subprocess.run(["jbig2", "-p", "-s", src], cwd=tmp, check=True, capture_output=True)
            page = open(os.path.join(tmp, "output.0000"), "rb").read()
            globals_ = open(os.path.join(tmp, "output.sym"), "rb").read()
        else:
            # Generic mode writes the embedded page stream to stdout.
            page = subprocess.run(["jbig2", "-p", src], cwd=tmp, check=True, capture_output=True).stdout
    scale = 72.0 / 150.0
    objs = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>"
        % (W * scale, H * scale),
        stream(b"", b"q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q" % (W * scale, H * scale)),
    ]
    parms = b""
    if globals_ is not None:
        parms = b" /DecodeParms << /JBIG2Globals 6 0 R >>"
    objs.append(stream(
        b"/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /JBIG2Decode" % (W, H) + parms,
        page,
    ))
    if globals_ is not None:
        objs.append(stream(b"", globals_))
    open(name, "wb").write(pdf(objs))


jbig2_pdf("scan-jbig2.pdf", True)
jbig2_pdf("scan-jbig2-generic.pdf", False)
