package api

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestAcceptMuxConnections_IdleConnectionDoesNotBlockHTTP(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	defer listener.Close()

	httpListener := newMuxListener(listener.Addr(), 4)
	defer httpListener.Close()

	server := &Server{}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.acceptMuxConnections(listener, httpListener)
	}()
	defer func() {
		_ = listener.Close()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("accept loop error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for accept loop")
		}
	}()

	idleConn, errIdle := net.Dial("tcp", listener.Addr().String())
	if errIdle != nil {
		t.Fatalf("dial idle: %v", errIdle)
	}
	defer idleConn.Close()

	time.Sleep(100 * time.Millisecond)

	httpConn, errHTTP := net.Dial("tcp", listener.Addr().String())
	if errHTTP != nil {
		t.Fatalf("dial http: %v", errHTTP)
	}
	defer httpConn.Close()
	if _, errWrite := io.WriteString(httpConn, "GET / HTTP/1.1\r\nHost: local\r\n\r\n"); errWrite != nil {
		t.Fatalf("write http: %v", errWrite)
	}

	acceptCh := make(chan net.Conn, 1)
	go func() {
		conn, _ := httpListener.Accept()
		acceptCh <- conn
	}()

	select {
	case conn := <-acceptCh:
		if conn == nil {
			t.Fatal("expected routed HTTP connection")
		}
		_ = conn.Close()
	case <-time.After(time.Second):
		t.Fatal("idle connection blocked HTTP routing")
	}
}
