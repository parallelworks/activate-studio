#!/usr/bin/env python3
"""Regenerate the JBIG2 test fixtures.

Run from this directory:

    python3 gen.py

Requirements: Python 3 with Pillow (with libtiff, for the MMR fixture),
jbig2enc (`jbig2`, from Homebrew: brew install jbig2enc), jbig2dec
(`jbig2dec`, brew install jbig2dec), a Go toolchain, and a system TTF at
FONT (rendered text gives the symbol encoder real glyphs to classify).

Each fixture is NAME.jb2 (the PDF-embedded page stream), optionally
NAME.globals (the /JBIG2Globals stream) and NAME.pbm or NAME.pbm.gz (the
expected page, P4, 1 = black). Expected outputs come from the jbig2dec
reference decoder, never from this package; for lossless encodings the
script also asserts that jbig2dec reproduces the source image.

Fixture families:
  *.gen / *.gen-tpgdon   jbig2enc generic region, TPGDON off / on
  *.sym                  jbig2enc symbol dictionary (globals) + text region
  mmr-*                  an MMR (T.6) generic region wrapped by this script
  synth-*                streams from the encoder in ../encoder_test.go
                         (templates 1-3, AT pixels, reference corners,
                         transposition, strips, composition operators,
                         unknown lengths, long referrals ...), written by
                         `go test -run TestWriteSyntheticFixtures -update`
                         and cross-checked here against jbig2dec.
"""
import glob
import gzip
import os
import random
import shutil
import struct
import subprocess
import sys

from PIL import Image, ImageDraw, ImageFont, ImageOps

FONT = "/System/Library/Fonts/Supplemental/Arial.ttf"
SERIF = "/System/Library/Fonts/Supplemental/Times New Roman.ttf"
HERE = os.path.dirname(os.path.abspath(__file__))
WORK = os.path.join(HERE, "_work")
GZIP_ABOVE = 16 * 1024  # expected PBMs larger than this are stored gzipped


def run(*args, **kw):
    return subprocess.run(args, check=True, capture_output=True, **kw)


def pbm_of(img):
    """Pack a mode-1 PIL image (255 = white) as P4 with 1 = black."""
    w, h = img.size
    return b"P4\n%d %d\n" % (w, h) + img.tobytes("raw", "1;I")  # "1;I": inverted, 1 = black


def read_pbm(path):
    b = open(path, "rb").read()
    assert b.startswith(b"P4")
    parts = b.split(b"\n", 2)
    return parts[2]


def write_expected(name, pbm):
    for old in (name + ".pbm", name + ".pbm.gz"):
        if os.path.exists(os.path.join(HERE, old)):
            os.remove(os.path.join(HERE, old))
    if len(pbm) > GZIP_ABOVE:
        with open(os.path.join(HERE, name + ".pbm.gz"), "wb") as f:
            with gzip.GzipFile(fileobj=f, mode="wb", mtime=0) as z:  # mtime=0 keeps output reproducible
                z.write(pbm)
    else:
        open(os.path.join(HERE, name + ".pbm"), "wb").write(pbm)


def jbig2dec(page, globals_=None):
    out = os.path.join(WORK, "dec.pbm")
    if os.path.exists(out):
        os.remove(out)
    args = ["jbig2dec", "-e", "-q", "-o", out]
    if globals_:
        args.append(globals_)
    args.append(page)
    run(*args)
    return open(out, "rb").read()


def install(name, page_bytes, globals_bytes, expected_pbm):
    open(os.path.join(HERE, name + ".jb2"), "wb").write(page_bytes)
    gpath = os.path.join(HERE, name + ".globals")
    if globals_bytes is not None:
        open(gpath, "wb").write(globals_bytes)
    elif os.path.exists(gpath):
        os.remove(gpath)
    write_expected(name, expected_pbm)


# --- source images -------------------------------------------------------

def text_image():
    img = Image.new("1", (200, 80), 1)
    d = ImageDraw.Draw(img)
    f = ImageFont.truetype(FONT, 24)
    d.text((5, 5), "Hello JBIG2", font=f, fill=0)
    d.text((5, 40), "Hello again", font=f, fill=0)
    d.ellipse((150, 40, 190, 75), outline=0)
    return img


def symbols_image():
    """A 600x400 block of text in two faces: many distinct glyphs and many
    repeats, so the symbol encoder builds a real dictionary."""
    img = Image.new("1", (600, 400), 1)
    d = ImageDraw.Draw(img)
    rnd = random.Random(7)
    words = ("quick brown fox jumps over lazy dog pack my box with five dozen "
             "liquor jugs sphinx of black quartz judge vow 0123456789").split()
    y = 8
    for i in range(11):
        f = ImageFont.truetype(FONT if i % 2 == 0 else SERIF, 22 + (i % 3) * 4)
        line = " ".join(rnd.choice(words) for _ in range(7))
        d.text((6, y), line, font=f, fill=0)
        y += 34
    return img


def page_image():
    """A letter-size 300 dpi page of text: the benchmark input."""
    W, H = 2550, 3300
    img = Image.new("1", (W, H), 1)
    d = ImageDraw.Draw(img)
    rnd = random.Random(1)
    f = ImageFont.truetype(SERIF, 40)
    words = ("the quick brown fox jumps over the lazy dog lorem ipsum dolor "
             "sit amet consectetur adipiscing elit sed do eiusmod tempor").split()
    y = 200
    while y < H - 250:
        d.text((250, y), " ".join(rnd.choice(words) for _ in range(14)), font=f, fill=0)
        y += 60
    return img


