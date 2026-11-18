package cmd

import (
	"net"
	"testing"
)

func TestPickLoopbackAddr(t *testing.T) {
	addr, err := pickLoopbackAddr()
	if err != nil {
		t.Fatalf("pickLoopbackAddr() error = %v", err)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", addr, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("host = %q, want %q", host, "127.0.0.1")
	}
	if port == "" || port == "0" {
		t.Fatalf("port = %q, want a kernel-assigned port", port)
	}
}
