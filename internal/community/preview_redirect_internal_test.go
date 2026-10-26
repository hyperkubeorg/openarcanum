package community

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A permitted site that redirects to a private address doesn't get the
// fetcher there: every hop is checked again before connecting.
func TestFetcherRefusesRedirectToPrivateAddress(t *testing.T) {
	var target atomic.Value
	var hits atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, target.Load().(string), http.StatusFound)
	}))
	defer site.Close()
	// Loopback stands in for a public site here; nothing else is allowed.
	f := testFetcher(t, true)
	for _, to := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/admin",
		"http://192.168.1.1:8080/",
		"http://[fd00::1]/",
		"http://[::ffff:10.0.0.1]/",
		"http://0.0.0.0:80/",
	} {
		target.Store(to)
		before := hits.Load()
		if _, err := f.fetch(context.Background(), site.URL+"/page"); !errors.Is(err, errPreviewBlocked) {
			t.Errorf("redirect to %s: %v", to, err)
		}
		if hits.Load() != before+1 {
			t.Errorf("redirect to %s: the site was asked %d times", to, hits.Load()-before)
		}
	}
	// A redirect to a scheme that isn't allowed is refused before any dial.
	target.Store("file:///etc/passwd")
	if _, err := f.fetch(context.Background(), site.URL+"/page"); !errors.Is(err, errPreviewScheme) {
		t.Errorf("redirect to file: %v", err)
	}
}
