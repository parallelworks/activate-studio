// Package stepmesh meshes STEP files as Node's model.ts does: with
// occt-import-js (OpenCascade compiled to WebAssembly by emscripten), the
// same module run under wazero, with the emscripten imports it calls
// written here in place of its JavaScript glue.
//
// The module is the package's occt-import-js.wasm put through
// scripts/occt/prepare.mjs, which rewrites its exception handling into the
// form wazero runs, adds exported trampolines for the embind invokers and
// records the glue's import and export names in a custom section
// (studio.occt). Each file is meshed in a fresh instance; the compiled
// module is kept, and with a cache directory kept on disk too.
package stepmesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

// Mesh is one mesh of a STEP file, as model.ts's parseStep keeps it.
type Mesh struct {
	Name      string     // "" for none
	Color     []any      // the [r, g, b] array as the module set it, or nil
	Positions []float32  // x, y, z per vertex
	Normals   []float32  // nil when the mesh has none
	Indices   []uint32   // three per triangle
}

// ErrUnconvertible is model.ts's 422: the module ran and found no geometry.
var ErrUnconvertible = errors.New("could not convert STEP geometry")

// section is the studio.occt custom section.
type section struct {
	Package string            `json:"package"`
	Imports map[string]string `json:"imports"` // minified name: glue name
	Exports map[string]string `json:"exports"` // glue name: minified name
}

// Mesher holds the compiled modules. One file at a time is meshed.
type Mesher struct {
	mu   sync.Mutex
	rt   wazero.Runtime
	full *module // exception handling as built
	fast *module // exception handling stripped, or nil
	// Log receives what the module writes to stdout and stderr.
	Log func(string)
}

type module struct {
	compiled wazero.CompiledModule
	sec      section
}

// New compiles the prepared module at wasmPath and, when it is there, its
// <name>.noeh.wasm beside it. cacheDir, when not "", keeps the compiled
// code between runs.
func New(ctx context.Context, wasmPath, cacheDir string) (*Mesher, error) {
	cfg := wazero.NewRuntimeConfig().
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling | experimental.CoreFeaturesExtendedConst | experimental.CoreFeaturesTailCall).
		WithCustomSections(true).
		WithCloseOnContextDone(true)
	if cacheDir != "" {
		if cache, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil {
			cfg = cfg.WithCompilationCache(cache)
		}
	}
	m := &Mesher{rt: wazero.NewRuntimeWithConfig(ctx, cfg)}
	var err error
	if m.full, err = m.compile(ctx, wasmPath); err != nil {
		m.rt.Close(ctx)
		return nil, err
	}
	fast := strings.TrimSuffix(wasmPath, ".wasm") + ".noeh.wasm"
	if _, statErr := os.Stat(fast); statErr == nil {
		if m.fast, err = m.compile(ctx, fast); err != nil {
			m.rt.Close(ctx)
			return nil, err
		}
	}
	return m, nil
}

func (m *Mesher) compile(ctx context.Context, path string) (*module, error) {
	wasm, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	compiled, err := m.rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", path, err)
	}
	mod := &module{compiled: compiled}
	for _, cs := range compiled.CustomSections() {
		if cs.Name() == "studio.occt" {
			if err := json.Unmarshal(cs.Data(), &mod.sec); err != nil {
				return nil, fmt.Errorf("%s: studio.occt section: %w", path, err)
			}
		}
	}
	if mod.sec.Imports == nil {
		return nil, errors.New(path + " was not prepared by scripts/occt/prepare.mjs (no studio.occt section)")
	}
	return mod, nil
}

// Package is the occt-import-js version the module came from.
func (m *Mesher) Package() string { return m.full.sec.Package }

// Close frees the runtime.
func (m *Mesher) Close(ctx context.Context) error { return m.rt.Close(ctx) }

// FullTimeout bounds the run on the module with exception handling, which
// wazero runs about a hundred times slower than Node runs it.
const FullTimeout = 3 * time.Minute

// ErrTooComplex is a file the full module did not finish in FullTimeout.
var ErrTooComplex = errors.New("this STEP file is too complex to preview")

