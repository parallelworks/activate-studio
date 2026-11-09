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
echo "platform=$(uname -s 2>/dev/null) $(uname -m 2>/dev/null)"
echo "tools=$(cd "$HOME/.activate-studio/go" 2>/dev/null && for d in */ok; do [ -f "$d" ] && printf '%s ' "${d%/ok}"; done)"
echo "embeds=$(cd "$HOME/.activate-studio/embed" 2>/dev/null && for d in */ok; do [ -f "$d" ] && printf '%s ' "${d%/ok}"; done)"
m="$HOME/.activate-studio/models/minilm384.gguf"; [ -f "$m" ] && echo "model=$m"
exit 0