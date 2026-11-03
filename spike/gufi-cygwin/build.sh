#!/usr/bin/env bash
# GUFI Cygwin build spike: build GUFI under Cygwin on a Windows runner.
#
#   build.sh recipe          Debug build with the flags of the reference recipe
#   build.sh release         Release build as the macOS payload builds it, then install and payload.sh
#   build.sh ctest           Release build on the full package list, then GUFI's ctest suite
#
# CMAKE_FROM=source builds CMake 4.2.1 from source first (default: Cygwin's own package).
# Run from Cygwin bash (bash -o igncr), from the repository checkout.
set -eo pipefail
MODE="${1:?mode}"
export PATH="/usr/bin:$(cygpath "$SYSTEMROOT")/system32"
HERE="$(cd "$(dirname "$0")" && pwd)"
WS="$(cygpath -u "$GITHUB_WORKSPACE")"
RES="$WS/results"; mkdir -p "$RES"
SUM="$(cygpath -u "$GITHUB_STEP_SUMMARY")"
NOTES="$RES/build-$MODE.md"
JOBS=4
GUFI_REF="${GUFI_REF:-0.8.0}"
DEP="$HOME/.local"
mkdir -p "$DEP"
DEP="$(realpath "$DEP")"

note() { echo "$*" | tee -a "$NOTES" >>"$SUM"; }
now() { date +%s; }

note "## build.sh $MODE (cmake from: ${CMAKE_FROM:-cygwin package})"
note ""
note "- runner: $(uname -a)"
note "- gcc: $(gcc --version | head -1)"
note "- cygwin1.dll: $(cygcheck -c cygwin | tail -1)"

if [ "${CMAKE_FROM:-}" = source ]; then
  s=$(now)
  cd "$HOME"
  curl -fsSLO https://github.com/Kitware/CMake/releases/download/v4.2.1/cmake-4.2.1.tar.gz
  tar xzf cmake-4.2.1.tar.gz
  cd cmake-4.2.1
  ./bootstrap --prefix="$DEP" --parallel=$JOBS -- -DCMAKE_USE_OPENSSL=OFF >"$RES/cmake-bootstrap.log" 2>&1
  make -j$JOBS >>"$RES/cmake-bootstrap.log" 2>&1
  make install >>"$RES/cmake-bootstrap.log" 2>&1
  export PATH="$DEP/bin:$PATH"
  note "- cmake built from source: $(( $(now) - s )) s"
fi
note "- cmake: $(cmake --version | head -1) at $(command -v cmake)"

cd "$HOME"
s=$(now)
git clone -q https://github.com/mar-file-system/GUFI.git
cd GUFI
git checkout -q "$GUFI_REF"
note "- GUFI $(git describe --tags --always) ($(git rev-parse HEAD)), clone and checkout $(( $(now) - s )) s"
# The reference recipe pins Python 3.9 (its sqlalchemy package is not found with
# Cygwin's default 3.12); GUFI's own CI does the same. The release job leaves the default.
case "$MODE" in recipe|ctest) ln -sf /usr/bin/python3.9 /usr/bin/python3 ;; esac
if [ "$MODE" = ctest ]; then
  # As GUFI's own Cygwin CI does. Not fatal here: the log shows why it fails if it does.
  set +e
  python3 -m pip install -r contrib/CI/requirements.txt >"$RES/pip-install.log" 2>&1
  note "- pip install -r contrib/CI/requirements.txt: exit $? (log in pip-install.log)"
  set -e
fi
note "- python3 -> $(readlink -f /usr/bin/python3) ($(python3 --version 2>&1)); sqlalchemy importable: $(python3 -c 'import sqlalchemy; print(sqlalchemy.__version__)' 2>&1 | tail -1)"
mkdir build
cd build

# What the build tree needs on PATH to run its own binaries, as GUFI's
# CMake warns: the dependency DLL directories (Cygwin ignores LD_LIBRARY_PATH).
deppath() {
  local d out=""
  for d in "$DEP/sqlite-vec/lib" "$DEP/sqlite-lembed/lib" "$DEP/llama.cpp/bin" "$DEP/sqlite3/bin" "$DEP/sqlite3-pcre" "$DEP/sqlite3/lib"; do
    [ -d "$d" ] && out="$out:$d"
  done
  echo "${out#:}"
}

