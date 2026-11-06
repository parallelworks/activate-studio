#!/usr/bin/env python3
"""Check ELF files the way a Linux host would load them, with no tools but Python.

For each file it reads the dynamic section and the symbol versions the file
needs, and checks:
  - the CPU is x86-64;
  - every needed library is found beside the files that load it (through the
    file's RUNPATH or RPATH, with $ORIGIN and $LIB resolved as the loader
    does), or is one of the system libraries a desktop install always has
    (the C library, the C++ runtime, libgcc_s and zlib, below);
  - no RUNPATH or RPATH names a directory outside --root, where the build
    machine's own copy would be found on that machine and nowhere else;
  - the newest glibc symbol version it needs is at most --max-glibc (the
    oldest target's, 2.28 for RHEL 8), and likewise --max-glibcxx and
    --max-cxxabi for libstdc++'s versions.
It prints the newest versions across all files, and exits 1 on a problem.

Usage: elf_check.py --root DIR [--max-glibc 2.28] [--max-glibcxx 3.4.25]
                    [--max-cxxabi 1.3.11] [--list] FILE|DIR...
A directory is searched for ELF files (symbolic links are not followed).
"""
import argparse
import os
import re
import struct
import sys

# Libraries every target has: glibc's, the C++ and GCC runtimes, and zlib.
SYSTEM = {
    'libc.so.6', 'libm.so.6', 'libdl.so.2', 'libpthread.so.0', 'librt.so.1', 'libutil.so.1',
    'libresolv.so.2', 'libnsl.so.1', 'libcrypt.so.1', 'libanl.so.1',
    'ld-linux-x86-64.so.2', 'libstdc++.so.6', 'libgcc_s.so.1', 'libz.so.1',
}

SHT_DYNAMIC = 6
SHT_GNU_VERNEED = 0x6FFFFFFE
DT_NEEDED, DT_SONAME, DT_RPATH, DT_RUNPATH = 1, 14, 15, 29


def vkey(v):
    return tuple(int(x) for x in v.split('.'))


class Elf:
    def __init__(self, path):
        self.path = path
        with open(path, 'rb') as fh:
            self.data = fh.read()
        d = self.data
        if d[:4] != b'\x7fELF':
            raise ValueError('not ELF')
        if d[4] != 2 or d[5] != 1:
            raise ValueError('not 64-bit little-endian')
        (self.type, self.machine) = struct.unpack_from('<HH', d, 16)
        shoff, = struct.unpack_from('<Q', d, 40)
        shentsize, shnum = struct.unpack_from('<HH', d, 58)
        self.sections = []
        for i in range(shnum if shoff else 0):
            name, typ, flags, addr, off, size, link, info, align, entsize = struct.unpack_from(
                '<IIQQQQIIQQ', d, shoff + i * shentsize)
            self.sections.append({'type': typ, 'off': off, 'size': size, 'link': link, 'info': info})
        self.needed, self.rpath, self.runpath, self.soname = [], [], [], None
        self.versions = []   # (file, version) pairs the file needs
        for s in self.sections:
            if s['type'] == SHT_DYNAMIC:
                strtab = self.sections[s['link']]
                for o in range(s['off'], s['off'] + s['size'], 16):
                    tag, val = struct.unpack_from('<qQ', d, o)
                    if tag == 0:
                        break
                    if tag == DT_NEEDED:
                        self.needed.append(self.string(strtab, val))
                    elif tag == DT_SONAME:
                        self.soname = self.string(strtab, val)
                    elif tag == DT_RPATH:
                        self.rpath = self.string(strtab, val).split(':')
                    elif tag == DT_RUNPATH:
                        self.runpath = self.string(strtab, val).split(':')
            elif s['type'] == SHT_GNU_VERNEED:
                strtab = self.sections[s['link']]
                o = s['off']
                for _ in range(s['info']):
                    _ver, cnt, fileoff, aux, nxt = struct.unpack_from('<HHIII', d, o)
                    fname = self.string(strtab, fileoff)
                    a = o + aux
                    for _ in range(cnt):
                        _hash, _flags, _other, nameoff, anext = struct.unpack_from('<IHHII', d, a)
                        self.versions.append((fname, self.string(strtab, nameoff)))
                        a += anext
                    o += nxt

    def string(self, strtab, off):
        start = strtab['off'] + off
        end = self.data.index(b'\0', start)
        return self.data[start:end].decode()


