s="$HOME/.activate-studio/builds/$1"
[ -d "$s" ] || { echo state=missing; exit 0; }
pid=$(cat "$s/pid" 2>/dev/null)
host=$(cat "$s/host" 2>/dev/null)
alive=1
if [ -n "$pid" ] && { [ -z "$host" ] || [ "$host" = "$(uname -n)" ]; } && ! kill -0 "$pid" 2>/dev/null; then alive=0; fi
echo "phase=$(cat "$s/phase" 2>/dev/null)"
if [ -f "$s/exit" ]; then
  echo "exit=$(cat "$s/exit")"
  [ -f "$s/error" ] && echo "error=$(head -c 400 "$s/error" | tr '\n' ' ')"
  if [ -f "$2/.studio-index.json" ]; then echo __MANIFEST__; cat "$2/.studio-index.json"; fi
  exit 0
fi
if [ "$alive" = 0 ]; then echo state=gone; echo "log=$(tail -n 3 "$s/log" 2>/dev/null | tr '\n' ' ')"; exit 0; fi
[ "$(cat "$s/phase" 2>/dev/null)" = index ] && echo "dirs=$(find "$2/.build" -name db.db 2>/dev/null | wc -l | tr -d ' ')"
exit 0