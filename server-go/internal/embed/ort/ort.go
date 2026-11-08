// Package ort is a small binding to ONNX Runtime's C API, loaded at run
// time (dlopen on Linux and macOS, LoadLibrary on Windows) through purego,
// so the server builds with CGO_ENABLED=0 and runs without the library:
// only what loads it fails. It covers what the embedder needs and nothing
// more: an environment, sessions with thread counts, and Run over int64
// input tensors with a float32 output.
//
// The C API is a table of function pointers (OrtApi) that only ever grows,
// so a function's index is fixed across releases. The indexes below are
// those of onnxruntime_c_api.h; the table is asked for at APIVersion,
// which every library from 1.14 on serves.
package ort

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// APIVersion is the OrtApi version asked for: 1.14's. Every function used
// here is older, and libraries from 1.14 on (the macOS x86_64 build stops
// at 1.23) serve it.
const APIVersion = 14

// Indexes into OrtApi (onnxruntime_c_api.h).
const (
	fnGetErrorMessage                = 2
	fnCreateEnv                      = 3
	fnDisableTelemetryEvents         = 6
	fnCreateSession                  = 7
	fnRun                            = 9
	fnCreateSessionOptions           = 10
	fnSetSessionGraphOptimizationLvl = 23
	fnSetIntraOpNumThreads           = 24
	fnSetInterOpNumThreads           = 25
	fnSessionGetInputCount           = 30
	fnSessionGetOutputCount          = 31
	fnSessionGetInputName            = 36
	fnSessionGetOutputName           = 37
	fnCreateTensorAsOrtValue         = 48
	fnGetTensorMutableData           = 51
	fnGetDimensionsCount             = 61
	fnGetDimensions                  = 62
	fnGetTensorTypeAndShape          = 65
	fnAllocatorFree                  = 76
	fnGetAllocatorWithDefaultOptions = 78
	fnReleaseEnv                     = 92
	fnReleaseStatus                  = 93
	fnReleaseSession                 = 95
	fnReleaseValue                   = 96
	fnReleaseTensorTypeAndShapeInfo  = 99
	fnReleaseSessionOptions          = 100
	tableSize                        = 101
	loggingLevelError                = 3
	graphOptimizationAll             = 99
	tensorInt64                      = 7
	tensorFloat                      = 1
)

// Runtime is a loaded library with one environment.
type Runtime struct {
	path    string
	version string
	fns     [tableSize]uintptr
	env     uintptr
	alloc   uintptr
}

var (
	loadMu sync.Mutex
	loaded = map[string]*Runtime{}
)

// Load opens the library at path (once per path; later calls return the
// same Runtime) and creates its environment.
func Load(path string) (*Runtime, error) {
	loadMu.Lock()
	defer loadMu.Unlock()
	if r := loaded[path]; r != nil {
		return r, nil
	}
	sym, err := openLibrary(path)
	if err != nil {
		return nil, fmt.Errorf("onnxruntime: %s: %w", path, err)
	}
	base, _, _ := purego.SyscallN(sym)
	if base == 0 {
		return nil, fmt.Errorf("onnxruntime: %s: OrtGetApiBase returned nothing", path)
	}
	// struct OrtApiBase { const OrtApi* (*GetApi)(uint32_t); const char* (*GetVersionString)(void); }
	getAPI := *(*uintptr)(cptr(base))
	getVersion := *(*uintptr)(unsafe.Add(cptr(base), unsafe.Sizeof(uintptr(0))))
	r := &Runtime{path: path}
	if v, _, _ := purego.SyscallN(getVersion); v != 0 {
		r.version = goString(v)
	}
	api, _, _ := purego.SyscallN(getAPI, APIVersion)
	if api == 0 {
		return nil, fmt.Errorf("onnxruntime: %s (version %s) does not serve API version %d", path, r.version, APIVersion)
	}
	for i := range r.fns {
		r.fns[i] = *(*uintptr)(unsafe.Add(cptr(api), uintptr(i)*unsafe.Sizeof(uintptr(0))))
	}
	logid := cString("studio")
	var env uintptr
	if err := r.status(r.call(fnCreateEnv, loggingLevelError, uintptr(unsafe.Pointer(&logid[0])), uintptr(unsafe.Pointer(&env)))); err != nil {
		return nil, err
	}
	runtime.KeepAlive(logid)
	r.env = env
	_ = r.status(r.call(fnDisableTelemetryEvents, env))
	if err := r.status(r.call(fnGetAllocatorWithDefaultOptions, uintptr(unsafe.Pointer(&r.alloc)))); err != nil {
		return nil, err
	}
	loaded[path] = r
	return r, nil
}

