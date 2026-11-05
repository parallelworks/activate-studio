# Versions and checksums the desktop apps' builds (deploy/mac, deploy/linux)
# have in common, sourced by their scripts so that one change moves every
# platform. Every download is checked against its SHA-256 on each build.
# What only one platform uses (macOS's libomp and GUFI package, each
# platform's Node and Python archives) is pinned in that platform's own
# scripts.

NODE_VERSION=v26.10.0    # as deploy/make_bundle.sh; package.json asks for Node 26
PY_TAG=20261013          # python-build-standalone release
PY_VERSION=3.12.15
MODEL_COMMIT=7a7bac37782986fe1d4f213de771a8a3d9170b35    # as deploy/app.def
MODEL_SHA256=71f1d177171468fb5f186c07019e303015aea17af275a67767760bba7be8d2e6
# The PDF reader for files downmark cannot read (indexer/enrich.py), pure
# Python, so it needs no build for any platform.
PYPDF_VERSION=6.21.2
PYPDF_URL=https://files.pythonhosted.org/packages/1b/4d/c9d2d67f204eff463237a1a8f5e25a6923b7b1043982b91fcaf104c8ef60/pypdf-6.21.2-py3-none-any.whl
PYPDF_SHA256=1d44d1a3a200f25062adff0085b02040e4646431ac2ccac22e42679e91466084

# GUFI 0.8.0 (its tag), built from source, with the sources it links.
GUFI_REPO=https://github.com/mar-file-system/GUFI
GUFI_COMMIT=021a717a6089b9e28b068695dc7c7f3732fd1a56
PCRE2_SRC_VERSION=10.49
PCRE2_SRC_SHA256=53c156e1ba416a20da8e65395daa132da0d80e76910424caca3fcdae7831d384

# OCR, built from source: tesseract, leptonica and the image libraries
# leptonica reads, with English from tessdata_fast.
TESSERACT_VERSION=5.5.3
TESSERACT_SHA256=9218e62793116d42a9f6d14cd9348518b27f382096eea3d0f2d1a24616bb5884
LEPTONICA_VERSION=1.87.0
LEPTONICA_SHA256=c73363397f96eb1295602bf44d708a994ad42046c791bf03ea0505d829bdb6a7
LIBPNG_VERSION=1.6.59
LIBPNG_SHA256=2540302a1844ad2b2b501977abecfa850f265f97b78f065a712ab4074a89f5b5
JPEG_TURBO_VERSION=3.2.0
JPEG_TURBO_SHA256=6f30092cef9fb839779646608f4ee14ae3cbac989c47fa05e841b0841f09878e
LIBTIFF_VERSION=4.7.2
LIBTIFF_SHA256=672bd7d10aee4606171afb864f3570b83340f6a33e2c186dc0512f7145ffdf6a
LIBWEBP_VERSION=1.6.0
LIBWEBP_SHA256=e4ab7009bf0629fd11982d4c2aa83964cf244cffba7347ecd39019a9e38c4564
GIFLIB_VERSION=6.1.3
GIFLIB_SHA256=b65b66b99f0424b93525f987386f22fc5efb9da2bfc92ad4a532249aaffbab0e
# English from tessdata_fast, the integer versions of tesseract's LSTM
# models, and that repository's license.
TESSDATA_TAG=4.1.0
ENG_SHA256=7d4322bd2a7749724879683fc3912cb542f19906c83bcc1a52132556427170b2
TESSDATA_LICENSE_SHA256=cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30

# The license texts of GCC's runtime libraries (libgomp and libatomic, which
# the Linux app ships), from the GCC 11 source tag: GPL 3 and the runtime
# library exception. Their package in the build container carries none.
GCC_LICENSE_TAG=releases/gcc-11.5.0
GCC_COPYING3_SHA256=8ceb4b9ee5adedde47b31e975c1d90c73ad27b6b165a1dcd80c7c545eb65b903
GCC_RUNTIME_EXCEPTION_SHA256=9d6b43ce4d8de0c878bf16b54d8e7a10d9bd42b75178153e3af6a815bdc90f74