case "$MODE" in
  recipe)
    CONFIG=(-DDEP_INSTALL_PREFIX="$DEP" -DDEP_USE_JEMALLOC=Off -DDEP_AI=On -DCMAKE_BUILD_TYPE=Debug -DDEP_BUILD_THREADS=4
            -DCMAKE_C_FLAGS="-Wall -Wextra -Wpedantic" -DCMAKE_CXX_FLAGS="-Wall -Wextra -Wpedantic")
    ;;
  release|ctest)
    CONFIG=(-DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$HOME/gufi-install" -DDEP_INSTALL_PREFIX="$DEP"
            -DDEP_USE_JEMALLOC=Off -DDEP_AI=On -DDEP_BUILD_THREADS=$JOBS -DENABLE_SUDO_TESTS=Off)
    ;;
  *) echo "unknown mode $MODE"; exit 2 ;;
esac
note "- cmake flags: \`${CONFIG[*]}\`"

s=$(now)
cmake .. "${CONFIG[@]}" 2>&1 | tee "$RES/cmake-configure-$MODE.log" | tail -25
note "- configure: $(( $(now) - s )) s"

s=$(now)
set +e
make -j$JOBS >"$RES/make-$MODE.log" 2>&1
rc=$?
set -e
note "- make -j$JOBS: $(( $(now) - s )) s, exit $rc"
if [ $rc -ne 0 ]; then
  tail -60 "$RES/make-$MODE.log"
  note "- BUILD FAILED, last lines of the log are in make-$MODE.log"
  exit $rc
fi
tail -3 "$RES/make-$MODE.log"
note "- built tools: $(find . -name 'gufi_*.exe' | wc -l) gufi_*.exe in the build tree"
note "- dependency DLLs under $DEP: $(find "$DEP" -name '*.dll' | wc -l)"

case "$MODE" in
  recipe)
    export PATH="$PATH:$(deppath)"
    note "- build tree size: $(du -sm "$HOME/GUFI/build" | cut -f1) MB; $DEP: $(du -sm "$DEP" | cut -f1) MB"
    "$(find . -name gufi_query.exe | head -1)" -h 2>&1 | head -3 | tee -a "$NOTES" || true
    ctest -N | tail -3 | tee -a "$NOTES"
    ;;
  release)
    export PATH="$PATH:$(deppath)"
    s=$(now)
    cmake --install . >"$RES/install.log" 2>&1
    note "- cmake --install: $(( $(now) - s )) s"
    bash -o igncr "$HERE/payload.sh" "$HOME/gufi-install" "$DEP" "$WS/payload/gufi" "$RES"
    ;;
  ctest)
    export PATH="$PATH:$(deppath)"
    s=$(now)
    set +e
    ctest --output-on-failure --timeout 900 -E dsi 2>&1 | tee "$RES/ctest-full.log" | tail -n 60
    rc=${PIPESTATUS[0]}
    set -e
    note "- ctest -E dsi: $(( $(now) - s )) s, exit $rc"
    grep -E "tests passed|tests failed" "$RES/ctest-full.log" | tee -a "$NOTES" >>"$SUM" || true
    { echo; echo "Failed or timed-out tests:"; sed -n '/The following tests FAILED/,$p' "$RES/ctest-full.log"; } | tee -a "$NOTES" >>"$SUM"
    ctest -N -E dsi | tail -n 2 | tee -a "$NOTES"
    ctest -N | grep -c dsi | sed 's/^/dsi tests excluded: /' | tee -a "$NOTES"
    note "- tests that import sqlalchemy or pugsql:"
    grep -rl -E "sqlalchemy|pugsql" "$HOME/GUFI/test" | sed 's/^/  /' | tee -a "$NOTES"
    # keep the large log out of the artifact beyond its tail
    tail -n 3000 "$RES/ctest-full.log" >"$RES/ctest-tail.log"; rm -f "$RES/ctest-full.log"
    exit 0
    ;;
esac
