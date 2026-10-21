#!/usr/bin/env python3
"""Extract files from a macOS installer package without macOS tools.

A .pkg is a xar archive: a zlib-compressed XML table of contents, then a
heap holding each part. A component's Payload is a cpio archive (odc or
newc), compressed with gzip or with Apple's pbzx (xz chunks). This reads
both layouts with the standard library, so the build runs on Linux too.

Usage:
  pkg_extract.py PKG --list
  pkg_extract.py PKG DEST [--prefix usr/local] [--only bin lib etc]

With --prefix, only entries under that directory of the Payload are
written, with the prefix removed (./usr/local/bin/x becomes DEST/bin/x);
--only further limits them to the named top-level directories under it.
Modes and symbolic links are kept; nothing is written outside DEST.
"""
import argparse
import gzip
import lzma
import os
import struct
import sys
import xml.etree.ElementTree as ET
import zlib


def xar_parts(data):
    """Yields (name, bytes) for every file in a xar archive's heap."""
    magic, hsize, _ver, toc_len, _toc_raw, _alg = struct.unpack('>4sHHQQI', data[:28])
    if magic != b'xar!':
        raise SystemExit('not a xar archive (a macOS .pkg)')
    toc = ET.fromstring(zlib.decompress(data[hsize:hsize + toc_len]))
    heap = hsize + toc_len

    def walk(node, prefix=''):
        for f in node.findall('file'):
            name = prefix + (f.findtext('name') or '')
            d = f.find('data')
            if d is not None:
                off, ln = int(d.findtext('offset')), int(d.findtext('length'))
                style = d.find('encoding').get('style', '')
                raw = data[heap + off:heap + off + ln]
                if 'gzip' in style:
                    raw = zlib.decompress(raw)
                elif 'bzip2' in style:
                    import bz2
                    raw = bz2.decompress(raw)
                yield name, raw
            yield from walk(f, name + '/')

    yield from walk(toc.find('toc'))


def unpbzx(raw):
    """Apple's pbzx stream: a header, then (flags, length, chunk) records."""
    out, i = [], 12
    while i < len(raw):
        _flags, length = struct.unpack('>QQ', raw[i:i + 16])
        i += 16
        chunk = raw[i:i + length]
        i += length
        out.append(lzma.decompress(chunk) if chunk[:6] == b'\xfd7zXZ\x00' else chunk)
    return b''.join(out)


def payload_bytes(raw):
    if raw[:4] == b'pbzx':
        return unpbzx(raw)
    if raw[:2] == b'\x1f\x8b':
        return gzip.decompress(raw)
    return raw


def cpio_entries(raw):
    """Yields (name, mode, body) from an odc (070707) or newc (070701/2) cpio."""
    i = 0
    while i < len(raw):
        magic = raw[i:i + 6]
        if magic == b'070707':
            mode = int(raw[i + 18:i + 24], 8)
            namesize = int(raw[i + 59:i + 65], 8)
            filesize = int(raw[i + 65:i + 76], 8)
            name = raw[i + 76:i + 76 + namesize - 1].decode('utf-8', 'surrogateescape')
            start = i + 76 + namesize
            body = raw[start:start + filesize]
            i = start + filesize
        elif magic in (b'070701', b'070702'):
            mode = int(raw[i + 14:i + 22], 16)
            filesize = int(raw[i + 54:i + 62], 16)
            namesize = int(raw[i + 94:i + 102], 16)
            name = raw[i + 110:i + 110 + namesize - 1].decode('utf-8', 'surrogateescape')
            start = (i + 110 + namesize + 3) & ~3
            body = raw[start:start + filesize]
            i = (start + filesize + 3) & ~3
        else:
            raise SystemExit(f'unrecognized cpio header at byte {i}')
        if name == 'TRAILER!!!':
            return
        yield name, mode, body


def payloads(pkg):
    with open(pkg, 'rb') as fh:
        data = fh.read()
    for name, raw in xar_parts(data):
        if name == 'Payload' or name.endswith('/Payload'):
            yield name, payload_bytes(raw)


def inside(dest, path):
    dest = os.path.realpath(dest)
    return os.path.realpath(path) == dest or os.path.realpath(path).startswith(dest + os.sep)


def main():
    ap = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    ap.add_argument('pkg')
    ap.add_argument('dest', nargs='?')
    ap.add_argument('--list', action='store_true', help='list the Payload entries and exit')
    ap.add_argument('--prefix', default='', help='Payload directory to extract, e.g. usr/local')
    ap.add_argument('--only', nargs='*', default=None, help='top-level directories under the prefix to keep')
    args = ap.parse_args()
    if not args.list and not args.dest:
        ap.error('DEST is required unless --list is given')

    prefix = args.prefix.strip('/')
    written = 0
    links = []
    for part, raw in payloads(args.pkg):
        entries = list(cpio_entries(raw))
        if args.list:
            print(f'{part}: {len(entries)} entries')
            for name, mode, body in entries:
                kind = {0o040000: 'd', 0o120000: 'l'}.get(mode & 0o170000, '-')
                print(f'  {kind} {mode & 0o7777:04o} {len(body):>10} {name}')
            continue
        for name, mode, body in entries:
            rel = os.path.normpath(name[2:] if name.startswith('./') else name)
            if rel == '.' or rel.startswith('..') or os.path.isabs(rel):
                continue
            if prefix:
                if rel != prefix and not rel.startswith(prefix + '/'):
                    continue
                rel = os.path.relpath(rel, prefix)
                if rel == '.':
                    continue
            if args.only is not None and rel.split('/')[0] not in args.only:
                continue
            out = os.path.join(args.dest, rel)
            if not inside(args.dest, os.path.dirname(out) or args.dest):
                raise SystemExit(f'refusing to write outside {args.dest}: {name}')
            kind = mode & 0o170000
            if kind == 0o040000:
                os.makedirs(out, exist_ok=True)
                continue
            os.makedirs(os.path.dirname(out), exist_ok=True)
            if os.path.lexists(out):
                os.remove(out)
            if kind == 0o120000:
                links.append((out, body.decode('utf-8', 'surrogateescape')))
                continue
            if kind != 0o100000:
                continue    # devices and fifos have no place in an install
            with open(out, 'wb') as fh:
                fh.write(body)
            os.chmod(out, mode & 0o777)
            written += 1
    # Links last, each checked to resolve inside DEST.
    for out, target in links:
        if os.path.isabs(target) or not inside(args.dest, os.path.join(os.path.dirname(out), target)):
            raise SystemExit(f'refusing a link that leaves {args.dest}: {out} -> {target}')
        os.symlink(target, out)
    if not args.list:
        print(f'extracted {written} files and {len(links)} links into {args.dest}')
        if written == 0:
            sys.exit(1)


if __name__ == '__main__':
    main()
