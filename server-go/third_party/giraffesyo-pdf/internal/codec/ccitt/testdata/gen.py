#!/usr/bin/env python3
"""Regenerate the CCITT decoder fixtures.

Synthetic bilevel images are rendered in memory, written as a minimal
uncompressed MinIsWhite TIFF, and compressed by libtiff's tiffcp with each
of the fax codecs the decoder supports. The single encoded strip of every
output is stored as <image>.<variant>.ccitt, the expected pixels as
<image>.pbm (P4, 1 = black), and fixtures.json lists every fixture with
its decode parameters. The "page" image is too large to store as a PBM;
the Go tests regenerate it from the same LCG pattern (see pagePattern in
fixtures_test.go), so keep the two in sync.

Requirements: Pillow (with libtiff support, used only to read the TIFF
tags back and to cross-check the round trip) and tiffcp on PATH.

    python3 internal/codec/ccitt/testdata/gen.py
"""

import json
import os
import struct
import subprocess
import sys
import tempfile

from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))

# tiffcp compression option -> (K sign, byte-aligned EOLs)
VARIANTS = {
    "g4": ("g4", -1, False),
    "g3-1d": ("g3:1d", 0, False),
    "g3-2d": ("g3:2d", 4, False),
    "g3-1d-fill": ("g3:1d:fill", 0, True),
    "g3-2d-fill": ("g3:2d:fill", 4, True),
}


# The large page is stored in only these encodings.
PAGE_VARIANTS = ("g4", "g3-2d-fill")


class Bitmap:
    """One byte per pixel, 1 = black."""

    def __init__(self, w, h):
        self.w, self.h = w, h
        self.px = bytearray(w * h)

    def set(self, x, y):
        if 0 <= x < self.w and 0 <= y < self.h:
            self.px[y * self.w + x] = 1

    def rect(self, x, y, w, h):
        for yy in range(max(y, 0), min(y + h, self.h)):
            row = yy * self.w
            for xx in range(max(x, 0), min(x + w, self.w)):
                self.px[row + xx] = 1

    def packed(self):
        """Rows packed MSB first, padded to a byte boundary."""
        stride = (self.w + 7) // 8
        out = bytearray(stride * self.h)
        for y in range(self.h):
            row = y * self.w
            for x in range(self.w):
                if self.px[row + x]:
                    out[y * stride + (x >> 3)] |= 0x80 >> (x & 7)
        return bytes(out)


class LCG:
    def __init__(self, seed):
        self.s = seed

    def next(self):
        self.s = (self.s * 1103515245 + 12345) & 0x7FFFFFFF
        return self.s


