#!/usr/bin/env bash
# A temporary keychain holding a code signing identity, for sign_app.sh and
# notarize.sh on a build machine (the release's signing job), where the
# login keychain is not to be touched.
#
#   keychain.sh create KEYCHAIN       a new keychain at KEYCHAIN (a path
#                                     ending .keychain-db), with the identity
#                                     in MACOS_SIGNING_P12 (the .p12 file,
#                                     base64) and MACOS_SIGNING_P12_PASSWORD
#   keychain.sh create-test KEYCHAIN  the same with a throwaway self-signed
#                                     identity made here; prints its SHA-1
#   keychain.sh developer-id KEYCHAIN TEAM
#                                     prints the SHA-1 of the valid
#                                     "Developer ID Application" identity of
#                                     TEAM in KEYCHAIN
#   keychain.sh delete KEYCHAIN       removes it
#
# The keychain gets a random password, is unlocked for an hour, lets
# codesign use its key without asking, and is added to the user's search
# list (so codesign finds the certificate chain). The .p12 is written only
# to a temporary directory, readable by this user, for as long as the
# import takes. Nothing secret is printed.
set -euo pipefail

fail() { echo "keychain: $*" >&2; exit 1; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/keychain.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

create() {
  local kc="$1" pass k
  [ -n "${MACOS_SIGNING_P12:-}" ] && [ -n "${MACOS_SIGNING_P12_PASSWORD:-}" ] ||
    fail "MACOS_SIGNING_P12 (the .p12, base64) and MACOS_SIGNING_P12_PASSWORD are needed"
  case "$kc" in /*.keychain-db) ;; *) fail "KEYCHAIN is an absolute path ending .keychain-db, not $kc" ;; esac
  [ ! -e "$kc" ] || fail "$kc exists"
  (umask 077 && printf '%s' "$MACOS_SIGNING_P12" | tr -d ' \r\n' | base64 --decode >"$WORK/identity.p12" 2>/dev/null) ||
    fail "MACOS_SIGNING_P12 is not base64"
  pass="$(/usr/bin/openssl rand -hex 24)"
  security create-keychain -p "$pass" "$kc"
  security set-keychain-settings -lut 3600 "$kc"
  security unlock-keychain -p "$pass" "$kc"
  if ! security import "$WORK/identity.p12" -k "$kc" -f pkcs12 -P "$MACOS_SIGNING_P12_PASSWORD" -T /usr/bin/codesign >/dev/null; then
    rm -f "$WORK/identity.p12"
    fail "security could not import the .p12 (is MACOS_SIGNING_P12_PASSWORD its password?)"
  fi
  rm -f "$WORK/identity.p12"
  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$pass" "$kc" >/dev/null
  local list=()
  while IFS= read -r k; do
    k="$(printf '%s' "$k" | sed 's/^[[:space:]]*"//; s/"[[:space:]]*$//')"
    if [ -n "$k" ] && [ "$k" != "$kc" ]; then list+=("$k"); fi
  done < <(security list-keychains -d user)
  security list-keychains -d user -s "$kc" ${list[@]+"${list[@]}"}
  echo "Keychain $kc, with:" >&2
  security find-identity -p codesigning "$kc" | awk '/Valid identities only/ { exit } sub(/^ *[0-9]+\) /, "  ")' >&2
}

create_test() {
  local kc="$1" pass
  cat >"$WORK/req.cnf" <<'CNF'
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = ACTIVATE Studio test signing
[ext]
basicConstraints = critical, CA:false
keyUsage = critical, digitalSignature
extendedKeyUsage = critical, codeSigning
CNF
  # The system's openssl (LibreSSL) writes a .p12 that security can read.
  /usr/bin/openssl req -x509 -newkey rsa:2048 -nodes -days 2 -config "$WORK/req.cnf" \
    -keyout "$WORK/key.pem" -out "$WORK/cert.pem" 2>/dev/null
  pass="$(/usr/bin/openssl rand -hex 16)"
  /usr/bin/openssl pkcs12 -export -inkey "$WORK/key.pem" -in "$WORK/cert.pem" -name "ACTIVATE Studio test signing" \
    -passout "pass:$pass" -out "$WORK/test.p12"
  MACOS_SIGNING_P12="$(base64 <"$WORK/test.p12" | tr -d '\n')" MACOS_SIGNING_P12_PASSWORD="$pass" create "$kc"
  /usr/bin/openssl x509 -in "$WORK/cert.pem" -noout -fingerprint -sha1 | sed 's/.*=//; s/://g'
}

developer_id() {
  local kc="$1" team="$2" id
  id="$(security find-identity -v -p codesigning "$kc" |
    awk -v suffix="($team)\"" 'index($0, "\"Developer ID Application: ") && substr($0, length($0) - length(suffix) + 1) == suffix { print $2; exit }')"
  if [ -z "$id" ]; then
    security find-identity -p codesigning "$kc" >&2
    fail "no valid Developer ID Application identity for team $team in $kc"
  fi
  printf '%s\n' "$id"
}

case "${1:-}" in
  create) [ $# -eq 2 ] || fail "usage: keychain.sh create KEYCHAIN"; create "$2" ;;
  create-test) [ $# -eq 2 ] || fail "usage: keychain.sh create-test KEYCHAIN"; create_test "$2" ;;
  developer-id) [ $# -eq 3 ] || fail "usage: keychain.sh developer-id KEYCHAIN TEAM"; developer_id "$2" "$3" ;;
  delete)
    [ $# -eq 2 ] || fail "usage: keychain.sh delete KEYCHAIN"
    security delete-keychain "$2" 2>/dev/null || true
    rm -f "$2"
    ;;
  *) sed -n '2,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; exit 2 ;;
esac
