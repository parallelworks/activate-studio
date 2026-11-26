package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A 401 to a bearer key through HTTPClient tells OnRefused's listeners of
// that key; other answers, and a 401 to a request with no key, do not.
func TestOnRefusedHearsA401ToABearerKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer good-key" {
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	var mu sync.Mutex
	var heard []string
	OnRefused(func(key string) { mu.Lock(); heard = append(heard, key); mu.Unlock() })
	for _, h := range []map[string]string{{"Authorization": "Bearer good-key"}, {"Authorization": "Bearer refused-key"}, {}} {
		if _, err := Get(context.Background(), srv.URL+"/api/auth/whoami", h, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		mu.Lock()
		n := len(heard)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(heard) != 1 || heard[0] != "refused-key" {
		t.Fatalf("heard %v", heard)
	}
}
