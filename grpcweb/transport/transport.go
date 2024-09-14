package transport

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

var ErrInvalidResponseCode = errors.New("received invalid response code")

type UnaryTransport interface {
	Header() http.Header
	Send(ctx context.Context, endpoint, contentType string, body io.Reader) (http.Header, io.ReadCloser, error)
	Close() error
}

type httpTransport struct {
	url    *url.URL
	client *http.Client

	header http.Header

	sent bool
}

func (t *httpTransport) Header() http.Header {
	return t.header
}

func (t *httpTransport) Send(
	ctx context.Context,
	endpoint, contentType string,
	body io.Reader,
) (http.Header, io.ReadCloser, error) {
	if t.sent {
		return nil, nil, errors.New("Send must be called only one time per one Request")
	}
	defer func() {
		t.sent = true
	}()

	u := *t.url
	u.Path += endpoint

	url := u.String()
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to build the API request")
	}

	req.Header = t.Header()
	req.Header.Add("content-type", contentType)
	req.Header.Add("x-grpc-web", "1")

	res, err := t.client.Do(req)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to send the API")
	}

	if res.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("%w: %d", ErrInvalidResponseCode, res.StatusCode)
	}

	return res.Header, res.Body, nil
}

func (t *httpTransport) Close() error {
	t.client.CloseIdleConnections()
	return nil
}

var NewUnary = func(host string, opts ...ConnectOption) (UnaryTransport, error) {
	o := new(connectOptions)
	for _, f := range opts {
		f(o)
	}

	scheme := "https"
	if o.insecure {
		scheme = "http"
	}

	u, err := url.Parse(fmt.Sprintf("%s://%s", scheme, host))
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse host into url")
	}

	client := http.DefaultClient
	if o.tlsConf != nil {
		if defTransport, ok := http.DefaultTransport.(*http.Transport); ok {
			defTransport.TLSClientConfig = o.tlsConf
			client.Transport = defTransport
		}
	}

	return &httpTransport{
		url:    u,
		client: client,
		header: make(http.Header),
	}, nil
}

type ClientStreamTransport interface {
	Header() (http.Header, error)
	Trailer() http.Header

	// SetRequestHeader sets headers to send gRPC-Web server.
	// It should be called before calling Send.
	SetRequestHeader(h http.Header)
	Send(ctx context.Context, body io.Reader) error
	Receive(ctx context.Context) (io.ReadCloser, error)

	// CloseSend sends a close signal to the server.
	CloseSend() error

	// Close closes the connection.
	Close() error
}

// webSocketTransport is a stream transport implementation.
//
// Currently, gRPC-Web specification does not support client streaming. (https://github.com/improbable-eng/grpc-web#client-side-streaming)
// webSocketTransport supports improbable-eng/grpc-web's own implementation.
//
// spec: https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-WEB.md
type webSocketTransport struct {
	host     string
	endpoint string

	conn *websocket.Conn

	once    sync.Once
	resOnce sync.Once

	closed bool

	writeMu sync.Mutex

	reqHeader, header, trailer http.Header
}

func (t *webSocketTransport) Header() (http.Header, error) {
	return t.header, nil
}

func (t *webSocketTransport) Trailer() http.Header {
	return t.trailer
}

func (t *webSocketTransport) SetRequestHeader(h http.Header) {
	t.reqHeader = h
}

func (t *webSocketTransport) Send(ctx context.Context, body io.Reader) error {
	if t.closed {
		return io.EOF
	}

	var err error
	t.once.Do(func() {
		h := t.reqHeader
		if h == nil {
			h = make(http.Header)
		}
		h.Set("content-type", "application/grpc-web+proto")
		h.Set("x-grpc-web", "1")
		var b bytes.Buffer
		_ = h.Write(&b)

		err = t.writeMessage(websocket.BinaryMessage, b.Bytes())
	})
	if err != nil {
		return err
	}

	var b bytes.Buffer
	b.Write([]byte{0x00})
	_, err = io.Copy(&b, body)
	if err != nil {
		return errors.Wrap(err, "failed to read request body")
	}

	return t.writeMessage(websocket.BinaryMessage, b.Bytes())
}

func (t *webSocketTransport) Receive(context.Context) (_ io.ReadCloser, err error) {
	if t.closed {
		return nil, io.EOF
	}

	defer func() {
		if err == nil {
			return
		}

		if berr, ok := errors.Cause(err).(*net.OpError); ok && !berr.Temporary() {
			err = io.EOF
		}
	}()

	// skip response header
	t.resOnce.Do(func() {
		_, _, err = t.conn.NextReader()
		if err != nil {
			err = errors.Wrap(err, "failed to read response header")
			return
		}

		_, msg, err := t.conn.NextReader()
		if err != nil {
			err = errors.Wrap(err, "failed to read response header")
			return
		}

		h := make(http.Header)
		s := bufio.NewScanner(msg)
		for s.Scan() {
			t := s.Text()
			i := strings.Index(t, ": ")
			if i == -1 {
				continue
			}
			k := strings.ToLower(t[:i])
			h.Add(k, t[i+2:])
		}
		t.header = h
	})

	var buf bytes.Buffer
	var b []byte

	_, b, err = t.conn.ReadMessage()
	if err != nil {
		if cerr, ok := err.(*websocket.CloseError); ok {
			if cerr.Code == websocket.CloseNormalClosure {
				return nil, io.EOF
			}
			if cerr.Code == websocket.CloseAbnormalClosure {
				return nil, io.ErrUnexpectedEOF
			}
		}
		err = errors.Wrap(err, "failed to read response body")
		return
	}
	buf.Write(b)

	var r io.Reader
	_, r, err = t.conn.NextReader()
	if err != nil {
		return
	}

	res := io.NopCloser(io.MultiReader(&buf, r))

	by, err := io.ReadAll(res)
	if err != nil {
		panic(err)
	}

	res = io.NopCloser(bytes.NewReader(by))

	return res, nil
}

func (t *webSocketTransport) CloseSend() error {
	// 0x01 means the finish send frame.
	// ref. transports/websocket/websocket.ts
	if err := t.writeMessage(websocket.BinaryMessage, []byte{0x01}); err != nil {
		return fmt.Errorf("failed to write message to a websocket: %w", err)
	}

	return nil
}

func (t *webSocketTransport) Close() error {
	// Send the close message.
	err := t.writeMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil {
		return err
	}
	t.closed = true
	// Close the WebSocket connection.
	return t.conn.Close()
}

func (t *webSocketTransport) writeMessage(msg int, b []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.conn.WriteMessage(msg, b)
}

var NewClientStream = func(host, endpoint string, opts ...ConnectOption) (ClientStreamTransport, error) {
	o := new(connectOptions)
	for _, f := range opts {
		f(o)
	}

	scheme := "wss"
	if o.insecure {
		scheme = "ws"
	}

	u, err := url.Parse(fmt.Sprintf("%s://%s%s", scheme, host, endpoint))
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse url")
	}

	wsDialer := &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 45 * time.Second,
	}

	if o.tlsConf != nil {
		wsDialer.TLSClientConfig = o.tlsConf
	}

	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "grpc-websockets")
	var conn *websocket.Conn
	conn, _, err = wsDialer.Dial(u.String(), h)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to dial to '%s'", u.String())
	}

	return &webSocketTransport{
		host:     host,
		endpoint: endpoint,
		conn:     conn,
	}, nil
}
