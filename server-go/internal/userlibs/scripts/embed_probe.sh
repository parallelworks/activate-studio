echo "platform=$(uname -s 2>/dev/null) $(uname -m 2>/dev/null)"
echo "tools=$(cd "$HOME/.activate-studio/go" 2>/dev/null && for d in */ok; do [ -f "$d" ] && printf '%s ' "${d%/ok}"; done)"
echo "embeds=$(cd "$HOME/.activate-studio/embed" 2>/dev/null && for d in */ok; do [ -f "$d" ] && printf '%s ' "${d%/ok}"; done)"
exit 0