// ReadStep is ReadStepFile(new Uint8Array(buf), null), the meshes as
// parseStep keeps them. The module without exception handling runs first;
// when it traps (a C++ throw, which it cannot catch), the full one runs, for
// at most FullTimeout (ErrTooComplex past it).
func (m *Mesher) ReadStep(ctx context.Context, buf []byte) ([]Mesh, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readStep(ctx, buf, m.read, FullTimeout)
}

// readStep is ReadStep with the run and the bound given, for the tests.
func (m *Mesher) readStep(ctx context.Context, buf []byte, read func(context.Context, *module, []byte) ([]Mesh, error), full time.Duration) ([]Mesh, error) {
	if m.fast != nil {
		result, err := read(ctx, m.fast, buf)
		if err == nil || ctx.Err() != nil {
			return result, err
		}
	}
	fctx, cancel := context.WithTimeout(ctx, full)
	defer cancel()
	result, err := read(fctx, m.full, buf)
	if err != nil && ctx.Err() == nil && fctx.Err() != nil {
		return nil, ErrTooComplex
	}
	return result, err
}

func (m *Mesher) read(ctx context.Context, mod *module, buf []byte) ([]Mesh, error) {
	in := &instance{m: m, mod: mod, ev: newEmval(), types: map[uint32]*wireType{}, fns: map[string]*embindFn{}}
	result, err := in.run(ctx, "ReadStepFile", &bytesView{b: buf}, nil)
	if err != nil {
		return nil, err
	}
	return meshesOf(result)
}

// meshesOf is parseStep's reading of the result object.
func meshesOf(result any) ([]Mesh, error) {
	r, _ := result.(*jsObject)
	if r == nil || !truthy(r.get("success")) {
		return nil, ErrUnconvertible
	}
	list, _ := r.get("meshes").(*jsArray)
	if list == nil || len(list.items) == 0 {
		return nil, ErrUnconvertible
	}
	var out []Mesh
	for _, it := range list.items {
		mo, ok := deref(it).(*jsObject)
		if !ok {
			return nil, typeError("a mesh is not an object")
		}
		var mesh Mesh
		// name: m.name || undefined
		if v := mo.get("name"); truthy(v) {
			if s, ok := v.(string); ok {
				mesh.Name = s
			} else {
				mesh.Name = propertyKey(v)
			}
		}
		// color: m.color ?? undefined
		if c, ok := mo.get("color").(*jsArray); ok {
			for _, x := range c.items {
				if x == nil {
					mesh.Color = append(mesh.Color, nil)
				} else {
					mesh.Color = append(mesh.Color, deref(x))
				}
			}
			if mesh.Color == nil {
				mesh.Color = []any{}
			}
		}
		attrs, _ := mo.get("attributes").(*jsObject)
		if attrs == nil {
			return nil, typeError("Cannot read properties of undefined (reading 'position')")
		}
		pos, err := floats(attrs.get("position"))
		if err != nil {
			return nil, err
		}
		mesh.Positions = pos
		if n := attrs.get("normal"); truthy(n) {
			if mesh.Normals, err = floats(n); err != nil {
				return nil, err
			}
		}
		idx, _ := mo.get("index").(*jsObject)
		if idx == nil {
			return nil, typeError("Cannot read properties of undefined (reading 'array')")
		}
		arr, _ := idx.get("array").(*jsArray)
		if arr == nil {
			return nil, typeError("index.array is not an array")
		}
		mesh.Indices = make([]uint32, len(arr.items))
		for i, x := range arr.items {
			mesh.Indices[i] = toUint32(elem(x))
		}
		out = append(out, mesh)
	}
	return out, nil
}

// elem is an array element as a number (a hole is undefined: NaN).
func elem(x any) float64 {
	if x == nil {
		return math.NaN()
	}
	return toNumber(deref(x))
}

// floats is new Float32Array(attr.array).
func floats(attr any) ([]float32, error) {
	o, _ := attr.(*jsObject)
	if o == nil {
		return nil, typeError("Cannot read properties of undefined (reading 'array')")
	}
	arr, _ := o.get("array").(*jsArray)
	if arr == nil {
		return nil, typeError("attribute array is not an array")
	}
	out := make([]float32, len(arr.items))
	for i, x := range arr.items {
		out[i] = float32(elem(x))
	}
	return out, nil
}

