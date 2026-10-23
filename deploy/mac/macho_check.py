#!/usr/bin/env python3
"""Check Mach-O files the way a Mac would load them, without macOS tools.

For each file (thin, or the arm64 slice of a universal file) it checks:
  - the CPU is arm64;
  - every linked library resolves the way dyld resolves it with
    DYLD_LIBRARY_PATH set to --lib-dir: by leaf name in --lib-dir, at an
    @rpath, @loader_path or @executable_path location, or under /usr/lib
    or /System (present on every Mac);
  - the code signature (ad hoc is enough on Apple silicon) matches the
    file's pages, since a page that does not match its hash kills the
    process on load;
  - with --max-minos, that the file runs on that macOS version;
  - with --bundled DIR, that no library is loaded from the system when DIR
    carries a library of the same name: a build that picked up the build
    machine's copy needs a macOS release that has one.
Problems are printed and the exit status is 1.

Usage: macho_check.py [--lib-dir DIR] [--max-minos 13.5] [--bundled DIR] [--verbose] FILE...
       macho_check.py --print-minos FILE...   (the newest macOS any file needs)
Files that are not Mach-O (scripts, data) and symbolic links are skipped.
"""
import argparse
import hashlib
import os
import struct
import sys

LC_REQ_DYLD = 0x80000000
LC_LOAD_DYLIB = 0xC
LC_ID_DYLIB = 0xD
LC_CODE_SIGNATURE = 0x1D
LC_LOAD_WEAK_DYLIB = 0x18 | LC_REQ_DYLD
LC_REEXPORT_DYLIB = 0x1F | LC_REQ_DYLD
LC_LAZY_LOAD_DYLIB = 0x20
LC_LOAD_UPWARD_DYLIB = 0x23 | LC_REQ_DYLD
LC_RPATH = 0x1C | LC_REQ_DYLD
LC_BUILD_VERSION = 0x32
LC_VERSION_MIN_MACOSX = 0x24
DYLIB_CMDS = {LC_LOAD_DYLIB, LC_LOAD_WEAK_DYLIB, LC_REEXPORT_DYLIB, LC_LAZY_LOAD_DYLIB, LC_LOAD_UPWARD_DYLIB}
CPU_ARM64 = 0x0100000C
HASHES = {1: hashlib.sha1, 2: hashlib.sha256}


def cstr(buf, off):
    end = buf.index(b'\0', off)
    return buf[off:end].decode('utf-8', 'replace')


def slice_of(data):
    """The arm64 image inside a file, the file itself when thin, or None."""
    magic = data[:4]
    if magic in (b'\xca\xfe\xba\xbe', b'\xca\xfe\xba\xbf'):    # universal
        n = struct.unpack('>I', data[4:8])[0]
        if n > 20:
            return None    # a Java class file shares this magic
        wide = magic[3] == 0xbf
        step = 32 if wide else 20
        for k in range(n):
            rec = data[8 + k * step:8 + (k + 1) * step]
            cpu, _sub, off, size = struct.unpack('>iiQQ' if wide else '>iiII', rec[:24 if wide else 16])
            if cpu & 0xffffffff == CPU_ARM64:
                return data[off:off + size]
        return b''    # universal, but no arm64 slice
    if magic == b'\xcf\xfa\xed\xfe':    # 64-bit little endian
        return data
    return None


def version(v):
    return f'{v >> 16}.{(v >> 8) & 0xff}'


def parse(path):
    with open(path, 'rb') as fh:
        data = fh.read()
    img = slice_of(data)
    if img is None:
        return None
    if not img:
        return {'cpu': 0}
    _magic, cpu, _sub, ftype, ncmds, _size, _flags, _res = struct.unpack('<IiiIIIII', img[:32])
    out = {'cpu': cpu & 0xffffffff, 'type': ftype, 'deps': [], 'rpaths': [], 'id': None, 'sig': None, 'minos': None, 'img': img}
    off = 32
    for _ in range(ncmds):
        cmd, size = struct.unpack('<II', img[off:off + 8])
        if cmd in DYLIB_CMDS:
            name_off = struct.unpack('<I', img[off + 8:off + 12])[0]
            out['deps'].append((cstr(img, off + name_off), cmd == LC_LOAD_WEAK_DYLIB))
        elif cmd == LC_ID_DYLIB:
            name_off = struct.unpack('<I', img[off + 8:off + 12])[0]
            out['id'] = cstr(img, off + name_off)
        elif cmd == LC_RPATH:
            path_off = struct.unpack('<I', img[off + 8:off + 12])[0]
            out['rpaths'].append(cstr(img, off + path_off))
        elif cmd == LC_CODE_SIGNATURE:
            out['sig'] = struct.unpack('<II', img[off + 8:off + 16])
        elif cmd == LC_BUILD_VERSION:
            _plat, minos = struct.unpack('<II', img[off + 8:off + 16])
            out['minos'] = minos
        elif cmd == LC_VERSION_MIN_MACOSX:
            out['minos'] = struct.unpack('<I', img[off + 8:off + 12])[0]
        off += size
    return out


