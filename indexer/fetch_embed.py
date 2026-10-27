#!/usr/bin/env python3
"""Fetch what the ONNX embedding backend and the document readers need,
pinned in embed_deps.json.

  --deps DIR    onnxruntime, tokenizers and numpy wheels for one platform,
                and xlrd and olefile (pure Python, for .xls and .ppt files),
                unpacked into DIR (a site-packages, or indexer/.deps, which
                embed_onnx.py and enrich.py add to sys.path themselves);
                with --readers-only, xlrd and olefile alone
  --model DIR   the int8 all-MiniLM-L6-v2 ONNX model and its tokenizer, as
                DIR/model.onnx and DIR/tokenizer.json
  --download    the wheels for --platform and the model files into --cache
                alone, installing nothing: what the Studio sends to another
                system for a build there, which runs this script with that
                cache, --no-readers and no network

Every download is checked against its SHA-256, and kept in --cache so a
second run needs no network. --platform names the wheels to take (for
example macos-arm64-cp312 when the macOS app is built on another machine);
by default, those for the Python running this script. Standard library
only, so it runs before anything is installed.

Without these, embed.py and the server keep using the older GGUF model
through sqlite-lembed: search by meaning still works, slower.
"""
import argparse
import hashlib
import json
import platform
import shutil
import sys
import tempfile
import urllib.error
import urllib.request
import zipfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
PINS = json.loads((HERE / 'embed_deps.json').read_text())

# Parts of the packages the backend never imports: onnxruntime's model
# tooling and the packages' own test suites.
TRIM = ('onnxruntime/tools', 'onnxruntime/transformers', 'onnxruntime/quantization', 'onnxruntime/datasets',
        'numpy/tests', 'numpy/*/tests', 'numpy/_core/tests')


def this_platform() -> str:
    machine = platform.machine().lower()
    arch = {'amd64': 'x86_64', 'x86_64': 'x86_64', 'arm64': 'aarch64', 'aarch64': 'aarch64'}.get(machine, machine)
    system = sys.platform
    py = f'cp{sys.version_info.major}{sys.version_info.minor}'
    if system == 'darwin':
        return f'macos-{"arm64" if arch == "aarch64" else arch}-{py}'
    if system.startswith('linux'):
        return f'linux-{arch}-{py}'
    return f'{system}-{arch}-{py}'


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()


def fetch(url: str, want: str, cache: Path) -> Path:
    """A pinned download, from the cache when it is already there and intact."""
    cache.mkdir(parents=True, exist_ok=True)
    dest = cache / f'{want[:16]}-{url.rsplit("/", 1)[-1]}'
    if dest.exists() and sha256(dest) == want:
        return dest
    part = dest.with_name(dest.name + '.part')
    try:
        with urllib.request.urlopen(url, timeout=120) as r, part.open('wb') as f:
            shutil.copyfileobj(r, f)
    except (urllib.error.URLError, OSError) as exc:
        part.unlink(missing_ok=True)
        raise SystemExit(f'could not download {url}: {exc}. Without network, search by meaning keeps using the older model.')
    got = sha256(part)
    if got != want:
        part.unlink(missing_ok=True)
        raise SystemExit(f'{url} has SHA-256 {got}, not the pinned {want}; nothing was installed.')
    part.replace(dest)
    return dest


def install_deps(target: Path, plat: str, cache: Path) -> None:
    wheels = PINS['wheels'].get(plat)
    if not wheels:
        known = ', '.join(sorted(PINS['wheels']))
        raise SystemExit(f'no pinned wheels for {plat} (there are: {known}); search by meaning keeps using the older model.')
    target.mkdir(parents=True, exist_ok=True)
    for pkg, w in wheels.items():
        whl = fetch(w['url'], w['sha256'], cache)
        # A wheel is a zip laid out as site-packages wants it. An older copy
        # of the package goes first, so files it dropped do not linger.
        with zipfile.ZipFile(whl) as z:
            tops = {n.split('/', 1)[0] for n in z.namelist()}
            for top in tops:
                shutil.rmtree(target / top, ignore_errors=True)
            for info in z.infolist():
                z.extract(info, target)
                mode = (info.external_attr >> 16) & 0o777
                if mode:
                    (target / info.filename).chmod(mode)
        print(f'{pkg} {PINS["packages"][pkg]} into {target}')
    for pattern in TRIM:
        for p in target.glob(pattern):
            shutil.rmtree(p, ignore_errors=True)


def install_readers(target: Path, cache: Path) -> None:
    """xlrd and olefile: pure-Python wheels, the same on every platform."""
    target.mkdir(parents=True, exist_ok=True)
    for pkg, w in PINS['readers'].items():
        whl = fetch(w['url'], w['sha256'], cache)
        with zipfile.ZipFile(whl) as z:
            for top in {n.split('/', 1)[0] for n in z.namelist()}:
                shutil.rmtree(target / top, ignore_errors=True)
            z.extractall(target)
        print(f'{pkg} {w["version"]} into {target}')


def download(plat: str, cache: Path) -> None:
    """The wheels for a platform and the model files, into the cache only."""
    wheels = PINS['wheels'].get(plat)
    if not wheels:
        raise SystemExit(f'no pinned wheels for {plat}')
    for w in wheels.values():
        fetch(w['url'], w['sha256'], cache)
    for f in PINS['model']['files'].values():
        fetch(f['url'], f['sha256'], cache)
    print(f'the packages for {plat} and {PINS["model"]["id"]} in {cache}')


def install_model(target: Path, cache: Path) -> None:
    target.mkdir(parents=True, exist_ok=True)
    for name, f in PINS['model']['files'].items():
        src = fetch(f['url'], f['sha256'], cache)
        tmp = target / f'.{name}.part'
        shutil.copyfile(src, tmp)
        tmp.replace(target / name)
    (target / 'MODEL').write_text(PINS['model']['id'] + '\n')
    print(f'{PINS["model"]["id"]} into {target}')


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    ap.add_argument('--deps', help='unpack the wheels into this directory')
    ap.add_argument('--model', help='put model.onnx and tokenizer.json in this directory')
    ap.add_argument('--platform', default='', help=f'wheels for this platform (default {this_platform()})')
    ap.add_argument('--cache', default='', help='keep downloads here (default: a folder in the temp directory)')
    ap.add_argument('--readers-only', action='store_true', help='with --deps, only xlrd and olefile')
    ap.add_argument('--no-readers', action='store_true', help='with --deps, leave out xlrd and olefile')
    ap.add_argument('--download', action='store_true', help='only download the wheels for --platform and the model into --cache')
    args = ap.parse_args()
    if not args.deps and not args.model and not args.download:
        ap.error('give --deps, --model, or both, or --download')
    cache = Path(args.cache) if args.cache else Path(tempfile.gettempdir()) / 'studio-embed-cache'
    if args.download:
        download(args.platform or this_platform(), cache)
    if args.deps:
        if not args.no_readers:
            install_readers(Path(args.deps), cache)
        if not args.readers_only:
            install_deps(Path(args.deps), args.platform or this_platform(), cache)
    if args.model:
        install_model(Path(args.model), cache)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
