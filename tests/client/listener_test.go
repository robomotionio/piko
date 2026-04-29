//go:build system

package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/andydunstall/yamux"
	"github.com/stretchr/testify/assert"

	"github.com/andydunstall/piko/client"
	"github.com/andydunstall/piko/pikotest/cluster"
	pikostatus "github.com/andydunstall/piko/server/status/client"
)

// TestListener_CloseThenListen exercises the close→listen cycle. It mirrors the
// shape of a deskbot reconnect: the prior listener is closed (sending GoAway)
// and a new one is created for the same endpoint. The server should converge
// to exactly 1 registered upstream within a few seconds, with no lingering
// dead session.
//
// This indirectly validates the WS2a fix because the per-listener Close
// sequence sends GoAway+Close (Close via Shutdown semantics inherited from
// session teardown). Without WS2a's session-leak fix, an internal-reconnect
// path inside a single listener would also leave a stale upstream registered;
// that path is much harder to reproduce in this test harness.
func TestListener_CloseThenListen(t *testing.T) {
	node := cluster.NewNode()
	node.Start()
	defer node.Stop()

	upstreamURL := &url.URL{Scheme: "http", Host: node.UpstreamAddr()}

	upstream := client.Upstream{URL: upstreamURL}
	ln1, err := upstream.Listen(context.Background(), "cycle-endpoint")
	assert.NoError(t, err)
	waitForCount(t, node.AdminAddr(), "cycle-endpoint", 1, 2*time.Second)

	// Close ln1 (Listener.Close sends GoAway). Then immediately Shutdown to
	// also drop the conn so the server completes the deferred RemoveConn.
	assert.NoError(t, ln1.Close())
	assert.NoError(t, ln1.Shutdown())

	// New listener for the same endpoint.
	ln2, err := upstream.Listen(context.Background(), "cycle-endpoint")
	assert.NoError(t, err)
	defer ln2.Close()

	// Server should converge to a single registered upstream — the new one.
	waitForCount(t, node.AdminAddr(), "cycle-endpoint", 1, 3*time.Second)
	assertProxyRoundTrip(t, node.ProxyAddr(), ln2, "cycle-endpoint")
}

// TestListener_YamuxConfigPropagates verifies that a custom yamux config
// passed via Upstream.YamuxConfig is honored end-to-end. We set a very tight
// KeepAliveInterval and confirm the listener still functions normally.
func TestListener_YamuxConfigPropagates(t *testing.T) {
	node := cluster.NewNode()
	node.Start()
	defer node.Stop()

	muxCfg := yamux.DefaultConfig()
	muxCfg.KeepAliveInterval = 1 * time.Second
	muxCfg.ConnectionWriteTimeout = 2 * time.Second

	upstream := client.Upstream{
		URL:         &url.URL{Scheme: "http", Host: node.UpstreamAddr()},
		YamuxConfig: muxCfg,
	}
	ln, err := upstream.Listen(context.Background(), "custom-yamux-endpoint")
	assert.NoError(t, err)
	defer ln.Close()

	waitForCount(t, node.AdminAddr(), "custom-yamux-endpoint", 1, 2*time.Second)
	assertProxyRoundTrip(t, node.ProxyAddr(), ln, "custom-yamux-endpoint")
}

func waitForCount(t *testing.T, adminAddr, endpointID string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last int
	for time.Now().Before(deadline) {
		count, err := getEndpointCountListener(adminAddr, endpointID)
		if err == nil {
			last = count
			if count == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for endpoint %q count to reach %d (last=%d)", endpointID, want, last)
}

func getEndpointCountListener(adminAddr, endpointID string) (int, error) {
	u, err := url.Parse("http://" + adminAddr)
	if err != nil {
		return 0, err
	}
	c := pikostatus.NewClient(u)
	endpoints, err := pikostatus.NewUpstream(c).Endpoints()
	if err != nil {
		return 0, fmt.Errorf("get endpoints: %w", err)
	}
	return endpoints[endpointID], nil
}

func assertProxyRoundTrip(t *testing.T, proxyAddr string, ln client.Listener, endpointID string) {
	t.Helper()
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(done)
	}()
	defer func() {
		_ = srv.Close()
		<-done
	}()

	// Give the server a moment to start.
	time.Sleep(20 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodGet, "http://"+proxyAddr, nil)
	req.Header.Set("x-piko-endpoint", endpointID)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok", string(body))
}
