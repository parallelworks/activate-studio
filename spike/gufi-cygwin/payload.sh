#!/usr/bin/env bash
# Assemble the Windows payload from a GUFI install: the tools, every DLL they
# load (GUFI's dependencies and the Cygwin runtime, found with cygcheck), the
# attr tools the Studio calls (getfattr, setfattr), and an extras directory
# with sh.exe, which gufi_vt needs and the smoke test adds on purpose.
#   payload.sh INSTALL_PREFIX DEP_PREFIX OUT_DIR RESULTS_DIR
set -eo pipefail
INST="$1"; DEP="$2"; OUT="$3"; RES="$4"
SUM="$(cygpath -u "$GITHUB_STEP_SUMMARY")"
note() { echo "$*" | tee -a "$RES/payload.md" >>"$SUM"; }

echo "=== install tree ===";   (cd "$INST" && find . | sort | head -150)
echo "=== dependency DLLs ==="; find "$DEP" -name '*.dll' -o -name '*.dll.a' | sort
{ echo "install tree:"; (cd "$INST" && find . -type f | sort); echo; echo "dependency prefix DLLs:"; find "$DEP" -name '*.dll' | sort; } >"$RES/install-tree.txt"

rm -rf "$OUT"; mkdir -p "$OUT/bin" "$OUT/extras"
# GUFI's own files: tools, scripts, and any DLLs it installs.
cp -a "$INST/bin/." "$OUT/bin/"
find "$INST" -name '*.dll' -exec cp -n {} "$OUT/bin/" \;
# Dependency DLLs from the nested builds (not their build trees).
find "$DEP" -name '*.dll' -not -path '*/builds/*' -exec cp -n {} "$OUT/bin/" \;
# The attr tools the Studio runs for labels.
for t in getfattr setfattr; do cp "/usr/bin/$t.exe" "$OUT/bin/"; done

# Every DLL the tools load, found with cygcheck.
deps_of() {  # deps_of DIR FILES...: copy the Cygwin runtime DLLs the files need into DIR
  local dest="$1"; shift
  local f d
  for f in "$@"; do
    PATH="$OUT/bin:$PATH" cygcheck "$f" 2>/dev/null | tr -d '\r' | sed 's/^ *//' | grep -i -E '\\cygwin\\bin\\.*\.dll$|\\cyg[^\\]*\.dll$' || true
  done | sort -u | while read -r d; do
    p="$(cygpath -u "$d")"
    [ -e "$dest/$(basename "$p")" ] || [ -e "$OUT/bin/$(basename "$p")" ] || cp "$p" "$dest/"
  done
}
# Twice: a DLL copied on the first pass has dependencies of its own.
mapfile -t files < <(ls "$OUT"/bin/*.exe "$OUT"/bin/*.dll)
deps_of "$OUT/bin" "${files[@]}"
mapfile -t files < <(ls "$OUT"/bin/*.exe "$OUT"/bin/*.dll)
deps_of "$OUT/bin" "${files[@]}"

# extras: sh.exe (a copy of bash) and what it needs that bin does not have.
cp /usr/bin/bash.exe "$OUT/extras/sh.exe"
deps_of "$OUT/extras" "$OUT/extras/sh.exe"

# Report.
{
  echo "# cygcheck for gufi_query.exe (as run from the payload)"
  PATH="$OUT/bin:$PATH" cygcheck "$OUT/bin/gufi_query.exe" | tr -d '\r'
  echo; echo "# cygcheck for gufi_sqlite3.exe"
  PATH="$OUT/bin:$PATH" cygcheck "$OUT/bin/gufi_sqlite3.exe" | tr -d '\r'
  echo; echo "# cygcheck for gufi_dir2index.exe"
  PATH="$OUT/bin:$PATH" cygcheck "$OUT/bin/gufi_dir2index.exe" | tr -d '\r'
} >"$RES/cygcheck.txt"

{
  echo "# DLLs in bin (name, bytes)"
  (cd "$OUT/bin" && stat -c '%s %n' *.dll | sort -k2)
  echo; echo "# executables in bin"
  (cd "$OUT/bin" && stat -c '%s %n' *.exe | sort -k2)
  echo; echo "# other files in bin"
  (cd "$OUT/bin" && for f in *; do case "$f" in *.dll|*.exe) ;; *) stat -c '%s %n' "$f" ;; esac; done)
  echo; echo "# extras"
  (cd "$OUT/extras" && stat -c '%s %n' *)
} >"$RES/payload-files.txt"
cat "$RES/payload-files.txt"

note ""
note "### Payload"
note "- DLLs in bin: $(ls "$OUT"/bin/*.dll | wc -l), of which cyg*.dll: $(ls "$OUT"/bin/cyg*.dll | wc -l)"
note "- executables and scripts in bin: $(ls "$OUT/bin" | grep -v -E '\.dll$' | wc -l)"
note "- payload size unpacked: $(du -sm "$OUT" | cut -f1) MB (bin $(du -sm "$OUT/bin" | cut -f1) MB, extras $(du -sm "$OUT/extras" | cut -f1) MB)"
note "- install prefix size: $(du -sm "$INST" | cut -f1) MB; dependency prefix: $(du -sm "$DEP" | cut -f1) MB"
# Launch check from a minimal PATH inside Cygwin.
env -i PATH="$OUT/bin:$(cygpath "$SYSTEMROOT")/system32" "$OUT/bin/gufi_query.exe" -h 2>&1 | head -3 || true
