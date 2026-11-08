package ort

import (
	"os"
	"testing"
)

func TestSmoke(t *testing.T) {
	lib, model := os.Getenv("ORT_LIB"), os.Getenv("ORT_MODEL")
	if lib == "" {
		t.Skip()
	}
	r, err := Load(lib)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.NewSession(model, SessionOptions{IntraOpThreads: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(r.Version(), s.Inputs(), s.Outputs())
	ids := []int64{101, 7592, 2088, 102}
	out, shape, err := s.RunFloat([]Int64Input{
		{"input_ids", []int64{1, 4}, ids},
		{"attention_mask", []int64{1, 4}, []int64{1, 1, 1, 1}},
		{"token_type_ids", []int64{1, 4}, []int64{0, 0, 0, 0}},
	}, s.Outputs()[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Log(shape, out[:4])
}