// wireType is a type embind registered: how a value crosses as a wire
// value (toWireType, for emval::as) and is read from memory
// (readValueFromPointer, for _emval_take_value).
type wireType struct {
	name      string
	kind      string // void, bool, int, bigint, float, string, wstring, emval, view
	size      uint32
	signed    bool
	unsigned  bool // the C++ name says unsigned: toWireType is value >>> 0
	trueVal   uint32
	falseVal  uint32
	utf8      bool   // std::string (else std::basic_string<unsigned char>)
	viewKind  uint32 // memory_view's data type index
}

type embindFn struct {
	argCount  uint32
	signature string
	invoker   uint32
	fn        uint32
}

// instance is one run of the module.
type instance struct {
	m     *Mesher
	mod   *module
	inst  api.Module
	mem   api.Memory
	ev    *emval
	types map[uint32]*wireType
	fns   map[string]*embindFn
	out   []byte
	fail  error
}

// trap ends the run with err from inside a host function.
func (in *instance) trap(err error) {
	if in.fail == nil {
		in.fail = err
	}
	panic(err)
}

func (in *instance) run(ctx context.Context, name string, args ...any) (any, error) {
	host := in.m.rt.NewHostModuleBuilder("a")
	for _, f := range in.mod.compiled.ImportedFunctions() {
		modName, field, _ := f.Import()
		if modName != "a" {
			return nil, fmt.Errorf("unexpected import module %q", modName)
		}
		glue := in.mod.sec.Imports[field]
		impl := in.impl(glue)
		if impl == nil {
			return nil, fmt.Errorf("no implementation of the module's import %s (%s)", field, glue)
		}
		host.NewFunctionBuilder().WithGoModuleFunction(impl, f.ParamTypes(), f.ResultTypes()).Export(field)
	}
	hostMod, err := host.Instantiate(ctx)
	if err != nil {
		return nil, err
	}
	defer hostMod.Close(ctx)
	// The glue's own start: the module has no start function and no main;
	// its constructors (embind's registrations) run from __wasm_call_ctors.
	inst, err := in.m.rt.InstantiateModule(ctx, in.mod.compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return nil, err
	}
	defer inst.Close(ctx)
	in.inst = inst
	in.mem = inst.ExportedMemory(in.mod.sec.Exports["wasmMemory"])
	if in.mem == nil {
		return nil, errors.New("the module exports no memory")
	}
	if _, err := in.call(ctx, in.mod.sec.Exports["___wasm_call_ctors"]); err != nil {
		return nil, err
	}
	fn := in.fns[name]
	if fn == nil {
		return nil, fmt.Errorf("the module registers no %s", name)
	}
	if int(fn.argCount) != len(args)+1 {
		return nil, fmt.Errorf("%s takes %d arguments", name, fn.argCount-1)
	}
	params := []uint64{uint64(fn.invoker), uint64(fn.fn)}
	for _, a := range args {
		params = append(params, uint64(in.ev.toHandle(a)))
	}
	res, err := in.call(ctx, "__studio_call_"+fn.signature, params...)
	if err != nil {
		return nil, err
	}
	h := uint32(res[0])
	v, err := in.ev.toValue(h)
	if err != nil {
		return nil, err
	}
	in.ev.decref(h)
	return v, nil
}

func (in *instance) call(ctx context.Context, export string, params ...uint64) ([]uint64, error) {
	f := in.inst.ExportedFunction(export)
	if f == nil {
		return nil, fmt.Errorf("the module exports no %s", export)
	}
	res, err := f.Call(ctx, params...)
	if in.fail != nil {
		return nil, in.fail
	}
	if err != nil {
		return nil, fmt.Errorf("occt-import-js: %w", err)
	}
	return res, nil
}

// Memory helpers.

func (in *instance) u8(p uint32) byte {
	b, _ := in.mem.ReadByte(p)
	return b
}

func (in *instance) u16(p uint32) uint16 {
	v, _ := in.mem.ReadUint16Le(p)
	return v
}

func (in *instance) u32(p uint32) uint32 {
	v, _ := in.mem.ReadUint32Le(p)
	return v
}

func (in *instance) putU32(p, v uint32) { in.mem.WriteUint32Le(p, v) }

