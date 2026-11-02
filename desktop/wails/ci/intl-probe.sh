#!/bin/bash
# Run as root in an EL9 container: install the distro's webkit2gtk3, then find
# out whether its JavaScriptCore can build an Intl.NumberFormat, and look at
# the ICU it uses.
#
# Usage: intl-probe.sh OUT_DIR
set -u
OUT=$1
mkdir -p "$OUT"
exec >"$OUT/probe.txt" 2>&1
section() { echo; echo "== $*"; }

section "os-release"; cat /etc/os-release
section "repos before"; dnf repolist 2>&1
# EPEL where it can be had (not needed for webkit2gtk3, which is in AppStream).
section "install webkit2gtk3"
dnf -y install webkit2gtk3 webkit2gtk3-jsc python3 strace binutils file; echo "dnf exit $?"
section "install result"
rpm -q webkit2gtk3 webkit2gtk3-jsc libicu glibc libsoup || true
if ! rpm -q webkit2gtk3 >/dev/null 2>&1; then
  section "why not"
  dnf -y install webkit2gtk3 2>&1 | tail -n 5
  dnf provides webkit2gtk3 2>&1 | tail -n 5
  echo "webkit2gtk3 could not be installed in this image" >"$OUT/NO_WEBKIT"
  exit 0
fi
JSC=/usr/libexec/webkit2gtk-4.0/jsc
section "package facts"
rpm -qi webkit2gtk3 | grep -E "^(Name|Version|Release|Build Date|Build Host|Vendor|Packager|Signature|From repo)" || true
dnf info --installed webkit2gtk3 2>/dev/null | grep -E "From repo|Repository" || true
rpm -qi libicu | grep -E "^(Version|Release|Build Date|Vendor|Signature)"
section "ICU libraries JavaScriptCore loads"
ldd "$JSC" | grep -iE "icu|javascriptcore"
ls -la /usr/lib64/libicudata.so* /usr/lib64/libicui18n.so* /usr/lib64/libicuuc.so*
rpm -V libicu 2>&1 | head; echo "rpm -V libicu exit $?"
section "ICU data file and env"
env | grep -E "^(ICU|LANG|LC_)" || echo "no ICU_, LANG or LC_ variable"
ls -la /usr/share/icu* 2>&1 | head
section "ICU symbols the jsc binary and JavaScriptCore import"
nm -D --undefined-only "$JSC" 2>/dev/null | grep -E "_6[0-9]$" | sed 's/^ *//' | sort | head -n 5
nm -D --undefined-only /usr/lib64/libjavascriptcoregtk-4.0.so.18 | grep -oE " u[a-z0-9_]+_67$" | sort -u | wc -l
nm -D --undefined-only /usr/lib64/libjavascriptcoregtk-4.0.so.18 | grep -E "unum|unumf|ulocdata|uldn|uplrules|ufmt|udat_" | sed 's/^ *//' | sort -u | head -n 60

section "ICU used directly (ctypes, no JavaScriptCore): unum_open and format"
python3 - <<'PY'
import ctypes
i18n = ctypes.CDLL("libicui18n.so.67")
uc = ctypes.CDLL("libicuuc.so.67")
status = ctypes.c_int(0)
for loc in (b"en_US", b"en", b"en_US_POSIX", b"root"):
    status.value = 0
    f = i18n.unum_open_67(1, None, 0, loc, None, ctypes.byref(status))
    ok = status.value
    out = ""
    if f and ok <= 0:
        buf = ctypes.create_unicode_buffer(64)
        status.value = 0
        n = i18n.unum_formatDouble_67(ctypes.c_void_p(f), ctypes.c_double(1234.5), buf, 64, None, ctypes.byref(status))
        out = buf.value
    print(loc, "unum_open status", ok, "format status", status.value, repr(out))
