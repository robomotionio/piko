package upstream

import (
	"crypto/tls"
	"errors"
	"net"
	"sync"

	"github.com/andydunstall/yamux"

	"github.com/andydunstall/piko/server/cluster"
)

var (
	// ErrGone indicates an upstream is no longer accepting connections.
	ErrGone = errors.New("gone")
)

// Upstream represents an upstream for a given endpoint.
//
// An upstream may be an upstream service connected to the local node, or
// another Piko server node.
type Upstream interface {
	EndpointID() string
	// Dial opens a connection the the upstream.
	//
	// If the upstream signals it is no longer accepting connections, returns
	// ErrGone.
	Dial() (net.Conn, error)
	// Forward indicates whether the upstream is forwarding traffic to a remote
	// node rather than a client listener.
	Forward() bool
}

// ConnUpstream represents a connection to an upstream service thats connected
// to the local node.
type ConnUpstream struct {
	endpointID string
	sess       *yamux.Session

	// onGone, if non-nil, is invoked exactly once when Dial observes the
	// remote has sent yamux GoAway. The upstream server uses this to
	// deregister the upstream from the load balancer eagerly.
	onGone     func()
	onGoneOnce sync.Once
}

func NewConnUpstream(endpointID string, sess *yamux.Session) *ConnUpstream {
	return &ConnUpstream{
		endpointID: endpointID,
		sess:       sess,
	}
}

func (u *ConnUpstream) EndpointID() string {
	return u.endpointID
}

// Session returns the underlying yamux session. Used by the upstream server
// to displace stale sessions when SingleSessionPerEndpoint is enabled.
func (u *ConnUpstream) Session() *yamux.Session {
	return u.sess
}

func (u *ConnUpstream) Dial() (net.Conn, error) {
	c, err := u.sess.OpenStream()
	if err != nil && errors.Is(err, yamux.ErrRemoteGoAway) {
		if u.onGone != nil {
			u.onGoneOnce.Do(u.onGone)
		}
		err = ErrGone
	}
	return c, err
}

func (u *ConnUpstream) Forward() bool {
	return false
}

// CloseSession sends a yamux GoAway to the remote and closes the session.
// Used by the manager to displace this upstream when a newer session for the
// same endpoint connects.
func (u *ConnUpstream) CloseSession() {
	_ = u.sess.GoAway()
	_ = u.sess.Close()
}

// NodeUpstream represents a remote Piko server node.
type NodeUpstream struct {
	endpointID string
	node       *cluster.Node
	tlsConfig  *tls.Config
}

func NewNodeUpstream(endpointID string, node *cluster.Node, tlsConfig *tls.Config) *NodeUpstream {
	return &NodeUpstream{
		endpointID: endpointID,
		node:       node,
		tlsConfig:  tlsConfig,
	}
}

func (u *NodeUpstream) EndpointID() string {
	return u.endpointID
}

func (u *NodeUpstream) Dial() (net.Conn, error) {
	if u.tlsConfig != nil {
		return tls.Dial("tcp", u.node.ProxyAddr, u.tlsConfig)
	}

	return net.Dial("tcp", u.node.ProxyAddr)
}

func (u *NodeUpstream) Forward() bool {
	return true
}