// latin1 is readLatin1String: bytes to the first NUL, one character each.
func (in *instance) latin1(p uint32) string {
	var r []rune
	for {
		b := in.u8(p)
		if b == 0 {
			return string(r)
		}
		r = append(r, rune(b))
		p++
	}
}

// i53 is convertI32PairToI53Checked.
func i53(lo, hi uint32) float64 {
	if uint32(int32(hi)+2097152) < 4194305-boolToU32(lo != 0) {
		return float64(lo) + float64(int32(hi))*4294967296
	}
	return math.NaN()
}

func boolToU32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// Emscripten's errno values.
const (
	eBADF   = 8
	eINVAL  = 28
	eNOENT  = 44
	eNOSYS  = 52
	eNOTTY  = 59
	eSPIPE  = 70
)

func neg(errno int32) uint64 { return uint64(uint32(-errno)) }

// heapMax is getHeapMax().
const heapMax = 2147483648

func (in *instance) impl(glue string) api.GoModuleFunction {
	type fn = func(ctx context.Context, mod api.Module, stack []uint64)
	var f fn
	switch glue {
	// Files: the module's file system is empty (Node's MEMFS holds nothing
	// the module did not write), so a path is not there. The standard
	// streams are the only descriptors.
	case "___syscall_openat", "___syscall_stat64", "___syscall_lstat64", "___syscall_newfstatat",
		"___syscall_chmod", "___syscall_faccessat", "___syscall_rmdir", "___syscall_unlinkat":
		f = func(_ context.Context, _ api.Module, s []uint64) { s[0] = neg(eNOENT) }
	case "___syscall_fcntl64", "___syscall_fstat64":
		f = func(_ context.Context, _ api.Module, s []uint64) {
			if uint32(s[0]) <= 2 {
				s[0] = neg(eNOSYS)
				return
			}
			s[0] = neg(eBADF)
		}
	case "___syscall_ioctl":
		f = func(_ context.Context, _ api.Module, s []uint64) {
			if uint32(s[0]) <= 2 {
				s[0] = neg(eNOTTY)
				return
			}
			s[0] = neg(eBADF)
		}
	case "_fd_close", "_fd_read":
		f = func(_ context.Context, _ api.Module, s []uint64) {
			if uint32(s[0]) <= 2 {
				s[0] = 0
				if glue == "_fd_read" {
					in.putU32(uint32(s[3]), 0)
				}
				return
			}
			s[0] = eBADF
		}
	case "_fd_seek":
		f = func(_ context.Context, _ api.Module, s []uint64) {
			if uint32(s[0]) <= 2 {
				s[0] = eSPIPE
				return
			}
			s[0] = eBADF
		}
	case "_fd_write":
		f = func(_ context.Context, _ api.Module, s []uint64) {
			fd, iov, iovcnt, pnum := uint32(s[0]), uint32(s[1]), uint32(s[2]), uint32(s[3])
			if fd != 1 && fd != 2 {
				s[0] = eBADF
				return
			}
			var n uint32
			for i := uint32(0); i < iovcnt; i++ {
				ptr, l := in.u32(iov+i*8), in.u32(iov+i*8+4)
				b, _ := in.mem.Read(ptr, l)
				in.write(b)
				n += l
			}
			in.putU32(pnum, n)
			s[0] = 0
		}
	case "__abort_js":
		f = func(context.Context, api.Module, []uint64) { in.trap(errors.New("occt-import-js aborted")) }
	case "_exit":
		f = func(_ context.Context, _ api.Module, s []uint64) {
			in.trap(fmt.Errorf("occt-import-js exited with status %d", int32(s[0])))
		}
	case "__emscripten_get_now_is_monotonic":
		f = func(_ context.Context, _ api.Module, s []uint64) { s[0] = 1 }
	case "_emscripten_date_now":
		f = func(_ context.Context, _ api.Module, s []uint64) {
			s[0] = math.Float64bits(float64(time.Now().UnixMilli()))
		}
	case "_emscripten_get_callstack", "__emscripten_lookup_name":
		f = func(_ context.Context, _ api.Module, s []uint64) { s[0] = 0 }
	case "__munmap_js":
		f = func(_ context.Context, _ api.Module, s []uint64) { s[0] = 0 }
	case "__emscripten_memcpy_js":
		f = func(_ context.Context, _ api.Module, s []uint64) {
			dest, src, n := uint32(s[0]), uint32(s[1]), uint32(s[2])
			b, ok := in.mem.Read(src, n)
			if ok {
				c := append([]byte(nil), b...)
				in.mem.Write(dest, c)
			}
		}
	case "_emscripten_get_heap_max":
		f = func(_ context.Context, _ api.Module, s []uint64) { s[0] = heapMax }
	case "_emscripten_resize_heap":
		f = func(_ context.Context, _ api.Module, s []uint64) { s[0] = uint64(boolToU32(in.resizeHeap(uint32(s[0])))) }
	case "_environ_sizes_get", "_environ_get":
		f = func(_ context.Context, _ api.Module, s []uint64) {
			env := environ()
			if glue == "_environ_sizes_get" {
				var size uint32
				for _, e := range env {
					size += uint32(len(e)) + 1
				}
				in.putU32(uint32(s[0]), uint32(len(env)))
				in.putU32(uint32(s[1]), size)
			} else {
				ptrs, buf := uint32(s[0]), uint32(s[1])
				for i, e := range env {
					in.putU32(ptrs+uint32(i)*4, buf)
					in.mem.Write(buf, append([]byte(e), 0))
					buf += uint32(len(e)) + 1
				}
			}
			s[0] = 0
		}
	case "__tzset_js":
		f = func(_ context.Context, _ api.Module, s []uint64) { in.tzset(uint32(s[0]), uint32(s[1]), uint32(s[2]), uint32(s[3])) }
	case "__localtime_js":
		f = func(_ context.Context, _ api.Module, s []uint64) { in.localtime(i53(uint32(s[0]), uint32(s[1])), uint32(s[2])) }
	default:
		f = in.embindImpl(glue)
	}
	if f == nil {
		return nil
	}
	return api.GoModuleFunc(f)
}

