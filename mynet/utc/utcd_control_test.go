package utc

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// macOS caps unix socket paths well below t.TempDir() lengths.
func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "u")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "c.sock")
}

// A control query whose server never replies must fail inside the caller's
// budget instead of hanging the operation.
func TestUTCDStatusBlockedQueryRespectsBudget(t *testing.T) {
	socket := shortSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				time.Sleep(5 * time.Second)
			}(conn)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := UTCDStatus(ctx, socket); err == nil {
		t.Fatal("blocked control query must fail")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("blocked query outlived the budget: %s", elapsed)
	}
}

// A response that arrives after the caller's deadline must not flip the
// outcome to success: the budget applies to the whole query, not just setup.
func TestUTCDStatusLateReplyIsNotSuccess(t *testing.T) {
	socket := shortSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(1500 * time.Millisecond)
		reply := make([]byte, 16)
		binary.BigEndian.PutUint32(reply[0:4], utcdControlMagic)
		binary.BigEndian.PutUint16(reply[4:6], utcdControlVersion)
		binary.BigEndian.PutUint16(reply[6:8], utcdControlReply)
		binary.BigEndian.PutUint32(reply[8:12], 1)
		_, _ = conn.Write(reply)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := UTCDStatus(ctx, socket); err == nil {
		t.Fatal("a reply after the deadline must not count as success")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("late-reply query outlived the budget: %s", elapsed)
	}
}
