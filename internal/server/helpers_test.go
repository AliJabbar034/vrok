package server_test

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

// newJar returns a cookie jar so the unlock cookie survives between requests,
// which is how a real browser behaves after submitting the password form.
func newJar() http.CookieJar {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	return jar
}

// post submits a form to a path inside the share.
func (f *fixture) post(t *testing.T, rel string, values url.Values) *http.Response {
	t.Helper()
	resp, err := f.client.Post(f.url(rel), "application/x-www-form-urlencoded", strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatalf("POST %s: %v", rel, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
