package grpcweb

import (
	"crypto/tls"

	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/metadata"
)

var (
	defaultDialOptions = dialOptions{}
	defaultCallOptions = callOptions{
		codec: encoding.GetCodecV2(proto.Name),
	}
)

type dialOptions struct {
	defaultCallOptions []CallOption
	insecure           bool
	tlsConf            *tls.Config
}

type DialOption func(*dialOptions)

func WithDefaultCallOptions(opts ...CallOption) DialOption {
	return func(opt *dialOptions) {
		opt.defaultCallOptions = opts
	}
}

func WithInsecure() DialOption {
	return func(opt *dialOptions) {
		opt.insecure = true
	}
}

func WithTLSConfig(conf *tls.Config) DialOption {
	return func(opt *dialOptions) {
		opt.tlsConf = conf
	}
}

type callOptions struct {
	codec           encoding.CodecV2
	header, trailer *metadata.MD
}

type CallOption func(*callOptions)

func CallContentSubtype(contentSubtype string) CallOption {
	return func(opt *callOptions) {
		opt.codec = encoding.GetCodecV2(contentSubtype)
	}
}

func Header(h *metadata.MD) CallOption {
	return func(opt *callOptions) {
		*h = metadata.New(nil)
		opt.header = h
	}
}

func Trailer(t *metadata.MD) CallOption {
	return func(opt *callOptions) {
		*t = metadata.New(nil)
		opt.trailer = t
	}
}
