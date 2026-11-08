package stepmesh

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/tetratelabs/wazero/api"
)

// embind's registrations and emval's operations, as the glue's
// __embind_* and __emval_* functions do them.

func (in *instance) register(raw uint32, t *wireType) { in.types[raw] = t }

func (in *instance) typeOf(raw uint32, what string) *wireType {
	t := in.types[raw]
	if t == nil {
		in.trap(bindingError(fmt.Sprintf("%s has unknown type %d", what, raw)))
	}
	return t
}

func (in *instance) value(h uint32) any {
	v, err := in.ev.toValue(h)
	if err != nil {
		in.trap(err)
	}
	return v
}

func (in *instance) embindImpl(glue string) func(ctx context.Context, mod api.Module, s []uint64) {
	u := func(s []uint64, i int) uint32 { return uint32(s[i]) }
	switch glue {
	case "__embind_register_void":
		return func(_ context.Context, _ api.Module, s []uint64) {
			in.register(u(s, 0), &wireType{name: in.latin1(u(s, 1)), kind: "void"})
		}
	case "__embind_register_bool":
		return func(_ context.Context, _ api.Module, s []uint64) {
			in.register(u(s, 0), &wireType{name: in.latin1(u(s, 1)), kind: "bool", trueVal: u(s, 2), falseVal: u(s, 3)})
		}
	case "__embind_register_integer":
		return func(_ context.Context, _ api.Module, s []uint64) {
			name := in.latin1(u(s, 1))
			in.register(u(s, 0), &wireType{name: name, kind: "int", size: u(s, 2), signed: int32(u(s, 3)) != 0, unsigned: strings.Contains(name, "unsigned")})
		}
	case "__embind_register_bigint":
		// The glue registers nothing for 64-bit integers.
		return func(context.Context, api.Module, []uint64) {}
	case "__embind_register_float":
		return func(_ context.Context, _ api.Module, s []uint64) {
			in.register(u(s, 0), &wireType{name: in.latin1(u(s, 1)), kind: "float", size: u(s, 2)})
		}
	case "__embind_register_std_string":
		return func(_ context.Context, _ api.Module, s []uint64) {
			name := in.latin1(u(s, 1))
			in.register(u(s, 0), &wireType{name: name, kind: "string", utf8: name == "std::string"})
		}
	case "__embind_register_std_wstring":
		return func(_ context.Context, _ api.Module, s []uint64) {
			in.register(u(s, 0), &wireType{name: in.latin1(u(s, 2)), kind: "wstring", size: u(s, 1)})
		}
	case "__embind_register_emval":
		return func(_ context.Context, _ api.Module, s []uint64) {
			in.register(u(s, 0), &wireType{name: "emscripten::val", kind: "emval"})
		}
	case "__embind_register_memory_view":
		return func(_ context.Context, _ api.Module, s []uint64) {
			in.register(u(s, 0), &wireType{name: in.latin1(u(s, 2)), kind: "view", viewKind: u(s, 1)})
		}
	case "__embind_register_function":
		return func(_ context.Context, _ api.Module, s []uint64) {
			name := in.latin1(u(s, 0))
			in.fns[name] = &embindFn{argCount: u(s, 1), signature: in.latin1(u(s, 3)), invoker: u(s, 4), fn: u(s, 5)}
		}
	case "__emval_new_object":
		return func(_ context.Context, _ api.Module, s []uint64) { s[0] = uint64(in.ev.toHandle(newObject())) }
	case "__emval_new_array":
		return func(_ context.Context, _ api.Module, s []uint64) { s[0] = uint64(in.ev.toHandle(&jsArray{})) }
	case "__emval_new_cstring":
		return func(_ context.Context, _ api.Module, s []uint64) { s[0] = uint64(in.ev.toHandle(in.latin1(u(s, 0)))) }
	case "__emval_get_global":
		return func(_ context.Context, _ api.Module, s []uint64) {
			if u(s, 0) == 0 {
				s[0] = uint64(in.ev.toHandle(newObject()))
				return
			}
			in.trap(fmt.Errorf("occt-import-js asked for the global %q, which is not provided", in.latin1(u(s, 0))))
		}
	case "__emval_get_method_caller", "__emval_call_method":
		return func(context.Context, api.Module, []uint64) {
			in.trap(fmt.Errorf("occt-import-js called a JavaScript method (%s), which is not provided", glue))
		}
	case "__emval_incref":
		return func(_ context.Context, _ api.Module, s []uint64) { in.ev.incref(u(s, 0)) }
	case "__emval_decref":
		return func(_ context.Context, _ api.Module, s []uint64) { in.ev.decref(u(s, 0)) }
	case "__emval_run_destructors":
		// The only destructors list is one emval::as made, and no type here
		// makes one; the list itself is released.
		return func(_ context.Context, _ api.Module, s []uint64) { in.ev.decref(u(s, 0)) }
	case "__emval_get_property":
		return func(_ context.Context, _ api.Module, s []uint64) {
			v, err := getProperty(in.value(u(s, 0)), in.value(u(s, 1)))
			if err != nil {
				in.trap(err)
			}
			s[0] = uint64(in.ev.toHandle(v))
		}
	case "__emval_set_property":
		return func(_ context.Context, _ api.Module, s []uint64) {
			if err := setProperty(in.value(u(s, 0)), in.value(u(s, 1)), in.value(u(s, 2))); err != nil {
				in.trap(err)
			}
		}
	case "__emval_as":
		return func(_ context.Context, _ api.Module, s []uint64) {
			v := in.value(u(s, 0))
			t := in.typeOf(u(s, 1), "emval::as")
			s[0] = math.Float64bits(in.toWire(t, v))
		}
	case "__emval_take_value":
		return func(ctx context.Context, _ api.Module, s []uint64) {
			t := in.typeOf(u(s, 0), "_emval_take_value")
			s[0] = uint64(in.ev.toHandle(in.readValue(ctx, t, u(s, 1))))
		}
	}
	return nil
}

