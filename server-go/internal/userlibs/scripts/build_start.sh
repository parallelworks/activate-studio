s="$HOME/.activate-studio/builds/$1"
mkdir -p "$s" || exit 3
printf '%s\n' "$2" > "$s/run.sh"
rm -f "$s/enrich.py" "$s/embed.py" "$s/embed_onnx.py" "$s/fetch_embed.py" "$s/embed_deps.json" "$s/exit" "$s/error" "$s/phase" "$s/pid" "$s/host"
[ -n "$3" ] && printf '%s\n' "$3" > "$s/enrich.py"
[ -n "$4" ] && printf '%s\n' "$4" > "$s/embed.py"
[ -n "$5" ] && printf '%s\n' "$5" > "$s/embed_onnx.py"
[ -n "$6" ] && printf '%s\n' "$6" > "$s/fetch_embed.py"
[ -n "$7" ] && printf '%s\n' "$7" > "$s/embed_deps.json"
if [ -n "$8" ]; then mkdir -p "$HOME/.activate-studio/embed" && touch "$HOME/.activate-studio/embed/$8.sending"; fi
if [ -n "$9" ]; then mkdir -p "$HOME/.activate-studio/readers" && touch "$HOME/.activate-studio/readers/$9.sending"; fi
shift 9
if command -v setsid >/dev/null 2>&1; then
  setsid nohup bash "$s/run.sh" "$s" "$@" </dev/null >"$s/log" 2>&1 &
else
  nohup bash "$s/run.sh" "$s" "$@" </dev/null >"$s/log" 2>&1 &
fi
echo started