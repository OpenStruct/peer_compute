package agent

import (
	"context"
	"io"
	"log/slog"
	"net"
)

// runSSHProxy listens on providerWGIP:22 and forwards each connection to
// localhost:sshPort. This lets renters reach the container's SSH daemon via
// the WireGuard tunnel without any OS-level port forwarding rules.
// It runs until ctx is cancelled.
func runSSHProxy(ctx context.Context, providerWGIP, sshPort string, log *slog.Logger) {
	listenAddr := net.JoinHostPort(providerWGIP, "22")
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Warn("ssh proxy: listen failed", "addr", listenAddr, "error", err)
		return
	}
	log.Info("ssh proxy listening", "addr", listenAddr, "→", "localhost:"+sshPort)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return // context cancelled
			}
			log.Warn("ssh proxy: accept error", "error", err)
			return
		}
		go proxyConn(ctx, conn, sshPort, log)
	}
}

func proxyConn(ctx context.Context, src net.Conn, sshPort string, log *slog.Logger) {
	defer src.Close()

	dst, err := net.Dial("tcp", "127.0.0.1:"+sshPort)
	if err != nil {
		log.Warn("ssh proxy: dial container failed", "port", sshPort, "error", err)
		return
	}
	defer dst.Close()

	done := make(chan struct{}, 2)
	copy := func(w io.Writer, r io.Reader) {
		io.Copy(w, r) //nolint:errcheck
		done <- struct{}{}
	}
	go copy(dst, src)
	go copy(src, dst)

	select {
	case <-done:
	case <-ctx.Done():
	}
}
