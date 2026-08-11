package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func withSSHTunnel(ctx context.Context, profile SSHProfileConfig, remoteHost string, remotePort int, fn func(localHost string, localPort int) error) error {
	if err := validateSSHProfile(profile); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to listen local tunnel port: %w", err)
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("failed to read local tunnel address")
	}
	_ = listener.Close()

	cmd := sshTunnelCommand(ctx, profile.Host, "127.0.0.1", addr.Port, remoteHost, remotePort)
	output := strings.Builder{}
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ssh tunnel: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			slog.Info("ssh tunnel stopped", "host", profile.Host, "err", err, "output", compactText(output.String(), 500))
		}
	}()
	if err := waitLocalPort(ctx, "127.0.0.1", addr.Port); err != nil {
		return fmt.Errorf("failed to wait ssh tunnel: %w: %s", err, compactText(output.String(), 500))
	}
	return fn("127.0.0.1", addr.Port)
}

func sshTunnelCommand(ctx context.Context, sshHost string, localHost string, localPort int, remoteHost string, remotePort int) *exec.Cmd {
	forwardSpec := net.JoinHostPort(localHost, strconv.Itoa(localPort)) + ":" + net.JoinHostPort(remoteHost, strconv.Itoa(remotePort))
	return exec.CommandContext(ctx, "ssh", "-N", "-L", forwardSpec, sshHost)
}

func validateSSHProfile(profile SSHProfileConfig) error {
	if strings.TrimSpace(profile.Host) == "" {
		return fmt.Errorf("SSH host 未配置")
	}
	return nil
}

func waitLocalPort(ctx context.Context, host string, port int) error {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return fmt.Errorf("timeout waiting for %s", address)
		case <-ticker.C:
		}
	}
}
