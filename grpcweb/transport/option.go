package transport

import "crypto/tls"

type connectOptions struct {
	insecure bool
	tlsConf  *tls.Config
}

type ConnectOption func(*connectOptions)

func WithInsecure() ConnectOption {
	return func(opt *connectOptions) {
		opt.insecure = true
	}
}

func WithTLSConfig(conf *tls.Config) ConnectOption {
	return func(opt *connectOptions) {
		opt.tlsConf = conf
	}
}
