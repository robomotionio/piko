//go:build system

package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/andydunstall/piko/client"
	"github.com/andydunstall/piko/pikotest/cluster"
	pikostatus "github.com/andydunstall/piko/server/status/client"
)

// TestUpstream_DisplacesPriorSession verifies that when SingleSessionPerEndpoint
// is enabled, registering a second listener for the same endpoint displaces the
// first one within ~1s and that subsequent traffic only reaches the second
// listener.
func TestUpstream_DisplacesPriorSession(t *testing.T) {
	node := cluster.NewNode(cluster.WithSingleSessionPerEndpoint(true))
	node.Start()
	defer node.Stop()

	upstreamURL := &url.URL{Scheme: "http", Host: node.UpstreamAddr()}

	// First listener - should be displaced.
	upstreamA := client.Upstream{URL: upstreamURL}
	lnA, err := upstreamA.Listen(context.Background(), "my-endpoint")
	assert.NoError(t, err)
	defer lnA.Close()

	hitA := 0
	srvA := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA++
		_, _ = w.Write([]byte("A"))
	}))
	srvA.Listener = lnA
	go srvA.Start()
	defer srvA.Close()

	// Sanity: A serves traffic before B connects.
	assertEndpointResponds(t, node.ProxyAddr(), "my-endpoint", "A")
	assert.Equal(t, 1, hitA)

	// Second listener - should displace A.
	upstreamB := client.Upstream{URL: upstreamURL}
	lnB, err := upstreamB.Listen(context.Background(), "my-endpoint")
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

	// Wait for A's listener to observe the GoAway/Close from the displacement.
	// The Accept loop should return ErrClosed once the underlying session closes.
	deadline := time.Now().Add(3 * time.Second)
	displaced := false
	for time.Now().Before(deadline) {
		// Endpoint list should now report exactly 1 connected upstream.
		count, err := getEndpointCount(node.AdminAddr(), "my-endpoint")
		if err == nil && count == 1 {
			displaced = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.True(t, displaced, "expected A to be displaced from the load balancer within 3s")

	// Subsequent traffic should hit only B.
	for i := 0; i < 5; i++ {
		assertEndpointResponds(t, node.ProxyAddr(), "my-endpoint", "B")
	}
	assert.Equal(t, 1, hitA, "A should not have received any traffic after displacement")
	assert.GreaterOrEqual(t, hitB, 5)
}

// TestUpstream_MultiSession_Default verifies that without SingleSessionPerEndpoint
// the load balancer keeps both sessions and round-robins between them.
func TestUpstream_MultiSession_Default(t *testing.T) {
	node := cluster.NewNode()
	node.Start()
	defer node.Stop()

	upstreamURL := &url.URL{Scheme: "http", Host: node.UpstreamAddr()}

	upstreamA := client.Upstream{URL: upstreamURL}
	lnA, err := upstreamA.Listen(context.Background(), "shared-endpoint")
	assert.NoError(t, err)
	defer lnA.Close()
	hitA := 0
	srvA := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA++
		_, _ = w.Write([]byte("A"))
	}))
	srvA.Listener = lnA
	go srvA.Start()
	defer srvA.Close()

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

	// Wait until both upstreams are registered.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		count, _ := getEndpointCount(node.AdminAddr(), "shared-endpoint")
		if count == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	for i := 0; i < 8; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://"+node.ProxyAddr(), nil)
		req.Header.Set("x-piko-endpoint", "shared-endpoint")
		resp, err := http.DefaultClient.Do(req)
		assert.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	assert.Greater(t, hitA, 0, "expected A to receive some round-robin traffic")
	assert.Greater(t, hitB, 0, "expected B to receive some round-robin traffic")
}

func assertEndpointResponds(t *testing.T, proxyAddr, endpointID, want string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+proxyAddr, nil)
	req.Header.Set("x-piko-endpoint", endpointID)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "body=%q", body)
	assert.Equal(t, want, string(body))
}

func getEndpointCount(adminAddr, endpointID string) (int, error) {
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