def elf_files(args):
    for a in args:
        if os.path.isdir(a):
            for d, dirs, files in os.walk(a):
                for f in sorted(files):
                    p = os.path.join(d, f)
                    if os.path.islink(p) or not os.path.isfile(p):
                        continue
                    try:
                        with open(p, 'rb') as fh:
                            if fh.read(4) == b'\x7fELF':
                                yield p
                    except OSError:
                        pass
        elif os.path.isfile(a):
            yield a


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--root', required=True, help='the tree the files are loaded from')
    ap.add_argument('--max-glibc', default='2.34')
    ap.add_argument('--max-glibcxx', default='3.4.29')
    ap.add_argument('--max-cxxabi', default='1.3.13')
    ap.add_argument('--list', action='store_true', help='list the newest glibc version each file needs')
    ap.add_argument('--skip', action='append', default=[], metavar='PREFIX',
                    help='leave out files whose path under --root starts with PREFIX (the window shells, checked with --no-libs)')
    ap.add_argument('--no-libs', action='store_true',
                    help='do not check the needed libraries (a window shell links to the computer\'s GTK and WebKitGTK)')
    ap.add_argument('files', nargs='+')
    args = ap.parse_args()
    root = os.path.realpath(args.root)
    limits = {'GLIBC': args.max_glibc, 'GLIBCXX': args.max_glibcxx, 'CXXABI': args.max_cxxabi}
    newest = {k: ('0', None) for k in limits}
    sysneeds = {}
    problems, count = [], 0
    # Every file in the tree, by its leaf name, for resolving by SONAME.
    tree_names = {}
    for p in elf_files([root]):
        tree_names.setdefault(os.path.basename(p), []).append(p)
    for p in elf_files(args.files):
        if any(os.path.relpath(p, root).startswith(pre) for pre in args.skip):
            continue
        try:
            e = Elf(p)
        except (ValueError, struct.error) as exc:
            problems.append(f'{p}: {exc}')
            continue
        count += 1
        rel = os.path.relpath(p, root)
        if e.machine != 62:
            problems.append(f'{rel}: machine {e.machine}, not x86-64')
        origin = os.path.dirname(os.path.realpath(p))
        dirs = []
        for entry in (e.runpath or e.rpath):
            d = entry.replace('${ORIGIN}', origin).replace('$ORIGIN', origin)
            d = d.replace('${LIB}', 'lib64').replace('$LIB', 'lib64')
            dirs.append(os.path.normpath(d))
            if entry and not os.path.realpath(dirs[-1]).startswith(root + os.sep) and os.path.realpath(dirs[-1]) != root:
                problems.append(f'{rel}: run path {entry} leaves the tree')
        for n in ([] if args.no_libs else e.needed):
            if '/' in n:
                # A needed name with a path (libpython3.so's, $ORIGIN/../lib/...) is loaded from that path.
                if os.path.exists(os.path.normpath(n.replace('${ORIGIN}', origin).replace('$ORIGIN', origin))):
                    continue
            if any(os.path.exists(os.path.join(d, n)) for d in dirs):
                continue
            if n in SYSTEM:
                sysneeds.setdefault(n, []).append(rel)
                continue
            where = f' (in the tree: {os.path.relpath(tree_names[n][0], root)}, not on its run path)' if n in tree_names else ''
            problems.append(f'{rel}: needs {n}, which is neither beside it nor a system library{where}')
        top = '0'
        for fname, ver in e.versions:
            m = re.fullmatch(r'(GLIBC|GLIBCXX|CXXABI)_([0-9.]+)', ver)
            if not m:
                continue
            kind, v = m.groups()
            if vkey(v) > vkey(newest[kind][0]):
                newest[kind] = (v, rel)
            if vkey(v) > vkey(limits[kind]):
                problems.append(f'{rel}: needs {ver}, newer than the target\'s {kind}_{limits[kind]}')
            if kind == 'GLIBC' and vkey(v) > vkey(top):
                top = v
        if args.list and top != '0':
            print(f'  GLIBC_{top:<7} {rel}')
    print(f'{count} ELF files')
    for kind in ('GLIBC', 'GLIBCXX', 'CXXABI'):
        v, who = newest[kind]
        print(f'newest {kind}: {kind}_{v}' + (f' ({who})' if who else '') + f'; limit {kind}_{limits[kind]}')
    print('system libraries needed: ' + ', '.join(sorted(sysneeds)))
    for p in problems:
        print('PROBLEM ' + p)
    return 1 if problems else 0


if __name__ == '__main__':
    sys.exit(main())