// Version is the library's version string, such as 1.31.0.
func (r *Runtime) Version() string { return r.version }

// Path is the file the library was loaded from.
func (r *Runtime) Path() string { return r.path }

// call calls OrtApi function fn. Pointers converted to uintptr in its
// arguments are kept on the heap until it returns, as for SyscallN.
//
//go:uintptrescapes
func (r *Runtime) call(fn int, args ...uintptr) uintptr {
	ret, _, _ := purego.SyscallN(r.fns[fn], args...)
	return ret
}

// status turns an OrtStatus* into an error, releasing it.
func (r *Runtime) status(st uintptr) error {
	if st == 0 {
		return nil
	}
	msg := "onnxruntime error"
	if p := r.call(fnGetErrorMessage, st); p != 0 {
		msg = "onnxruntime: " + goString(p)
	}
	r.call(fnReleaseStatus, st)
	return errors.New(msg)
}

// Session is one loaded model. Run may be called from several goroutines
// at once, as ONNX Runtime allows.
type Session struct {
	r       *Runtime
	h       uintptr
	inputs  []string
	outputs []string
	mu      sync.RWMutex
	closed  bool
}

// SessionOptions are a session's thread counts. Zero means 1.
type SessionOptions struct {
	IntraOpThreads int
	InterOpThreads int
}

// NewSession loads the model file at path.
func (r *Runtime) NewSession(model string, o SessionOptions) (*Session, error) {
	var opts uintptr
	if err := r.status(r.call(fnCreateSessionOptions, uintptr(unsafe.Pointer(&opts)))); err != nil {
		return nil, err
	}
	defer r.call(fnReleaseSessionOptions, opts)
	intra, inter := max(o.IntraOpThreads, 1), max(o.InterOpThreads, 1)
	for _, st := range []uintptr{
		r.call(fnSetIntraOpNumThreads, opts, uintptr(intra)),
		r.call(fnSetInterOpNumThreads, opts, uintptr(inter)),
		r.call(fnSetSessionGraphOptimizationLvl, opts, graphOptimizationAll),
	} {
		if err := r.status(st); err != nil {
			return nil, err
		}
	}
	p := pathString(model)
	var h uintptr
	err := r.status(r.call(fnCreateSession, r.env, uintptr(unsafe.Pointer(&p[0])), opts, uintptr(unsafe.Pointer(&h))))
	runtime.KeepAlive(p)
	if err != nil {
		return nil, err
	}
	s := &Session{r: r, h: h}
	if s.inputs, err = s.names(fnSessionGetInputCount, fnSessionGetInputName); err == nil {
		s.outputs, err = s.names(fnSessionGetOutputCount, fnSessionGetOutputName)
	}
	if err != nil {
		r.call(fnReleaseSession, h)
		return nil, err
	}
	return s, nil
}

func (s *Session) names(countFn, nameFn int) ([]string, error) {
	r := s.r
	var n uintptr
	if err := r.status(r.call(countFn, s.h, uintptr(unsafe.Pointer(&n)))); err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		var p uintptr
		if err := r.status(r.call(nameFn, s.h, i, r.alloc, uintptr(unsafe.Pointer(&p)))); err != nil {
			return nil, err
		}
		out = append(out, goString(p))
		r.call(fnAllocatorFree, r.alloc, p)
	}
	return out, nil
}

// Inputs are the model's input names, in order.
func (s *Session) Inputs() []string { return s.inputs }

// Outputs are the model's output names, in order.
func (s *Session) Outputs() []string { return s.outputs }