func (in *instance) write(b []byte) {
	in.out = append(in.out, b...)
	for {
		i := -1
		for j, c := range in.out {
			if c == '\n' {
				i = j
				break
			}
		}
		if i < 0 {
			return
		}
		if in.m.Log != nil {
			in.m.Log(string(in.out[:i]))
		}
		in.out = in.out[i+1:]
	}
}

// resizeHeap is _emscripten_resize_heap: the same overgrowth steps, so the
// heap grows as it does under Node.
func (in *instance) resizeHeap(requested uint32) bool {
	old := float64(in.mem.Size())
	if float64(requested) > heapMax {
		return false
	}
	for cutDown := 1.0; cutDown <= 4; cutDown *= 2 {
		over := old * (1 + .2/cutDown)
		over = math.Min(over, float64(requested)+100663296)
		size := math.Min(heapMax, math.Ceil(math.Max(float64(requested), over)/65536)*65536)
		pages := uint32((size - old + 65535) / 65536)
		if _, ok := in.mem.Grow(pages); ok {
			return true
		}
	}
	return false
}

// environ is getEnvStrings() under Node with LANG from
// navigator.languages[0], here the C locale.
func environ() []string {
	return []string{"USER=web_user", "LOGNAME=web_user", "PATH=/", "PWD=/", "HOME=/home/web_user", "LANG=C.UTF-8", "_=./this.program"}
}

// tzset is __tzset_js with the clock in UTC: timezone 0, no daylight
// saving, both names UTC-0000 (extractZone(0)).
func (in *instance) tzset(timezone, daylight, stdName, dstName uint32) {
	in.putU32(timezone, 0)
	in.putU32(daylight, 0)
	in.mem.Write(stdName, []byte("UTC-0000\x00"))
	in.mem.Write(dstName, []byte("UTC-0000\x00"))
}

// localtime is __localtime_js in UTC.
func (in *instance) localtime(t float64, tm uint32) {
	d := time.UnixMilli(int64(t * 1000)).UTC()
	vals := []int32{int32(d.Second()), int32(d.Minute()), int32(d.Hour()), int32(d.Day()), int32(d.Month()) - 1, int32(d.Year() - 1900), int32(d.Weekday()), int32(d.YearDay() - 1)}
	for i, v := range vals {
		in.putU32(tm+uint32(i)*4, uint32(v))
	}
	in.putU32(tm+32, 0) // tm_isdst
	in.putU32(tm+36, 0) // tm_gmtoff
}