// toWire is the type's toWireType, as the f64 _emval_as returns.
func (in *instance) toWire(t *wireType, v any) float64 {
	switch t.kind {
	case "bool":
		if truthy(v) {
			return float64(t.trueVal)
		}
		return float64(t.falseVal)
	case "int":
		if t.unsigned {
			return float64(toUint32(toNumber(v)))
		}
		return toNumber(v)
	case "float":
		return toNumber(v)
	case "emval":
		return float64(in.ev.toHandle(v))
	}
	in.trap(fmt.Errorf("emval::as to %s is not provided", t.name))
	return 0
}

// readValue is the type's readValueFromPointer.
func (in *instance) readValue(ctx context.Context, t *wireType, p uint32) any {
	switch t.kind {
	case "bool":
		return in.u8(p) != 0
	case "int":
		switch t.size {
		case 1:
			if t.signed {
				return float64(int8(in.u8(p)))
			}
			return float64(in.u8(p))
		case 2:
			if t.signed {
				return float64(int16(in.u16(p)))
			}
			return float64(in.u16(p))
		case 4:
			if t.signed {
				return float64(int32(in.u32(p)))
			}
			return float64(in.u32(p))
		}
	case "float":
		if t.size == 4 {
			return float64(math.Float32frombits(in.u32(p)))
		}
		v, _ := in.mem.ReadUint64Le(p)
		return math.Float64frombits(v)
	case "string":
		value := in.u32(p)
		n := in.u32(value)
		payload := value + 4
		var str string
		if t.utf8 {
			b, _ := in.mem.Read(payload, n)
			parts := strings.Split(string(b), "\x00")
			for i, part := range parts {
				parts[i] = validUTF8(part)
			}
			str = strings.Join(parts, "\x00")
		} else {
			b, _ := in.mem.Read(payload, n)
			r := make([]rune, len(b))
			for i, c := range b {
				r[i] = rune(c)
			}
			str = string(r)
		}
		if _, err := in.call(ctx, in.mod.sec.Exports["_free"], uint64(value)); err != nil {
			in.trap(err)
		}
		return str
	case "emval":
		h := in.u32(p)
		v := in.value(h)
		in.ev.decref(h)
		return v
	case "view":
		return &memoryView{kind: t.viewKind, size: in.u32(p), data: in.u32(p + 4)}
	}
	in.trap(fmt.Errorf("_emval_take_value of %s is not provided", t.name))
	return nil
}

func validUTF8(s string) string { return string([]rune(s)) }
