package tailnet

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

func LocalIPv4(ctx context.Context) (string, error) {
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) <= 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "tailscale", "ip", "-4")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tailscale ip -4: %w", err)
	}

	for _, line := range bytes.Split(out, []byte("\n")) {
		ip := strings.TrimSpace(string(line))
		if ip == "" {
			continue
		}
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil {
			continue
		}
		return ip, nil
	}
	return "", fmt.Errorf("no tailscale IPv4 address found")
}

func LocalBaseURL(ctx context.Context, port string) (string, error) {
	if port == "" {
		port = "8080"
	}
	ip, err := LocalIPv4(ctx)
	if err != nil {
		return "", err
	}
	return "http://" + formatHostPort(ip, port), nil
}

func formatHostPort(host, port string) string {
	return net.JoinHostPort(host, port)
}