# the number-skeleton API (what newer JavaScriptCore uses)
status.value = 0
skel = "precision-integer".encode("utf-16-le")
try:
    fn = i18n.unumf_openForSkeletonAndLocale_67
    fn.restype = ctypes.c_void_p
    h = fn(ctypes.c_char_p(skel), -1, b"en_US", ctypes.byref(status))
    print("unumf_openForSkeletonAndLocale status", status.value, bool(h))
except Exception as e:
    print("unumf not available:", e)
PY

section "jsc, one statement per run, with exit status"
run_js() {
  local label=$1 code=$2; shift 2
  out=$(env "$@" timeout 20 "$JSC" -e "$code" 2>&1); st=$?
  echo "[$label] exit=$st :: $(echo "$out" | head -n 2 | tr '\n' ' ')"
}
for envset in "A=1" "LANG=en_US.UTF-8" "LC_ALL=C.UTF-8"; do
  echo "--- env $envset"
  run_js "NumberFormat()"            'print(new Intl.NumberFormat().format(1234.5))' $envset
  run_js "NumberFormat('en-US')"     'print(new Intl.NumberFormat("en-US").format(1234.5))' $envset
  run_js "NumberFormat('en')"        'print(new Intl.NumberFormat("en").format(1234.5))' $envset
  run_js "NumberFormat('und')"       'print(new Intl.NumberFormat("und").format(1234.5))' $envset
  run_js "supportedLocalesOf"        'print(JSON.stringify(Intl.NumberFormat.supportedLocalesOf(["en-US","de","fr"])))' $envset
  run_js "NumberFormat percent"      'print(new Intl.NumberFormat("en-US",{style:"percent"}).format(0.5))' $envset
  run_js "toLocaleString"            'print((1234.5).toLocaleString())' $envset
  run_js "BigInt toLocaleString"     'print((12345n).toLocaleString())' $envset
  run_js "DateTimeFormat"            'print(new Intl.DateTimeFormat("en-US").format(new Date(0)))' $envset
  run_js "Date toLocaleString"       'print(new Date(0).toLocaleString())' $envset
  run_js "Collator"                  'print(new Intl.Collator("en-US").compare("a","b"))' $envset
  run_js "PluralRules"               'print(new Intl.PluralRules("en-US").select(1))' $envset
  run_js "RelativeTimeFormat"        'print(new Intl.RelativeTimeFormat("en-US").format(1,"day"))' $envset
  run_js "ListFormat"                'print(new Intl.ListFormat("en-US").format(["a","b"]))' $envset
  run_js "DisplayNames"              'print(new Intl.DisplayNames("en-US",{type:"region"}).of("US"))' $envset
done

section "jsc options that mention Intl or ICU"
"$JSC" --options 2>&1 | grep -iE "intl|icu|temporal" | head -n 20

section "strace of the failing constructor (files opened, errors)"
strace -f -e trace=openat,open,access,stat,newfstatat -o "$OUT/strace.txt" "$JSC" -e 'try { print(new Intl.NumberFormat("en-US").format(1)) } catch (e) { print("error " + e) }' 2>&1 | head -n 3
grep -iE "icu|\.dat|locale|zoneinfo" "$OUT/strace.txt" | grep -v ENOENT | head -n 20 | cut -c1-200
echo "-- ENOENT lines that mention icu or dat:"
grep -iE "icu|\.dat|locale" "$OUT/strace.txt" | grep ENOENT | head -n 10 | cut -c1-200

section "with glibc langpacks"
dnf -y install glibc-langpack-en langpacks-en glibc-all-langpacks 2>&1 | tail -n 3
for envset in "LANG=en_US.UTF-8" "LC_ALL=en_US.UTF-8"; do
  run_js "NumberFormat('en-US') $envset" 'print(new Intl.NumberFormat("en-US").format(1234.5))' $envset
done

section "other ICU-related packages available"
dnf list available 'libicu*' 'icu*' 2>&1 | head -n 20
dnf list installed '*icu*' 2>&1 | head
echo done