// Close releases the session. Runs still going must have returned.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.r.call(fnReleaseSession, s.h)
	}
}

// Int64Input is one input tensor: a name, a shape and its values in row
// order (len(Data) is the product of Shape).
type Int64Input struct {
	Name  string
	Shape []int64
	Data  []int64
}

// RunFloat runs the model on int64 inputs and returns the output named
// output (a float32 tensor), with its shape.
func (s *Session) RunFloat(inputs []Int64Input, output string) ([]float32, []int64, error) {
	r := s.r
	values := make([]uintptr, len(inputs))
	defer func() {
		for _, v := range values {
			if v != 0 {
				r.call(fnReleaseValue, v)
			}
		}
	}()
	var pin runtime.Pinner
	defer pin.Unpin()
	names := make([]uintptr, len(inputs))
	for i, in := range inputs {
		n := 1
		for _, d := range in.Shape {
			n *= int(d)
		}
		if n != len(in.Data) {
			return nil, nil, fmt.Errorf("onnxruntime: input %s: %d values for shape %v", in.Name, len(in.Data), in.Shape)
		}
		shape := append([]int64(nil), in.Shape...)
		pin.Pin(&shape[0])
		var v uintptr
		if err := r.status(r.call(fnCreateTensorAsOrtValue, r.alloc, uintptr(unsafe.Pointer(&shape[0])), uintptr(len(shape)), tensorInt64, uintptr(unsafe.Pointer(&v)))); err != nil {
			return nil, nil, err
		}
		values[i] = v
		var data uintptr
		if err := r.status(r.call(fnGetTensorMutableData, v, uintptr(unsafe.Pointer(&data)))); err != nil {
			return nil, nil, err
		}
		if n > 0 {
			copy(unsafe.Slice((*int64)(cptr(data)), n), in.Data)
		}
		name := cString(in.Name)
		pin.Pin(&name[0])
		names[i] = uintptr(unsafe.Pointer(&name[0]))
	}
	outName := cString(output)
	pin.Pin(&outName[0])
	outNames := []uintptr{uintptr(unsafe.Pointer(&outName[0]))}
	pin.Pin(&outNames[0])
	pin.Pin(&names[0])
	pin.Pin(&values[0])
	outs := make([]uintptr, 1)
	pin.Pin(&outs[0])
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, nil, errors.New("onnxruntime: session closed")
	}
	st := r.call(fnRun, s.h, 0, uintptr(unsafe.Pointer(&names[0])), uintptr(unsafe.Pointer(&values[0])), uintptr(len(values)),
		uintptr(unsafe.Pointer(&outNames[0])), 1, uintptr(unsafe.Pointer(&outs[0])))
	s.mu.RUnlock()
	if err := r.status(st); err != nil {
		return nil, nil, err
	}
	out := outs[0]
	defer r.call(fnReleaseValue, out)
	var info uintptr
	if err := r.status(r.call(fnGetTensorTypeAndShape, out, uintptr(unsafe.Pointer(&info)))); err != nil {
		return nil, nil, err
	}
	var nd uintptr
	err := r.status(r.call(fnGetDimensionsCount, info, uintptr(unsafe.Pointer(&nd))))
	dims := make([]int64, max(int(nd), 1))
	if err == nil && nd > 0 {
		err = r.status(r.call(fnGetDimensions, info, uintptr(unsafe.Pointer(&dims[0])), nd))
	}
	r.call(fnReleaseTensorTypeAndShapeInfo, info)
	if err != nil {
		return nil, nil, err
	}
	dims = dims[:nd]
	n := 1
	for _, d := range dims {
		n *= int(d)
	}
	var data uintptr
	if err := r.status(r.call(fnGetTensorMutableData, out, uintptr(unsafe.Pointer(&data)))); err != nil {
		return nil, nil, err
	}
	res := make([]float32, n)
	if n > 0 {
		copy(res, unsafe.Slice((*float32)(cptr(data)), n))
	}
	return res, dims, nil
}

func cString(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

func goString(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(cptr(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(cptr(p)), n))
}

// cptr is a pointer to C memory, as unsafe.Pointer.
func cptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }
