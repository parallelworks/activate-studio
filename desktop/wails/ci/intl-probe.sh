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
dnf -y install dnf-plugins-core epel-release >/dev/null 2>&1
dnf config-manager --set-enabled crb 2>&1 | tail -n 1
dnf -y install python3 strace binutils file ltrace gdb >"$OUT/dnf-tools.log" 2>&1; echo "tools dnf exit $?"
dnf -y install webkit2gtk3 webkit2gtk3-jsc webkit2gtk3-devel; echo "dnf exit $?"
section "install result"
rpm -q webkit2gtk3 webkit2gtk3-jsc webkit2gtk3-devel libicu glibc libsoup || true
if ! rpm -q webkit2gtk3 >/dev/null 2>&1; then
  section "why not: webkit2gtk3 is not installable here"
  dnf -y install webkit2gtk3 2>&1 | tail -n 4
  section "webkit packages the enabled repos offer"
  dnf list available '*webkit*' 2>&1 | tail -n 8
  echo "webkit2gtk3 could not be installed in this image" >"$OUT/NO_WEBKIT"
  dnf -y install webkit2gtk3-jsc 2>&1 | tail -n 2
  rpm -q webkit2gtk3-jsc || exit 0
fi
JSC=/usr/libexec/webkit2gtk-4.0/jsc
section "package facts"
rpm -qi webkit2gtk3 | grep -E "^(Name|Version|Release|Build Date|Build Host|Vendor|Packager|Signature|From repo)" || true
dnf info --installed webkit2gtk3 2>/dev/null | grep -E "From repo|Repository" || true
rpm -qi libicu | grep -E "^(Version|Release|Build Date|Vendor|Signature)"
section "ICU libraries JavaScriptCore loads"
ldd /usr/lib64/libjavascriptcoregtk-4.0.so.18 | grep -iE "icu"
ls -la /usr/lib64/libicudata.so* /usr/lib64/libicui18n.so* /usr/lib64/libicuuc.so*
rpm -V libicu 2>&1 | head; echo "rpm -V libicu exit $?"
section "ICU data file and env"
env | grep -E "^(ICU|LANG|LC_)" || echo "no ICU_, LANG or LC_ variable"
ls -la /usr/share/icu* 2>&1 | head
section "ICU symbols the jsc binary and JavaScriptCore import"
nm -D --undefined-only "$JSC" 2>/dev/null | grep -E "_6[0-9]$" | sed 's/^ *//' | sort | head -n 5
nm -D --undefined-only /usr/lib64/libjavascriptcoregtk-4.0.so.18 | grep -oE " u[a-z0-9_]+_67$" | sort -u | wc -l
nm -D --undefined-only /usr/lib64/libjavascriptcoregtk-4.0.so.18 | grep -E "unum|unumf|ulocdata|uldn|uplrules|ufmt|udat_" | sed 's/^ *//' | sort -u | head -n 60

section "C++ ICU number classes JavaScriptCore imports"
nm -D -C --undefined-only /usr/lib64/libjavascriptcoregtk-4.0.so.18 | grep -E "icu_67::number" | sed 's/^ *//' | sort -u | head -n 30
nm -D -C --undefined-only /usr/lib64/libjavascriptcoregtk-4.0.so.18 | grep -c "icu_67" 

section "ICU used directly (ctypes, no JavaScriptCore)"
python3 - <<'PY'
import ctypes
from ctypes import c_int, c_void_p, c_char_p, c_double, byref, POINTER, c_int32, c_uint16
i18n = ctypes.CDLL("libicui18n.so.67")
def U(s): return (c_uint16 * (len(s) + 1))(*[ord(c) for c in s])
i18n.unum_open_67.restype = c_void_p
i18n.unum_open_67.argtypes = [c_int, c_void_p, c_int32, c_char_p, c_void_p, POINTER(c_int)]
i18n.unum_formatDouble_67.restype = c_int32
i18n.unum_formatDouble_67.argtypes = [c_void_p, c_double, c_void_p, c_int32, c_void_p, POINTER(c_int)]
status = c_int(0)
for loc in (b"en_US", b"en", b"en_US_POSIX", b"root"):
    status.value = 0
    f = i18n.unum_open_67(1, None, 0, loc, None, byref(status))
    ok = status.value
    out = ""
    if f and ok <= 0:
        buf = (c_uint16 * 64)()
        status.value = 0
        n = i18n.unum_formatDouble_67(f, 1234.5, buf, 64, None, byref(status))
        out = "".join(chr(buf[k]) for k in range(max(n, 0)))
    print("unum_open", loc, "status", ok, "format status", status.value, repr(out), flush=True)
# the number-skeleton API, one token at a time (the C API takes UTF-16)
try:
    fn = i18n.unumf_openForSkeletonAndLocale_67
    fn.restype = c_void_p
    fn.argtypes = [c_void_p, c_int32, c_char_p, POINTER(c_int)]
    for tok in ["", "precision-integer", "rounding-mode-half-up", "rounding-mode-half-even", "sign-auto", "group-auto", "percent scale/100",
                "integer-width/*000", "numbering-system/latn", "compact-short", "currency/USD", "unit-width-short",
                "precision-increment/0.05", "rounding-mode-half-expand", "rounding-mode-half-odd", "rounding-mode-ceiling",
                "trailing-zero-display/hide-if-whole", "rounding-priority/auto", ".00", ".##/@@@+", "scale/100", "measure-unit/length-meter", "unit/meter"]:
        u = U(tok); status.value = 0
        h = fn(u, len(tok), b"en_US", byref(status))
        print("unumf skeleton %-40r status %d %s" % (tok, status.value, "ok" if h and status.value <= 0 else "REJECTED"), flush=True)
except Exception as e:
    print("unumf not available:", e)
PY

section "JavaScriptCore through its GObject API (works with only webkit2gtk3-jsc installed), one statement per process"
cat >"$OUT/jscpy.py" <<'PY'
import ctypes, sys
from ctypes import c_void_p, c_char_p, c_ssize_t
lib = ctypes.CDLL("libjavascriptcoregtk-4.0.so.18")
lib.jsc_context_new.restype = c_void_p
lib.jsc_context_evaluate.restype = c_void_p
lib.jsc_context_evaluate.argtypes = [c_void_p, c_char_p, c_ssize_t]
lib.jsc_context_get_exception.restype = c_void_p
lib.jsc_context_get_exception.argtypes = [c_void_p]
lib.jsc_exception_get_message.restype = c_char_p
lib.jsc_exception_get_message.argtypes = [c_void_p]
lib.jsc_value_to_string.restype = c_char_p
lib.jsc_value_to_string.argtypes = [c_void_p]
ctx = lib.jsc_context_new()
v = lib.jsc_context_evaluate(ctx, sys.argv[1].encode(), -1)
exc = lib.jsc_context_get_exception(ctx)
if exc:
    print("EXCEPTION", lib.jsc_exception_get_message(exc).decode())
else:
    print("OK", lib.jsc_value_to_string(v).decode())
PY
run_js() {
  local label=$1 code=$2; shift 2
  out=$(env "$@" timeout 30 python3 "$OUT/jscpy.py" "$code" 2>&1); st=$?
  echo "[$label] exit=$st :: $(echo "$out" | tail -n 2 | tr '\n' ' ' | cut -c1-150)"
}
for envset in "A=1" "LANG=en_US.UTF-8" "LC_ALL=C.UTF-8"; do
  echo "--- env $envset"
  run_js "NumberFormat()"            'new Intl.NumberFormat().format(1234.5)' $envset
  run_js "NumberFormat('en-US')"     'new Intl.NumberFormat("en-US").format(1234.5)' $envset
  run_js "NumberFormat percent"      'new Intl.NumberFormat("en-US",{style:"percent"}).format(0.5)' $envset
  run_js "supportedLocalesOf"        'JSON.stringify(Intl.NumberFormat.supportedLocalesOf(["en-US","de","fr"]))' $envset
  run_js "toLocaleString"            '(1234.5).toLocaleString()' $envset
  run_js "BigInt toLocaleString"     '(12345n).toLocaleString()' $envset
  run_js "DateTimeFormat"            'new Intl.DateTimeFormat("en-US").format(new Date(0))' $envset
  run_js "Date toLocaleString"       'new Date(0).toLocaleString()' $envset
  run_js "Collator"                  'new Intl.Collator("en-US").compare("a","b")' $envset
  run_js "PluralRules"               'new Intl.PluralRules("en-US").select(1)' $envset
  run_js "RelativeTimeFormat"        'new Intl.RelativeTimeFormat("en-US").format(1,"day")' $envset
  run_js "ListFormat"                'new Intl.ListFormat("en-US").format(["a","b"])' $envset
  run_js "DisplayNames"              'new Intl.DisplayNames("en-US",{type:"region"}).of("US")' $envset
  run_js "Segmenter"                 'typeof Intl.Segmenter' $envset
done

section "ICU calls made while the constructor fails (ltrace on the _67 functions)"
ltrace -f -x '*_67' -o "$OUT/ltrace.txt" python3 "$OUT/jscpy.py" 'new Intl.NumberFormat("en-US").format(1)' 2>&1 | tail -n 2
grep -E "unum|unumsys|uloc|ucurr|uplrules|ures|u_errorName" "$OUT/ltrace.txt" | head -n 40 | cut -c1-220
echo "-- last 25 ICU calls before the exception:"
tail -n 25 "$OUT/ltrace.txt" | cut -c1-220

section "jsc options that mention Intl or ICU"
[ -x "$JSC" ] && "$JSC" --options 2>&1 | grep -iE "intl|icu" | head -n 20

section "strace of the failing constructor (files opened, errors)"
strace -f -e trace=openat,open,access -o "$OUT/strace.txt" python3 "$OUT/jscpy.py" 'new Intl.NumberFormat("en-US").format(1)' 2>&1 | tail -n 2
grep -iE "icu|\.dat|locale" "$OUT/strace.txt" | grep -v ENOENT | head -n 10 | cut -c1-200
echo "-- ENOENT lines that mention icu, dat or locale:"
grep -iE "icu|\.dat|locale" "$OUT/strace.txt" | grep ENOENT | head -n 10 | cut -c1-200

section "with glibc langpacks"
dnf -y install glibc-langpack-en langpacks-en glibc-all-langpacks 2>&1 | tail -n 3
for envset in "LANG=en_US.UTF-8" "LC_ALL=en_US.UTF-8"; do
  run_js "NumberFormat('en-US') $envset" 'new Intl.NumberFormat("en-US").format(1234.5)' $envset
done

section "other ICU-related packages available"
dnf list available 'libicu*' 'icu*' 2>&1 | head -n 20
dnf list installed '*icu*' 2>&1 | head
echo done
