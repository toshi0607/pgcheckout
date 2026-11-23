//go:build integration

package testpg

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

type QueryFault uint8

const (
	ForwardQuery QueryFault = iota
	InjectQueryError
	DropQueryReply
)

type FaultProxy struct {
	t        *testing.T
	hook     func(string) QueryFault
	hookMu   sync.Mutex
	injected [DropQueryReply + 1]atomic.Int64
}

func StartFaultProxy(t *testing.T, adminURL *url.URL, hook func(string) QueryFault) (*url.URL, *FaultProxy) {
	t.Helper()
	cfg, err := pgconn.ParseConfig(adminURL.String())
	if err != nil {
		t.Fatal("fault proxy: could not parse the PostgreSQL test admin URL")
	}
	network, upstream := pgconn.NetworkAddress(cfg.Host, cfg.Port)
	if network != "tcp" {
		t.Fatal("fault proxy requires a TCP PostgreSQL upstream")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for fault proxy: %v", err)
	}
	proxyURL := *adminURL
	proxyURL.Host = listener.Addr().String()
	params := proxyURL.Query()
	if cfg.Password != "" {
		// Preserve credentials resolved from .pgpass for the original address.
		proxyURL.User = url.UserPassword(cfg.User, cfg.Password)
		params.Del("password")
	}
	params.Del("host")
	params.Del("port")
	params.Set("sslmode", "disable")
	params.Set("sslrootcert", "")
	proxyURL.RawQuery = params.Encode()

	p := &FaultProxy{t: t, hook: hook}
	var workers sync.WaitGroup
	t.Cleanup(func() {
		p.report("close listener", listener.Close())
		workers.Wait()
	})
	ctx := t.Context()
	workers.Go(func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				p.report("accept client", err)
				return
			}
			workers.Go(func() { p.serve(ctx, client, upstream) })
		}
	})
	return &proxyURL, p
}

func (p *FaultProxy) Injected(fault QueryFault) int64 {
	return p.injected[fault].Load()
}

type proxySession struct {
	client, server net.Conn
	clientWrite    sync.Mutex
	dropReply      atomic.Bool
}

func (p *FaultProxy) serve(ctx context.Context, client net.Conn, upstream string) {
	closeClient := p.closeOnCancel(ctx, client)
	defer closeClient()
	input := bufio.NewReader(client)
	startup, err := readProxyPacket(input, false)
	if err != nil {
		p.report("read startup packet", err)
		return
	}
	if len(startup) < 8 {
		p.t.Error("fault proxy: startup packet has no protocol code")
		return
	}
	const (
		cancelRequest = 80877102
		sslRequest    = 80877103
		gssencRequest = 80877104
	)
	code := binary.BigEndian.Uint32(startup[4:8])
	if code == sslRequest || code == gssencRequest {
		p.t.Error("fault proxy: received SSLRequest or GSSENCRequest, expected plain TCP")
		return
	}
	server, err := (&net.Dialer{}).DialContext(ctx, "tcp", upstream)
	if err != nil {
		if ctx.Err() == nil {
			p.report("dial upstream", err)
		}
		return
	}
	closeServer := p.closeOnCancel(ctx, server)
	defer closeServer()
	if _, err := server.Write(startup); err != nil {
		p.report("forward startup packet", err)
		return
	}
	if code == cancelRequest {
		_, err := io.Copy(client, server)
		p.report("relay cancel acknowledgement", err)
		return
	}

	session := &proxySession{client: client, server: server}
	var responses sync.WaitGroup
	responses.Go(func() {
		defer closeClient()
		p.report("relay server messages", p.relayResponses(session))
	})
	p.report("relay client messages", p.relayQueries(input, session))
	closeClient()
	if session.dropReply.Load() {
		// Leave the original backend connected while recovery uses a new session.
		<-ctx.Done()
	}
	closeServer()
	responses.Wait()
}

func (p *FaultProxy) relayQueries(input *bufio.Reader, session *proxySession) error {
	for {
		packet, err := readProxyPacket(input, true)
		if err != nil {
			return err
		}
		if packet[0] == 'Q' {
			sql, ok := strings.CutSuffix(string(packet[5:]), "\x00")
			if !ok {
				return errors.New("query is missing its NUL terminator")
			}
			switch p.queryFault(sql) {
			case InjectQueryError:
				if err := session.injectError(); err != nil {
					return fmt.Errorf("inject query error: %w", err)
				}
				p.injected[InjectQueryError].Add(1)
				continue
			case DropQueryReply:
				session.dropReply.Store(true)
			}
		}
		if _, err := session.server.Write(packet); err != nil {
			return err
		}
	}
}

func (p *FaultProxy) relayResponses(session *proxySession) error {
	input := bufio.NewReader(session.server)
	for {
		packet, err := readProxyPacket(input, true)
		if err != nil {
			return err
		}
		if session.dropReply.Load() && (packet[0] == 'C' || packet[0] == 'E') {
			p.injected[DropQueryReply].Add(1)
			return nil
		}
		if err := session.writeClient(packet); err != nil {
			return err
		}
	}
}

func (p *FaultProxy) queryFault(sql string) QueryFault {
	p.hookMu.Lock()
	defer p.hookMu.Unlock()
	return p.hook(sql)
}

func (s *proxySession) injectError() error {
	response := pgproto3.ErrorResponse{
		Severity: "ERROR", SeverityUnlocalized: "ERROR", Code: "58030", Message: "injected failure",
	}
	packet, err := response.Encode(nil)
	if err != nil {
		return err
	}
	packet, err = (&pgproto3.ReadyForQuery{TxStatus: 'I'}).Encode(packet)
	if err != nil {
		return err
	}
	return s.writeClient(packet)
}

func (s *proxySession) writeClient(packet []byte) error {
	s.clientWrite.Lock()
	defer s.clientWrite.Unlock()
	_, err := s.client.Write(packet)
	return err
}

func readProxyPacket(input *bufio.Reader, typed bool) ([]byte, error) {
	var header [5]byte
	lengthOffset := 0
	if typed {
		lengthOffset = 1
	}
	headerBytes := header[:lengthOffset+4]
	if _, err := io.ReadFull(input, headerBytes); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(headerBytes[lengthOffset:])
	if length < 4 || length > 0x3fffffff {
		return nil, fmt.Errorf("invalid PostgreSQL packet length %d", length)
	}
	packet := make([]byte, lengthOffset+int(length))
	copy(packet, headerBytes)
	_, err := io.ReadFull(input, packet[len(headerBytes):])
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return packet, err
}

func (p *FaultProxy) closeOnCancel(ctx context.Context, conn net.Conn) func() {
	closed := make(chan struct{})
	closeConn := func() {
		defer close(closed)
		p.report("close connection", conn.Close())
	}
	stop := context.AfterFunc(ctx, closeConn)
	return sync.OnceFunc(func() {
		if stop() {
			closeConn()
		}
		<-closed
	})
}

func (p *FaultProxy) report(operation string, err error) {
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		p.t.Errorf("fault proxy %s: %v", operation, err)
	}
}
