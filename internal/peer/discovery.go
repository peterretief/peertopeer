package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/peterretief/peertopeer/internal/protocol"
)

type Status struct {
	Peer   Peer           `json:"peer"`
	Info   *protocol.Info `json:"agent,omitempty"`
	Ready  bool           `json:"ready"`
	Reason string         `json:"reason,omitempty"`
}

func Port(p Peer, defaultPort string, ports map[string]string) string {
	if port := ports[p.HostName]; port != "" {
		return port
	}
	if defaultPort == "" {
		return "8080"
	}
	return defaultPort
}

func Probe(ctx context.Context, p Peer, port, shareID string) (protocol.Info, error) {
	var info protocol.Info
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(p.TailIP, port)+"/v1/info", nil)
	if err != nil {
		return info, err
	}
	req.Header.Set(protocol.ShareHeader, shareID)
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return info, fmt.Errorf("agent unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("agent returned %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&info); err != nil {
		return info, fmt.Errorf("invalid agent response: %w", err)
	}
	if info.Service != "dstore" || info.ProtocolVersion != protocol.Version {
		return info, fmt.Errorf("incompatible agent protocol")
	}
	if info.ShareID != shareID {
		return info, fmt.Errorf("sharing group mismatch")
	}
	if info.MaxShardBytes <= 0 || info.QuotaBytes < 0 || info.UsedBytes < 0 {
		return info, fmt.Errorf("invalid storage capabilities")
	}
	return info, nil
}

// Inspect bounds concurrent probes so one offline peer cannot stall the list.
func Inspect(ctx context.Context, peers []Peer, port string, ports map[string]string, shareID string) []Status {
	statuses := make([]Status, len(peers))
	limit := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, p := range peers {
		wg.Add(1)
		go func(i int, p Peer) {
			defer wg.Done()
			statuses[i] = Status{Peer: p, Reason: "VPN offline"}
			if !p.Online {
				return
			}
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				statuses[i].Reason = ctx.Err().Error()
				return
			}
			defer func() { <-limit }()
			info, err := Probe(ctx, p, Port(p, port, ports), shareID)
			if err != nil {
				statuses[i].Reason = err.Error()
				return
			}
			statuses[i].Info = &info
			if !info.Storage {
				statuses[i].Reason = "client only"
				return
			}
			if info.QuotaBytes > 0 && info.UsedBytes >= info.QuotaBytes {
				statuses[i].Reason = "storage full"
				return
			}
			statuses[i].Ready = true
			statuses[i].Reason = "ready"
		}(i, p)
	}
	wg.Wait()
	return statuses
}
