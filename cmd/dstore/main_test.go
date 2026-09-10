package main

import (
	"strings"
	"testing"

	"github.com/peterretief/peertopeer/internal/peer"
)

func TestParsePeersAcceptsHostPortSpecs(t *testing.T) {
	peers, ports, err := parsePeers("node-a=100.64.0.10:8081,node-b=storage.tailnet")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("peers: got %d want 2", len(peers))
	}
	if peers[0].HostName != "node-a" || peers[0].TailIP != "100.64.0.10" {
		t.Fatalf("peer 0: got %#v", peers[0])
	}
	if ports["node-a"] != "8081" {
		t.Fatalf("node-a port: got %q want 8081", ports["node-a"])
	}
	if peers[1].HostName != "node-b" || peers[1].TailIP != "storage.tailnet" {
		t.Fatalf("peer 1: got %#v", peers[1])
	}
}

func TestPrintPeersSortsAndFilters(t *testing.T) {
	var out strings.Builder
	err := printPeers(&out, []peer.Peer{
		{HostName: "z-node", TailIP: "100.64.0.3", Online: false},
		{HostName: "a-node", TailIP: "100.64.0.2", Online: true},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "HOSTNAME") || !strings.Contains(got, "a-node") || !strings.Contains(got, "online") {
		t.Fatalf("peer output missing expected online peer: %q", got)
	}
	if strings.Contains(got, "z-node") {
		t.Fatalf("offline peer should be filtered: %q", got)
	}
}
