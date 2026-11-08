py=$1
echo "platform=$("$py" -c 'import platform, sys; m = platform.machine().lower(); a = {"amd64": "x86_64", "arm64": "aarch64"}.get(m, m); s = "macos" if sys.platform == "darwin" else "linux" if sys.platform.startswith("linux") else sys.platform; print("%s-%s-cp%d%d" % (s, "arm64" if s == "macos" and a == "aarch64" else a, sys.version_info[0], sys.version_info[1]))' 2>/dev/null)"
echo "embeds=$(cd "$HOME/.activate-studio/embed" 2>/dev/null && for d in */ok; do [ -f "$d" ] && printf '%s ' "${d%/ok}"; done)"
echo "readers=$(cd "$HOME/.activate-studio/readers" 2>/dev/null && for d in */ok; do [ -f "$d" ] && printf '%s ' "${d%/ok}"; done)"
exit 0