def single_symbol_image():
    """One glyph repeated: the symbol dictionary has a single symbol, so
    SBSYMCODELEN is 0 and symbol IDs cost no bits."""
    img = Image.new("1", (120, 40), 1)
    d = ImageDraw.Draw(img)
    d.text((5, 5), "o o o o", font=ImageFont.truetype(FONT, 24), fill=0)
    return img


def odd_image(w, h):
    img = Image.new("1", (w, h), 1)
    d = ImageDraw.Draw(img)
    for x in range(w):
        for y in range(h):
            if (x * 7 + y * 3) % 5 == 0 or x == y:
                d.point((x, y), fill=0)
    return img


# --- jbig2enc fixtures ---------------------------------------------------

def jbig2enc_fixtures(name, img, symbol_mode=True):
    src = os.path.join(WORK, name + ".png")
    img.save(src)
    want = pbm_of(img)
    for suffix, flags in (("gen", []), ("gen-tpgdon", ["-d"])):
        page = run("jbig2", "-p", *flags, src).stdout
        got = jbig2dec_bytes(page)
        assert got == want, f"{name}.{suffix}: jbig2dec differs from source"
        install(f"{name}.{suffix}", page, None, got)
    if symbol_mode:
        base = os.path.join(WORK, name)
        run("jbig2", "-s", "-p", "-b", base, src)
        sym = open(base + ".sym", "rb").read()
        page = open(base + ".0000", "rb").read()
        # Symbol mode merges similar glyphs, so the reference decoder's
        # output (not the source) is the expectation.
        install(f"{name}.sym", page, sym, jbig2dec_bytes(page, sym))


def jbig2dec_bytes(page, globals_=None):
    p = os.path.join(WORK, "page.jb2")
    open(p, "wb").write(page)
    g = None
    if globals_ is not None:
        g = os.path.join(WORK, "page.globals")
        open(g, "wb").write(globals_)
    return jbig2dec(p, g)


# --- MMR fixture ---------------------------------------------------------

def segment(number, typ, data, referred=(), page=1):
    hdr = struct.pack(">IB", number, typ) + bytes([len(referred) << 5])
    hdr += bytes(referred) + bytes([page]) + struct.pack(">I", len(data))
    return hdr + data


def g4_strip(img):
    """Return the raw T.6 data of img via a single-strip Group 4 TIFF."""
    p = os.path.join(WORK, "mmr.tif")
    # Pillow writes mode-1 TIFFs as PhotometricInterpretation 1 (0 = black),
    # and libtiff's G4 coder treats 0 pixels as T.6 "white"; invert first so
    # the T.6 colours match the image (JBIG2 MMR: white = 0 bits).
    inv = ImageOps.invert(img.convert("L")).convert("1")
    inv.save(p, compression="group4", tiffinfo={278: img.size[1]})  # RowsPerStrip = height
    t = Image.open(p)
    offsets, counts = t.tag_v2[273], t.tag_v2[279]
    assert len(offsets) == 1, "expected a single strip"
    b = open(p, "rb").read()
    return b[offsets[0]:offsets[0] + counts[0]]


def mmr_fixture(name, img):
    w, h = img.size
    data = g4_strip(img)
    page_info = struct.pack(">IIIIBH", w, h, 0, 0, 0, 0)
    region = struct.pack(">IIIIB", w, h, 0, 0, 0) + bytes([1]) + data  # flags: MMR=1
    stream = segment(0, 48, page_info) + segment(1, 39, region) + segment(2, 49, b"")
    got = jbig2dec_bytes(stream)
    assert got == pbm_of(img), f"{name}: jbig2dec differs from source"
    install(name, stream, None, got)


# --- synthetic fixtures --------------------------------------------------

def synthetic_fixtures():
    for old in glob.glob(os.path.join(HERE, "synth-*")):
        os.remove(old)
    run("go", "test", "-run", "TestWriteSyntheticFixtures", "-update", ".", cwd=os.path.dirname(HERE))
    for src in sorted(glob.glob(os.path.join(HERE, "synth-*.src.pbm"))):
        name = os.path.basename(src)[:-len(".src.pbm")]
        page = os.path.join(HERE, name + ".jb2")
        g = os.path.join(HERE, name + ".globals")
        got = jbig2dec(page, g if os.path.exists(g) else None)
        want = open(src, "rb").read()
        assert got == want, f"{name}: jbig2dec differs from the encoder's source image"
        os.remove(src)
        write_expected(name, got)


def main():
    os.makedirs(WORK, exist_ok=True)
    jbig2enc_fixtures("text", text_image())
    jbig2enc_fixtures("symbols", symbols_image())
    jbig2enc_fixtures("single", single_symbol_image())
    jbig2enc_fixtures("odd37x13", odd_image(37, 13), symbol_mode=False)
    jbig2enc_fixtures("odd8x5", odd_image(8, 5), symbol_mode=False)
    jbig2enc_fixtures("odd1x1", odd_image(1, 1), symbol_mode=False)
    # The big page only as TPGDON generic: its expected output is large.
    src = os.path.join(WORK, "page.png")
    page_image().save(src)
    page = run("jbig2", "-p", "-d", src).stdout
    got = jbig2dec_bytes(page)
    assert got == pbm_of(page_image())
    install("page.gen-tpgdon", page, None, got)
    mmr_fixture("mmr-text", text_image())
    mmr_fixture("mmr-odd37x13", odd_image(37, 13))
    synthetic_fixtures()
    shutil.rmtree(WORK)
    total = sum(os.path.getsize(p) for p in glob.glob(os.path.join(HERE, "*")) if os.path.isfile(p))
    print(f"fixtures: {total / 1024:.0f} KiB")


if __name__ == "__main__":
    sys.exit(main())
