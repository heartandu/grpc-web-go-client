package grpcweb

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"strconv"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/heartandu/grpc-web-go-client/grpcweb/parser"
	"github.com/heartandu/grpc-web-go-client/grpcweb/transport"
)

var (
	ErrInsecureWithTLS      = errors.New("insecure and tls configuration couldn't be set simultaniously")
	ErrNotAStreamingRequest = errors.New("not a streaming request")
)

type ClientConn struct {
	host        string
	dialOptions *dialOptions
}

func NewClient(host string, opts ...DialOption) (*ClientConn, error) {
	opt := defaultDialOptions
	for _, o := range opts {
		o(&opt)
	}

	if opt.insecure && opt.tlsConf != nil {
		return nil, ErrInsecureWithTLS
	}

	return &ClientConn{
		host:        host,
		dialOptions: &opt,
	}, nil
}

func (c *ClientConn) Invoke(ctx context.Context, method string, args, reply interface{}, opts ...CallOption) error {
	callOptions := c.applyCallOptions(opts)
	codec := callOptions.codec

	tr, err := transport.NewUnary(c.host, c.connectOptions()...)
	if err != nil {
		return errors.Wrap(err, "failed to create a new unary transport")
	}
	defer tr.Close()

	r, err := encodeRequestBody(codec, args)
	if err != nil {
		return errors.Wrap(err, "failed to build the request body")
	}

	md, ok := metadata.FromOutgoingContext(ctx)
	if ok {
		for k, v := range md {
			for _, vv := range v {
				tr.Header().Add(k, vv)
			}
		}
	}

	contentType := "application/grpc-web+" + codec.Name()
	header, rawBody, err := tr.Send(ctx, method, contentType, r)
	if err != nil {
		if errors.Is(err, transport.ErrInvalidResponseCode) {
			return status.New(codes.Unavailable, err.Error()).Err()
		}

		return errors.Wrap(err, "failed to send the request")
	}
	defer rawBody.Close()

	md = toMetadata(header)
	if err := checkStatus(md).Err(); err != nil {
		return err
	}

	if callOptions.header != nil {
		*callOptions.header = md
	}

	resHeader, err := parser.ParseResponseHeader(rawBody)
	if err != nil {
		return errors.Wrap(err, "failed to parse response header")
	}

	if resHeader.IsMessageHeader() {
		resBody, err := parser.ParseLengthPrefixedMessage(rawBody, resHeader.ContentLength)
		if err != nil {
			return errors.Wrap(err, "failed to parse the response body")
		}
		if err := codec.Unmarshal([]mem.Buffer{mem.NewBuffer(&resBody, nil)}, reply); err != nil {
			return errors.Wrapf(err, "failed to unmarshal response body by codec %s", codec.Name())
		}

		resHeader, err = parser.ParseResponseHeader(rawBody)
		if err != nil {
			return errors.Wrap(err, "failed to parse response header")
		}
	}
	if !resHeader.IsTrailerHeader() {
		return errors.New("unexpected header")
	}

	status, trailer, err := parser.ParseStatusAndTrailer(rawBody, resHeader.ContentLength)
	if err != nil {
		return errors.Wrap(err, "failed to parse status and trailer")
	}
	if callOptions.trailer != nil {
		*callOptions.trailer = trailer
	}

	return status.Err()
}

func (c *ClientConn) NewStream(
	ctx context.Context,
	desc *grpc.StreamDesc,
	method string,
	opts ...CallOption,
) (Stream, error) {
	switch {
	case desc.ClientStreams && desc.ServerStreams:
		return c.newBidiStream(ctx, method, opts...)
	case desc.ClientStreams:
		return c.newClientStream(ctx, method, opts...)
	case desc.ServerStreams:
		return c.newServerStream(ctx, method, opts...)
	default:
		return nil, ErrNotAStreamingRequest
	}
}

func (c *ClientConn) newClientStream(ctx context.Context, method string, opts ...CallOption) (Stream, error) {
	tr, err := transport.NewClientStream(c.host, method, c.connectOptions()...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create a new transport stream")
	}

	return &clientStream{
		ctx:         ctx,
		endpoint:    method,
		transport:   tr,
		callOptions: c.applyCallOptions(opts),
	}, nil
}

func (c *ClientConn) newServerStream(ctx context.Context, method string, opts ...CallOption) (Stream, error) {
	tr, err := transport.NewUnary(c.host, c.connectOptions()...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create a new unary transport")
	}

	return &serverStream{
		ctx:         ctx,
		endpoint:    method,
		transport:   tr,
		callOptions: c.applyCallOptions(opts),
	}, nil
}

func (c *ClientConn) newBidiStream(ctx context.Context, method string, opts ...CallOption) (Stream, error) {
	stream, err := c.newClientStream(ctx, method, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create a new client stream")
	}

	return &bidiStream{
		clientStream: stream.(*clientStream),
	}, nil
}

func (c *ClientConn) applyCallOptions(opts []CallOption) *callOptions {
	callOpts := append(c.dialOptions.defaultCallOptions, opts...)
	callOptions := defaultCallOptions
	for _, o := range callOpts {
		o(&callOptions)
	}
	return &callOptions
}

func (c *ClientConn) connectOptions() []transport.ConnectOption {
	connOpts := make([]transport.ConnectOption, 0)
	if c.dialOptions.insecure {
		connOpts = append(connOpts, transport.WithInsecure())
	}

	if c.dialOptions.tlsConf != nil {
		connOpts = append(connOpts, transport.WithTLSConfig(c.dialOptions.tlsConf))
	}

	return connOpts
}

func checkStatus(md metadata.MD) *status.Status {
	var (
		code codes.Code
		msg  string
	)

	gs := md.Get("grpc-status")
	gm := md.Get("grpc-message")

	if len(gs) > 0 {
		c, err := strconv.ParseUint(gs[0], 10, 32)
		if err != nil {
			return status.New(codes.Unknown, "unknown status code "+gs[0])
		}

		code = codes.Code(c)
	}

	if len(gm) > 0 {
		msg = gm[0]
	}

	return status.New(code, msg)
}

// copied from rpc_util.go#msgHeader
const headerLen = 5

func header(bodyLen int) []byte {
	h := make([]byte, 5)
	h[0] = byte(0)
	binary.BigEndian.PutUint32(h[1:], uint32(bodyLen))
	return h
}

// header (compressed-flag(1) + message-length(4)) + body
// TODO: compressed message
func encodeRequestBody(codec encoding.CodecV2, in interface{}) (io.Reader, error) {
	body, err := codec.Marshal(in)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal the request body")
	}
	buf := bytes.NewBuffer(make([]byte, 0, headerLen+len(body)))
	_, _ = buf.Write(header(body.Len()))
	_, _ = buf.ReadFrom(body.Reader())
	return buf, nil
}

func toMetadata(h http.Header) metadata.MD {
	if len(h) == 0 {
		return nil
	}
	md := metadata.New(nil)
	for k, v := range h {
		md.Append(k, v...)
	}
	return md
}
