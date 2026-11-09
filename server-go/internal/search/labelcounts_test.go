package search

import (
	"reflect"
	"testing"
)

// Labels used equally often come by name, whatever order the rows came in.
func TestLabelCountsTiesByName(t *testing.T) {
	c := &labelCounts{m: map[string]float64{}}
	for _, l := range []string{"wind-tunnel", "cfd", "schlieren", "cad", "wind-tunnel", "cfd", "chat-session", "imagery"} {
		c.add(l, 1)
	}
	got := []string{}
	for _, lc := range c.top(4) {
		got = append(got, lc.Label)
	}
	want := []string{"cfd", "wind-tunnel", "cad", "chat-session"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("top(4) = %v, want %v", got, want)
	}
}
