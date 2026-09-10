package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os/exec"
	"strings"
	"time"
)

type Peer struct {
	HostName string
	TailIP   string
	Online   bool
	OS       string
}

func OnlinePeers(ctx context.Context) ([]Peer, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "tailscale", "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tailscale status --json: %w", err)
	}

	var status tailscaleStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return nil, fmt.Errorf("parse tailscale status: %w", err)
	}

	var peers []Peer
	for _, p := range status.Peer {
		if len(p.TailscaleIPs) == 0 {
			continue
		}
		ip := p.TailscaleIPs[0]
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil {
			continue
		}
		peers = append(peers, Peer{
			HostName: p.HostName,
			TailIP:   ip,
			Online:   p.Online,
			OS:       p.OS,
		})
	}
	return peers, nil
}

func RandomOnlinePeers(ctx context.Context, count int, exclude ...string) ([]Peer, error) {
	all, err := OnlinePeers(ctx)
	if err != nil {
		return nil, err
	}

	excludeSet := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		excludeSet[strings.ToLower(name)] = true
	}

	var online []Peer
	for _, p := range all {
		if p.Online && !excludeSet[strings.ToLower(p.HostName)] {
			online = append(online, p)
		}
	}

	if len(online) < count {
		return nil, fmt.Errorf("not enough online peers: got %d want %d", len(online), count)
	}

	rand.Shuffle(len(online), func(i, j int) {
		online[i], online[j] = online[j], online[i]
	})
	return online[:count], nil
}

func WhoIs(ctx context.Context, addr string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}

	ip, _, err := net.SplitHostPort(addr)
	if err != nil {
		ip = addr
	}

	cmd := exec.CommandContext(ctx, "tailscale", "whois", "--json", ip)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tailscale whois %s: %w", ip, err)
	}

	var identity tailscaleWhois
	if err := json.Unmarshal(out, &identity); err != nil {
		return "", fmt.Errorf("parse tailscale whois: %w", err)
	}

	node := strings.TrimSpace(identity.Node.ComputedName)
	if node == "" {
		node = strings.TrimSpace(identity.Node.HostName)
	}
	if node == "" {
		node = strings.TrimSuffix(strings.TrimSpace(identity.Node.Name), ".")
	}
	if node == "" {
		return "", fmt.Errorf("no hostname in whois response for %s", ip)
	}
	return node, nil
}

type tailscaleStatus struct {
	Peer map[string]tailscalePeer `json:"Peer"`
}

type tailscalePeer struct {
	TailscaleIPs []string `json:"TailscaleIPs"`
	HostName     string   `json:"HostName"`
	Online       bool     `json:"Online"`
	OS           string   `json:"OS"`
}

type tailscaleWhois struct {
	Node tailscaleWhoisNode `json:"Node"`
}

type tailscaleWhoisNode struct {
	HostName     string `json:"HostName"`
	Name         string `json:"Name"`
	ComputedName string `json:"ComputedName"`
}
