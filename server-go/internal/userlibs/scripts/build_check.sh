near() { p=$1; while [ ! -e "$p" ] && [ "$p" != / ]; do p=$(dirname "$p"); done; printf '%s' "$p"; }
if [ -d "$1" ]; then
  if [ -r "$1" ] && [ -x "$1" ]; then echo source=ok; else echo source=unreadable; fi
  echo "source_real=$(cd "$1" 2>/dev/null && pwd -P)"
elif [ -e "$1" ]; then echo source=notdir
else a=$(near "$1"); if [ -d "$a" ] && [ -w "$a" ]; then echo source=create; else echo source=nocreate; fi; echo "source_at=$a"; fi
if [ -d "$2" ]; then
  if [ -f "$2/.studio-index.json" ]; then echo index=studio; echo "index_source=$(sed -n 's/^ *"source": *"\(.*\)", *$/\1/p' "$2/.studio-index.json" | head -n 1)"
  elif [ -n "$(ls -A "$2" 2>/dev/null)" ]; then echo index=foreign
  elif [ -w "$2" ]; then echo index=empty
  else echo index=readonly; fi
  echo "index_real=$(cd "$2" 2>/dev/null && pwd -P)"
elif [ -e "$2" ]; then echo index=notdir
else a=$(near "$2"); if [ -d "$a" ] && [ -w "$a" ]; then echo index=create; else echo index=nocreate; fi; echo "index_at=$a"; fi
for p in python3.13 python3.12 python3.11 python3.10 python3; do
  c=$(command -v "$p" 2>/dev/null) || continue
  if "$c" -c 'import sqlite3, sys; sqlite3.connect(":memory:").execute("CREATE VIRTUAL TABLE t USING fts5(x)"); sys.exit(0 if sys.version_info >= (3, 10) else 1)' 2>/dev/null; then
    echo "python=$c"; echo "python_version=$("$c" -c 'import sys; print("%d.%d" % sys.version_info[:2])')"
    py=$c
echo "platform=$("$py" -c 'import platform, sys; m = platform.machine().lower(); a = {"amd64": "x86_64", "arm64": "aarch64"}.get(m, m); s = "macos" if sys.platform == "darwin" else "linux" if sys.platform.startswith("linux") else sys.platform; print("%s-%s-cp%d%d" % (s, "arm64" if s == "macos" and a == "aarch64" else a, sys.version_info[0], sys.version_info[1]))' 2>/dev/null)"
echo "embeds=$(cd "$HOME/.activate-studio/embed" 2>/dev/null && for d in */ok; do [ -f "$d" ] && printf '%s ' "${d%/ok}"; done)"
echo "readers=$(cd "$HOME/.activate-studio/readers" 2>/dev/null && for d in */ok; do [ -f "$d" ] && printf '%s ' "${d%/ok}"; done)"
    break
  fi
done
m="$HOME/.activate-studio/models/minilm384.gguf"; [ -f "$m" ] && echo "model=$m"
exit 0