#!/usr/bin/env python3
"""Make GUFI's macOS tools find their libraries inside the app.

GUFI's package links its tools against libraries at the build machine's
paths (/Users/runner/.local/..., /opt/homebrew/opt/...), and GUFI's guide
covers that with DYLD_LIBRARY_PATH. macOS removes DYLD_* variables whenever
a protected program such as /bin/sh or /usr/bin/env starts, so a tool run
from a script, from Python through a shell, or by gufi_vt (which runs
gufi_query through the shell) would not find them. This rewrites the
references in place, without changing any file's size:

  - a library named by an absolute path outside the system becomes
    @loader_path/../lib/<name> in bin/ and @loader_path/<name> in lib/;
  - for a file that loads libraries through @rpath, its first run path
    becomes @loader_path/../lib (bin/) or @loader_path (lib/);
  - a library's own install name becomes @rpath/<name>.

Then it recomputes the code page hashes of the ad hoc signature, which
Apple silicon checks when it maps each page. On a Mac, the build re-signs
the changed files with codesign afterwards as well.

Usage: macho_relink.py DIR    (DIR holds bin/ and lib/)
Prints each file it changed; exits 1 when a reference cannot be rewritten.
"""
import hashlib
import os
import struct
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from macho_check import (  # noqa: E402
    DYLIB_CMDS, HASHES, LC_CODE_SIGNATURE, LC_ID_DYLIB, LC_RPATH, cstr, system_lib)


def put(img, at, capacity, value):
    """Writes a NUL-terminated string into a slot of `capacity` bytes."""
    raw = value.encode() + b'\0'
    if len(raw) > capacity:
        return False
    img[at:at + capacity] = raw + b'\0' * (capacity - len(raw))
    return True


def resign(img):
    """Recomputes every CodeDirectory's code page hashes; False when the
    signature is not one this can redo (a certificate-signed file)."""
    _magic, _cpu, _sub, _ft, ncmds, _size, _flags, _res = struct.unpack('<IiiIIIII', img[:32])
    off, sig = 32, None
    for _ in range(ncmds):
        cmd, size = struct.unpack('<II', img[off:off + 8])
        if cmd == LC_CODE_SIGNATURE:
            sig = struct.unpack('<II', img[off + 8:off + 16])
        off += size
    if not sig:
        return False
    base = sig[0]
    _bmagic, _blen, count = struct.unpack('>III', img[base:base + 12])
    slots = [struct.unpack('>II', img[base + 12 + k * 8:base + 20 + k * 8]) for k in range(count)]
    if any(slot == 0x10000 for slot, _ in slots):
        return False    # a CMS signature over the CodeDirectory; only codesign can redo it
    done = 0
    for slot, boff in slots:
        if slot != 0 and not 0x1000 <= slot < 0x1005:
            continue
        cd = base + boff
        cmagic, _clen, cver, _fl, hash_off, _ident, _nsp, ncode, code_limit, hsize, htype, _plat, page_log = \
            struct.unpack('>IIIIIIIIIBBBB', img[cd:cd + 40])
        if cmagic != 0xfade0c02 or htype not in HASHES:
            continue
        if cver >= 0x20300:
            code_limit = struct.unpack('>Q', img[cd + 56:cd + 64])[0] or code_limit
        page = 1 << page_log if page_log else code_limit
        for i in range(ncode):
            start = i * page
            digest = HASHES[htype](bytes(img[start:min(start + page, code_limit)])).digest()[:hsize]
            at = cd + hash_off + i * hsize
            img[at:at + hsize] = digest
        done += 1
    return done > 0


def relink(path, lib_dir, in_bin):
    with open(path, 'rb') as fh:
        img = bytearray(fh.read())
    if img[:4] != b'\xcf\xfa\xed\xfe':
        return None    # not a thin 64-bit Mach-O (scripts, data)
    _magic, _cpu, _sub, _ft, ncmds, _size, _flags, _res = struct.unpack('<IiiIIIII', img[:32])
    here = '@loader_path/../lib' if in_bin else '@loader_path'
    problems, changed, uses_rpath, rpaths = [], False, False, []
    off = 32
    for _ in range(ncmds):
        cmd, size = struct.unpack('<II', img[off:off + 8])
        if cmd in DYLIB_CMDS or cmd == LC_ID_DYLIB:
            name_off = struct.unpack('<I', img[off + 8:off + 12])[0]
            name = cstr(img, off + name_off)
            leaf = os.path.basename(name)
            new = None
            if name.startswith('@rpath/'):
                uses_rpath = uses_rpath or cmd != LC_ID_DYLIB
            elif name.startswith('/') and not system_lib(name):
                if cmd == LC_ID_DYLIB:
                    new = f'@rpath/{leaf}'
                elif os.path.exists(os.path.join(lib_dir, leaf)):
                    new = f'{here}/{leaf}'
                else:
                    problems.append(f'{name} is not in {lib_dir}')
            if new and new != name:
                if put(img, off + name_off, size - name_off, new):
                    changed = True
                else:
                    problems.append(f'no room to rename {name}')
        elif cmd == LC_RPATH:
            path_off = struct.unpack('<I', img[off + 8:off + 12])[0]
            rpaths.append((off + path_off, size - path_off, cstr(img, off + path_off)))
        off += size
    if uses_rpath and (not rpaths or rpaths[0][2] != here):
        existing = {value for _at, _cap, value in rpaths}
        for at, cap, value in rpaths:
            if here in existing:
                problems.append(f'run path {here} is present but not first')
                break
            if put(img, at, cap, here):
                changed = True
                break
        else:
            if not rpaths:
                problems.append('loads libraries through @rpath but has no run path to point at lib/')
    if problems:
        return problems
    if not changed:
        return False
    if not resign(img):
        return [f'changed, but its signature cannot be redone here (re-sign it with codesign)']
    tmp = path + '.relink'
    with open(tmp, 'wb') as fh:
        fh.write(img)
    os.chmod(tmp, os.stat(path).st_mode & 0o7777)
    os.replace(tmp, path)
    return True


def main():
    if len(sys.argv) != 2:
        raise SystemExit(__doc__.strip().split('\n\n')[-2])
    root = sys.argv[1]
    lib_dir = os.path.join(root, 'lib')
    failed = False
    for sub in ('bin', 'lib'):
        d = os.path.join(root, sub)
        for name in sorted(os.listdir(d)) if os.path.isdir(d) else []:
            p = os.path.join(d, name)
            if os.path.islink(p) or not os.path.isfile(p):
                continue
            result = relink(p, lib_dir, sub == 'bin')
            if result is True:
                print(p)
            elif isinstance(result, list):
                failed = True
                for why in result:
                    print(f'{p}: {why}', file=sys.stderr)
    sys.exit(1 if failed else 0)


if __name__ == '__main__':
    main()