def page_pattern(w, h, seed):
    """Deterministic text-like page; mirrored by pagePattern in Go."""
    bm = Bitmap(w, h)
    r = LCG(seed)
    for _ in range((w * h) // 1500):
        x, y = r.next() % w, r.next() % h
        rw, rh = 1 + r.next() % 12, 1 + r.next() % 12
        bm.rect(x, y, rw, rh)
    for i in range(h // 50):
        bm.rect(w // 10, (i * 50 + 7) % h, w * 8 // 10, 1)
    for t in range(min(w, h)):
        bm.set(t, t)
        bm.set(w - 1 - t, t)
    return bm


def images():
    # Rectangles, diagonals and noise at a width divisible by 8.
    bm = Bitmap(200, 80)
    bm.rect(10, 10, 50, 30)
    bm.rect(80, 5, 3, 70)
    bm.rect(100, 40, 90, 2)
    for t in range(80):
        bm.set(t * 2, t)
        bm.set(199 - t, t)
    r = LCG(7)
    for _ in range(300):
        bm.set(r.next() % 200, r.next() % 80)
    yield "rects", bm

    yield "white", Bitmap(200, 80)

    bm = Bitmap(200, 80)
    bm.rect(0, 0, 200, 80)
    yield "black", bm

    # One pixel wide: alternating runs of a few rows each.
    bm = Bitmap(1, 50)
    for y in range(50):
        if (y // 3) % 2 == 1:
            bm.set(0, y)
    yield "w1", bm

    # Width not divisible by 8, random pixels.
    bm = Bitmap(37, 13)
    r = LCG(3)
    for _ in range(200):
        bm.set(r.next() % 37, r.next() % 13)
    yield "odd", bm

    # Tall and narrow with a zigzag.
    bm = Bitmap(5, 300)
    for y in range(300):
        bm.set(y % 5, y)
        if y % 17 == 0:
            bm.rect(0, y, 5, 1)
    yield "tall", bm

    # Every makeup code of both colours, including the shared extended
    # makeup codes 1792..2560: row pairs with a white-first and a
    # black-first long run.
    bm = Bitmap(2600, 82)
    y = 0
    for m in range(64, 2561, 64):
        bm.rect(m + 3, y, 2600, 1)  # white run m+3 then black
        y += 1
        bm.rect(0, y, m + 5, 1)  # black run m+5 then white
        y += 1
    bm.rect(0, 80, 2600, 1)  # full black row (ext makeup 2560 + 40)
    yield "makeup", bm  # row 81 stays white (white 2560 + 40)

    # Dense-ish 2-D content so pass/vertical modes get exercised.
    bm = Bitmap(128, 64)
    r = LCG(11)
    for _ in range(60):
        bm.rect(r.next() % 128, r.next() % 64, 1 + r.next() % 20, 1 + r.next() % 10)
    yield "blobs", bm

    yield "page", page_pattern(2550, 3300, 42)


def write_tiff(path, bm):
    """Minimal uncompressed little-endian TIFF, PhotometricInterpretation
    MinIsWhite (0 = white), 1 bit per sample, a single strip."""
    data = bm.packed()
    entries = [
        (256, 4, 1, bm.w),  # ImageWidth
        (257, 4, 1, bm.h),  # ImageLength
        (258, 3, 1, 1),  # BitsPerSample
        (259, 3, 1, 1),  # Compression none
        (262, 3, 1, 0),  # Photometric MinIsWhite
        (266, 3, 1, 1),  # FillOrder MSB first
        (273, 4, 1, 8),  # StripOffsets
        (277, 3, 1, 1),  # SamplesPerPixel
        (278, 4, 1, bm.h),  # RowsPerStrip
        (279, 4, 1, len(data)),  # StripByteCounts
    ]
    ifd_off = 8 + len(data)
    if ifd_off % 2:
        data += b"\0"
        ifd_off += 1
    out = bytearray(b"II*\0" + struct.pack("<I", ifd_off) + data)
    out += struct.pack("<H", len(entries))
    for tag, typ, count, value in entries:
        out += struct.pack("<HHI", tag, typ, count)
        out += struct.pack("<I", value) if typ == 4 else struct.pack("<HH", value, 0)
    out += struct.pack("<I", 0)
    with open(path, "wb") as f:
        f.write(out)


def write_pbm(path, bm):
    with open(path, "wb") as f:
        f.write(b"P4\n%d %d\n" % (bm.w, bm.h))
        f.write(bm.packed())


def main():
    index = []
    tmp = tempfile.mkdtemp()
    for name, bm in images():
        if name != "page":
            write_pbm(os.path.join(HERE, name + ".pbm"), bm)
        raw = os.path.join(tmp, name + ".tif")
        write_tiff(raw, bm)
        for variant, (copt, k, fill) in VARIANTS.items():
            if name == "page" and variant not in PAGE_VARIANTS:
                continue  # keep the committed fixtures small
            enc = os.path.join(tmp, f"{name}.{variant}.tif")
            subprocess.run(["tiffcp", "-c", copt, "-r", str(bm.h), raw, enc], check=True)
            with Image.open(enc) as im:
                tags = im.tag_v2
                assert tags[262] == 0, "expected MinIsWhite"
                assert tags.get(266, 1) == 1, "expected MSB-first fill order"
                offsets, counts = tags[273], tags[279]
                assert len(offsets) == 1, "expected a single strip"
                if k < 0:
                    assert tags[259] == 4
                else:
                    assert tags[259] == 3
                    t4 = tags.get(292, 0)
                    assert bool(t4 & 1) == (k > 0), "T4Options 2-D bit"
                    assert bool(t4 & 4) == fill, "T4Options fill bit"
                # Cross-check libtiff's own decode against the source pixels.
                decoded = im.convert("L").tobytes()
            want = bytes(0 if p else 255 for p in bm.px)
            assert decoded == want, f"{name}.{variant}: libtiff round trip mismatch"
            with open(enc, "rb") as f:
                f.seek(offsets[0])
                strip = f.read(counts[0])
            fixture = f"{name}.{variant}.ccitt"
            with open(os.path.join(HERE, fixture), "wb") as f:
                f.write(strip)
            index.append(
                {
                    "file": fixture,
                    "pbm": "" if name == "page" else name + ".pbm",
                    "width": bm.w,
                    "height": bm.h,
                    "k": k,
                    "byteAlign": fill,
                }
            )
            print(f"{fixture}: {len(strip)} bytes", file=sys.stderr)
    with open(os.path.join(HERE, "fixtures.json"), "w") as f:
        json.dump(index, f, indent=1)
        f.write("\n")


if __name__ == "__main__":
    main()
