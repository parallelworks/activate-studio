s="$HOME/.activate-studio/builds/$1"
mkdir -p "$s" || exit 3
printf '%s\n' "$2" > "$s/run.sh"
rm -f "$s/exit" "$s/error" "$s/phase" "$s/pid" "$s/host"
if [ -n "$3" ]; then mkdir -p "$HOME/.activate-studio/go" && touch "$HOME/.activate-studio/go/$3.sending"; fi
if [ -n "$4" ]; then mkdir -p "$HOME/.activate-studio/embed" && touch "$HOME/.activate-studio/embed/$4.sending"; fi
shift 4
if command -v setsid >/dev/null 2>&1; then
  setsid nohup bash "$s/run.sh" "$s" "$@" </dev/null >"$s/log" 2>&1 &
else
  nohup bash "$s/run.sh" "$s" "$@" </dev/null >"$s/log" 2>&1 &
fi
echo started