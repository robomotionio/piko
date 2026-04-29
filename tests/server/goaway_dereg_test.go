//go:build system

package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/andydunstall/piko/client"
	"github.com/andydunstall/piko/pikotest/cluster"
)

// TestUpstream_DeregistersOnGoAway verifies that when an upstream sends a yamux
// GoAway (e.g. via Listener.Close on the client) but keeps the underlying
// session open, subsequent traffic is routed away from it within ~1s. The
// reactive deregistration is triggered the first time the proxy attempts to
// dial through the GoAway'd session.
func TestUpstream_DeregistersOnGoAway(t *testing.T) {
	node := cluster.NewNode()
	node.Start()
	defer node.Stop()

	upstreamURL := &url.URL{Scheme: "http", Host: node.UpstreamAddr()}

	// Listener A — will GoAway shortly.
	upstreamA := client.Upstream{URL: upstreamURL}
	lnA, err := upstreamA.Listen(context.Background(), "shared-endpoint")
	assert.NoError(t, err)
	hitA := 0
	srvA := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA++
		_, _ = w.Write([]byte("A"))
	}))
	srvA.Listener = lnA
	go srvA.Start()
	defer srvA.Close()

	// Listener B — stays healthy.
	upstreamB := client.Upstream{URL: upstreamURL}
	lnB, err := upstreamB.Listen(context.Background(), "shared-endpoint")
	assert.NoError(t, err)
	defer lnB.Close()
	hitB := 0
	srvB := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitB++
		_, _ = w.Write([]byte("B"))
	}))
	srvB.Listener = lnB
	go srvB.Start()
	defer srvB.Close()

	// Wait for both upstreams to be registered.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		count, _ := getEndpointCount(node.AdminAddr(), "shared-endpoint")
		if count == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Trigger A's GoAway without closing the session. Listener.Close calls
	// sess.GoAway() but does not close the underlying connection — that
	// happens only on Shutdown(). This simulates "client wants to stop
	// accepting but yamux session is still alive" — the case Issue #290
	// describes.
	assert.NoError(t, lnA.Close())

	// At this point traffic may still hit A once before reactive dereg fires.
	// Send up to 10 requests; eventually the load balancer should drop A.
	gotA, gotB := 0, 0
	for i := 0; i < 20; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://"+node.ProxyAddr(), nil)
		req.Header.Set("x-piko-endpoint", "shared-endpoint")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		switch string(body) {
		case "A":
			gotA++
		case "B":
			gotB++
		}
		time.Sleep(20 * time.Millisecond)
	}

	assert.Greater(t, gotB, 0, "expected B to receive traffic after A goes away")
	// A may receive at most 2 requests (one to trigger the dereg, plus a
	// possible race window). It should not be a majority.
	assert.LessOrEqual(t, gotA, 2, "expected A to be removed from the load balancer quickly; got A=%d B=%d", gotA, gotB)

	// Final state: only B is registered.
	finalCount, err := getEndpointCount(node.AdminAddr(), "shared-endpoint")
	assert.NoError(t, err)
	assert.Equal(t, 1, finalCount)
}
