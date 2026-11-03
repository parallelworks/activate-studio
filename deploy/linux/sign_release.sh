#!/usr/bin/env bash
# Sign a release of the Linux payload: write the manifest its updater reads,
# and the manifest's Ed25519 signature, beside the archive. The same key and
# the same signature as the macOS app's (deploy/mac/sign_release.sh); the
# manifest differs in minGlibc, the oldest glibc the payload runs on, in
# place of minMacos.
#
# Usage: deploy/linux/sign_release.sh ARCHIVE [VERSION]
#   ARCHIVE   activate-studio-linux-x86_64.tar.gz, as build_app.sh writes it
#   VERSION   the release, vMAJOR.MINOR[.PATCH]; by default the version the
#             payload in the archive reports (build-info.json) less any
#             -N-gHASH
#
# The private key (PKCS#8 PEM, Ed25519) is STUDIO_UPDATE_SIGNING_KEY, the
# PEM text, or the file STUDIO_UPDATE_SIGNING_KEY_FILE. It has to match the
# public key apps carry, deploy/mac/update-signing.pub (the payload carries
# it as update-signing.pub; STUDIO_UPDATE_PUBLIC_KEY names another). Signing
# needs OpenSSL 3 or later (OPENSSL names it).
#
# Writes ARCHIVE's name with .manifest.json and .manifest.json.sig (base64)
# in place of .tar.gz.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TOP=activate-studio

fail() { echo "sign_release: $*" >&2; exit 1; }

[ $# -ge 1 ] && [ $# -le 2 ] || fail "usage: sign_release.sh ARCHIVE [VERSION]"
[ -f "$1" ] || fail "no such archive: $1"
ARCHIVE="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
case "$ARCHIVE" in *.tar.gz) ;; *) fail "the archive is not a .tar.gz: $ARCHIVE" ;; esac
MANIFEST="${ARCHIVE%.tar.gz}.manifest.json"
PUB="${STUDIO_UPDATE_PUBLIC_KEY:-$HERE/../mac/update-signing.pub}"
[ -f "$PUB" ] || fail "no public key at $PUB"

openssl3() {
  local c
  for c in "${OPENSSL:-}" openssl /opt/homebrew/opt/openssl@3/bin/openssl /usr/local/opt/openssl@3/bin/openssl; do
    if [ -n "$c" ] && command -v "$c" >/dev/null 2>&1; then
      case "$("$c" version 2>/dev/null)" in "OpenSSL "[3-9]*) echo "$c"; return 0 ;; esac
    fi
  done
  return 1
}
OPENSSL_BIN="$(openssl3)" || fail "OpenSSL 3 or later is needed (set OPENSSL to its path)"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/sign-release.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# The key goes to openssl through a file only this user can read, and is
# never echoed.
if [ -n "${STUDIO_UPDATE_SIGNING_KEY:-}" ]; then
  KEY="$WORK/key.pem"
  (umask 077 && printf '%s\n' "$STUDIO_UPDATE_SIGNING_KEY" >"$KEY")
elif [ -n "${STUDIO_UPDATE_SIGNING_KEY_FILE:-}" ]; then
  KEY="$STUDIO_UPDATE_SIGNING_KEY_FILE"
  [ -f "$KEY" ] || fail "no such key file: $KEY"
else
  fail "set STUDIO_UPDATE_SIGNING_KEY (the private key's PEM text) or STUDIO_UPDATE_SIGNING_KEY_FILE"
fi
"$OPENSSL_BIN" pkey -in "$KEY" -pubout -out "$WORK/derived.pub" 2>/dev/null || fail "the signing key is not a private key openssl can read"
body() { grep -v '^-----' "$1" | tr -d ' \r\n'; }
[ "$(body "$WORK/derived.pub")" = "$(body "$PUB")" ] || fail "the signing key does not match the public key apps carry ($PUB)"

# What the payload in the archive says about itself.
info="$(tar -xzOf "$ARCHIVE" "$TOP/app/build-info.json" 2>/dev/null)" \
  || fail "the archive has no $TOP/app/build-info.json"
tree="$(tar -xzOf "$ARCHIVE" "$TOP/manifest.json" 2>/dev/null)" || fail "the archive has no $TOP/manifest.json"
tar -xzOf "$ARCHIVE" "$TOP/update-signing.pub" 2>/dev/null | cmp -s - "$PUB" \
  || fail "the payload's update-signing.pub is not the key $PUB; installed copies could not verify the next update"
json_field() { printf '%s\n' "$info" | awk -F'"' -v k="$1" '$2 == k { print $4; exit }'; }
release_of() { printf '%s' "$1" | sed 's/-[0-9][0-9]*-g[0-9a-f][0-9a-f]*$//'; }
built="$(json_field version)"
built_at="$(json_field builtAt)"
min_glibc="$(printf '%s\n' "$tree" | awk -F'"' '$2 == "minGlibc" { print $4; exit }')"
[ -n "$min_glibc" ] || fail "the payload's manifest.json has no minGlibc"
version="${2:-$(release_of "$built")}"
printf '%s' "$version" | grep -Eq '^v[0-9]+\.[0-9]+(\.[0-9]+)?$' || fail "$version is not a release version (vMAJOR.MINOR[.PATCH])"
[ "$(release_of "$built")" = "$version" ] || fail "the payload in the archive is ${built:-of no version}, not $version"

if command -v sha256sum >/dev/null 2>&1; then sum="$(sha256sum "$ARCHIVE" | awk '{print $1}')"; else sum="$(shasum -a 256 "$ARCHIVE" | awk '{print $1}')"; fi
size="$(( $(wc -c <"$ARCHIVE") ))"
printf '{\n "version": "%s",\n "asset": "%s",\n "sha256": "%s",\n "size": %s,\n "minGlibc": "%s",\n "builtAt": "%s"\n}\n' \
  "$version" "$(basename "$ARCHIVE")" "$sum" "$size" "$min_glibc" "${built_at:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" >"$MANIFEST"

# The signature covers the manifest's exact bytes.
"$OPENSSL_BIN" pkeyutl -sign -inkey "$KEY" -rawin -in "$MANIFEST" -out "$WORK/sig.bin" || fail "openssl could not sign the manifest"
"$OPENSSL_BIN" pkeyutl -verify -pubin -inkey "$PUB" -rawin -in "$MANIFEST" -sigfile "$WORK/sig.bin" >/dev/null \
  || fail "the signature does not verify with $PUB"
printf '%s\n' "$("$OPENSSL_BIN" base64 -A -in "$WORK/sig.bin")" >"$MANIFEST.sig"

echo "Signed $version:"
echo "  $MANIFEST"
echo "  $MANIFEST.sig"