def signature_problem(info):
    """None when every code page matches the CodeDirectory, else why not."""
    if not info['sig']:
        return 'not signed (Apple silicon refuses to run unsigned code)'
    img = info['img']
    dataoff, datasize = info['sig']
    blob = img[dataoff:dataoff + datasize]
    magic, _length, count = struct.unpack('>III', blob[:12])
    if magic != 0xfade0cc0:
        return f'unexpected signature blob {magic:#x}'
    checked = 0
    for k in range(count):
        slot, boff = struct.unpack('>II', blob[12 + k * 8:20 + k * 8])
        if slot != 0 and not 0x1000 <= slot < 0x1005:
            continue    # requirements, entitlements, the CMS wrapper
        cd = blob[boff:]
        cmagic, _clen, cver, _flags, hash_off, _ident, _nspecial, ncode, code_limit, hsize, htype, _plat, page_log = \
            struct.unpack('>IIIIIIIIIBBBB', cd[:40])
        if cmagic != 0xfade0c02 or htype not in HASHES:
            continue
        if cver >= 0x20300:
            limit64 = struct.unpack('>Q', cd[56:64])[0]
            code_limit = limit64 or code_limit
        page = 1 << page_log if page_log else code_limit
        for i in range(ncode):
            start = i * page
            want = cd[hash_off + i * hsize:hash_off + (i + 1) * hsize]
            got = HASHES[htype](img[start:min(start + page, code_limit)]).digest()[:hsize]
            if got != want:
                return f'code page {i} does not match its signature (modified after signing)'
        checked += 1
    return None if checked else 'no CodeDirectory found'


def system_lib(name):
    return name.startswith('/usr/lib/') or name.startswith('/System/')


def resolve(name, info, path, lib_dir, exe_dir):
    """Where dyld finds `name` for `path`, or None."""
    leaf = os.path.basename(name)
    if lib_dir and os.path.exists(os.path.join(lib_dir, leaf)):
        return os.path.join(lib_dir, leaf)    # DYLD_LIBRARY_PATH, searched first
    if system_lib(name):
        return name
    loader = os.path.dirname(os.path.abspath(path))

    def expand(p):
        return p.replace('@loader_path', loader).replace('@executable_path', exe_dir or loader)
    if name.startswith('@rpath/'):
        for rp in info['rpaths']:
            cand = os.path.join(expand(rp), name[len('@rpath/'):])
            if os.path.exists(cand):
                return cand
        return None
    if name.startswith('@'):
        cand = expand(name)
        return cand if os.path.exists(cand) else None
    return None    # an absolute path outside the system: only on the build machine


def main():
    ap = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    ap.add_argument('--lib-dir', default='', help='the directory DYLD_LIBRARY_PATH will name')
    ap.add_argument('--exe-dir', default='', help='directory standing in for @executable_path')
    ap.add_argument('--max-minos', default='', help='oldest macOS the files must run on, e.g. 13.5')
    ap.add_argument('--bundled', default='', help='libraries the files must load from here, not from the system')
    ap.add_argument('--no-deps', action='store_true', help='check architecture and signature only')
    ap.add_argument('--print-minos', action='store_true', help='print the newest macOS version any file needs')
    ap.add_argument('--verbose', action='store_true')
    ap.add_argument('files', nargs='+')
    args = ap.parse_args()
    floor = None
    if args.max_minos:
        major, _, minor = args.max_minos.partition('.')
        floor = (int(major) << 16) | (int(minor or 0) << 8)
    if args.print_minos:
        for f in args.files:
            if not os.path.lexists(f):
                print(f'macho_check: {f}: no such file; left out of the minimum macOS', file=sys.stderr)
        infos = [parse(f) for f in args.files if not os.path.islink(f) and os.path.isfile(f)]
        print(version(max([i.get('minos') or 0 for i in infos if i] or [0])))
        return
    bad = seen = 0
    for f in args.files:
        if os.path.islink(f) or not os.path.isfile(f):
            continue
        info = parse(f)
        if info is None:
            continue
        seen += 1
        if info['cpu'] != CPU_ARM64:
            print(f'{f}: not arm64 (cpu type {info["cpu"]:#x})')
            bad += 1
            continue
        why = signature_problem(info)
        if why:
            print(f'{f}: {why}')
            bad += 1
        if floor is not None and info['minos'] and info['minos'] > floor:
            print(f'{f}: needs macOS {version(info["minos"])}, newer than {args.max_minos}')
            bad += 1
        if args.no_deps:
            continue
        for name, weak in info['deps']:
            if name == info['id']:
                continue
            where = resolve(name, info, f, args.lib_dir, args.exe_dir)
            if where is None and not weak:
                print(f'{f}: unresolved {name}')
                bad += 1
            elif args.bundled and system_lib(name) and \
                    os.path.exists(os.path.join(args.bundled, os.path.basename(name))):
                print(f'{f}: loads the system\'s {name}, not the one in {args.bundled}')
                bad += 1
            elif args.verbose:
                print(f'{f}: {name} -> {where or "(weak, absent)"}')
    if args.verbose or bad:
        print(f'{seen} Mach-O files checked, {bad} problems')
    sys.exit(1 if bad else 0)


if __name__ == '__main__':
    main()
