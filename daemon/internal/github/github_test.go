package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseRemote(t *testing.T) {
	ok := map[string]string{
		"https://github.com/DevertNet/bergfried.git": "DevertNet/bergfried",
		"https://github.com/DevertNet/bergfried":     "DevertNet/bergfried",
		"git@github.com:DevertNet/bergfried.git":     "DevertNet/bergfried",
		"ssh://git@github.com/o/r.git":               "o/r",
		"https://github.com/o/r/":                    "o/r",
	}
	for in, want := range ok {
		r, err := ParseRemote(in)
		if err != nil || r.String() != want {
			t.Fatalf("%s: %v %v", in, r, err)
		}
	}
	for _, bad := range []string{"", "https://evil.com/o/r.git", "https://user:tok@github.com/o/r.git", "https://github.com/o", "https://github.com/o/r/x",
		"https://github.com/../r", "https://github.com/o/..", "git@github.com:o/r;rm", "file:///etc/passwd", "https://github.com.evil.com/o/r", "https://github.com/o/r name"} {
		if _, err := ParseRemote(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func fake(t *testing.T) (*Client, *http.ServeMux, *[]string) {
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	var seen []string
	wrap := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" auth="+r.Header.Get("Authorization"))
		mux.ServeHTTP(w, r)
	})
	ts.Config.Handler = wrap
	c := New(StaticToken("tok123"))
	c.Base = ts.URL
	return c, mux, &seen
}

func TestErrorsAndMissingToken(t *testing.T) {
	c, mux, _ := fake(t)
	mux.HandleFunc("GET /repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	_, err := c.DefaultBranch(context.Background(), Repo{"o", "r"})
	var ae *APIError
	if !asAPI(err, &ae) || ae.Status != 404 {
		t.Fatalf("%v", err)
	}
	c.Tokens = StaticToken("")
	if _, err := c.DefaultBranch(context.Background(), Repo{"o", "r"}); err != ErrNoToken {
		t.Fatalf("%v", err)
	}
}

func asAPI(err error, out **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*out = e
	}
	return ok
}
