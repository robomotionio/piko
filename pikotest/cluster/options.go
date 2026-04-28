package cluster

import (
	"github.com/andydunstall/piko/pkg/auth"
	"github.com/andydunstall/piko/pkg/log"
)

type options struct {
	join                     []string
	authConfig               auth.Config
	tls                      bool
	logger                   log.Logger
	singleSessionPerEndpoint bool
	yamuxKeepAliveSeconds    int
}

type joinOption struct {
	Join []string
}

func (o joinOption) apply(opts *options) {
	opts.join = o.Join
}

// WithJoin configures the nodes to join.
func WithJoin(join []string) Option {
	return joinOption{Join: join}
}

type authConfigOption struct {
	AuthConfig auth.Config
}

func (o authConfigOption) apply(opts *options) {
	opts.authConfig = o.AuthConfig
}

// WithAuthConfig configures the upstream authentication config.
func WithAuthConfig(config auth.Config) Option {
	return authConfigOption{AuthConfig: config}
}

type tlsOption bool

func (o tlsOption) apply(opts *options) {
	opts.tls = bool(o)
}

// WithTLS configures the node ports to use TLS.
func WithTLS(tls bool) Option {
	return tlsOption(tls)
}

type loggerOption struct {
	Logger log.Logger
}

func (o loggerOption) apply(opts *options) {
	opts.logger = o.Logger
}

// WithLogger configures the logger. Defaults to no output.
func WithLogger(logger log.Logger) Option {
	return loggerOption{Logger: logger}
}

type singleSessionOption bool

func (o singleSessionOption) apply(opts *options) {
	opts.singleSessionPerEndpoint = bool(o)
}

// WithSingleSessionPerEndpoint configures the upstream manager to displace
// existing sessions for an endpoint when a new one connects.
func WithSingleSessionPerEndpoint(v bool) Option {
	return singleSessionOption(v)
}

type yamuxKeepAliveOption int

func (o yamuxKeepAliveOption) apply(opts *options) {
	opts.yamuxKeepAliveSeconds = int(o)
}

// WithYamuxKeepAliveSeconds overrides the upstream yamux KeepAliveInterval
// (seconds). 0 means use yamux default.
func WithYamuxKeepAliveSeconds(v int) Option {
	return yamuxKeepAliveOption(v)
}

type Option interface {
	apply(*options)
}
