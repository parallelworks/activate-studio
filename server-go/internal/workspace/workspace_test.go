package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWithStartsTheWorkspaceAndRetries(t *testing.T) {
	var cli [][]string
	tries := 0
	r := Runner{
		CLI: func(_ context.Context, a []string, _ time.Duration) (string, error) {
			cli = append(cli, a)
			return "", nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	v, res, err := With(context.Background(), r, func(context.Context) (string, error) {
		tries++
		if tries < 3 {
			return "", errors.New("User workspace not found or is not running")
		}
		return "run-1", nil
	})
	if err != nil || v != "run-1" || !res.Started || len(cli) != 1 || strings.Join(cli[0], " ") != "workspace start" {
		t.Fatal(v, res, err, cli)
	}
	_, _, err = With(context.Background(), r, func(context.Context) (int, error) { return 0, errors.New("bad input") })
	if err == nil || err.Error() != "bad input" {
		t.Fatal(err)
	}
	r.Budget = time.Nanosecond
	_, _, err = With(context.Background(), r, func(context.Context) (int, error) { return 0, errors.New("Could not execute workflow") })
	if err == nil || !strings.HasPrefix(err.Error(), "The platform user workspace is still not running after a start request") {
		t.Fatal(err)
	}
}
