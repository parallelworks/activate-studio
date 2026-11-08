package safeio

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadAllGuardedLimitError(t *testing.T) {
	t.Run("exact limit", func(t *testing.T) {
		got, err := ReadAllGuardedLimitError(bytes.NewBufferString("abcd"), 4)
		if err != nil || string(got) != "abcd" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("exceeded", func(t *testing.T) {
		got, err := ReadAllGuardedLimitError(bytes.NewBufferString("abcde"), 4)
		if !errors.Is(err, ErrLimitExceeded) || string(got) != "abcd" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

func TestReadAllGuardedLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  string
	}{
		{name: "whole input", limit: 32, want: "the whole input"},
		{name: "truncated", limit: 3, want: "the"},
		{name: "exact", limit: 15, want: "the whole input"},
		{name: "zero", limit: 0, want: ""},
		{name: "negative", limit: -1, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReadAllGuardedLimit(bytes.NewBufferString("the whole input"), tt.limit)
			if string(got) != tt.want {
				t.Fatalf("ReadAllGuardedLimit() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadAllGuardedLimitIncludesDataWithError(t *testing.T) {
	got := ReadAllGuardedLimit(dataAndErrorReader{}, 32)
	if string(got) != "data" {
		t.Fatalf("ReadAllGuardedLimit() = %q, want data", got)
	}
}

func TestReadAllGuardedLimitGrowthBoundaries(t *testing.T) {
	for _, size := range []int{0, 1, 511, 512, 513, (32 << 10) - 1, 32 << 10, (32 << 10) + 1, 64 << 10} {
		data := bytes.Repeat([]byte{'x'}, size)
		got := ReadAllGuardedLimit(bytes.NewReader(data), size+1)
		if !bytes.Equal(got, data) {
			t.Errorf("size %d: got %d bytes, want %d", size, len(got), len(data))
		}
	}
}

type dataAndErrorReader struct{}

func (dataAndErrorReader) Read(p []byte) (int, error) {
	return copy(p, "data"), io.ErrUnexpectedEOF
}

func BenchmarkReadAllGuarded(b *testing.B) {
	for _, size := range []int{128, 64 << 10} {
		data := bytes.Repeat([]byte{'x'}, size)
		b.Run(stringSize(size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = ReadAllGuarded(bytes.NewReader(data))
			}
		})
	}
}

func stringSize(size int) string {
	if size < 1<<10 {
		return "128B"
	}
	return "64KiB"
}

func TestAppendAllGuardedReusesCapacity(t *testing.T) {
	buf := make([]byte, 0, 64)
	out, err := AppendAllGuardedLimitError(buf[:0], strings.NewReader("hello"), 100)
	if err != nil || string(out) != "hello" || cap(out) != 64 {
		t.Fatalf("reuse: %q cap %d err %v", out, cap(out), err)
	}
	// Appends after an existing prefix; limit counts appended bytes only.
	out, err = AppendAllGuardedLimitError([]byte("ab"), strings.NewReader("cdefgh"), 3)
	if !errors.Is(err, ErrLimitExceeded) || string(out) != "abcde" {
		t.Fatalf("prefix+limit: %q err %v", out, err)
	}
	// A spare capacity larger than the limit must not read past it.
	big := make([]byte, 0, 1<<12)
	out, err = AppendAllGuardedLimitError(big, strings.NewReader(strings.Repeat("x", 50)), 10)
	if !errors.Is(err, ErrLimitExceeded) || len(out) != 10 {
		t.Fatalf("oversized buffer: len %d err %v", len(out), err)
	}
	// Exact fit reports no error.
	out, err = AppendAllGuardedLimitError(nil, strings.NewReader("12345"), 5)
	if err != nil || string(out) != "12345" {
		t.Fatalf("exact: %q err %v", out, err)
	}
}
