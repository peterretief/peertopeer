package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/peterretief/peertopeer/internal/dstore"
	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/manifest"
	"github.com/peterretief/peertopeer/internal/peer"
	"github.com/peterretief/peertopeer/internal/shardserver"
	"github.com/peterretief/peertopeer/internal/tailnet"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dstore:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	switch args[0] {
	case "init":
		return runInit(args[1:])
	case "node":
		return runNode(ctx, args[1:])
	case "add":
		return runAdd(ctx, args[1:])
	case "restore-tree":
		return runRestoreTree(ctx, args[1:])
	case "peers":
		return runPeers(ctx, args[1:])
	case "agent":
		fs := flag.NewFlagSet("agent", flag.ExitOnError)
		origin := fs.String("origin", "outfiles", "directory to watch for files to shard")
		shards := fs.String("shards", ".dstore-shards", "directory for local shard storage")
		addr := fs.String("addr", ":8080", "HTTP shard server listen address")
		baseURL := fs.String("base-url", "", "base URL used in emailed manifests; defaults to this node's Tailscale IPv4 on the server port")
		interval := fs.Duration("interval", 2*time.Second, "watch poll interval")
		peerPorts := fs.String("peer-ports", "", "comma-separated hostname:port pairs for peers (e.g. headscale-server:8081)")
		peersFlag := fs.String("peers", "", "comma-separated shard peers as hostname=host[:port] (overrides Tailscale auto-selection)")
		excludePeersFlag := fs.String("exclude-peers", "", "comma-separated hostnames to exclude from auto peer selection")
		maxFileBytes := fs.Int64("max-file-bytes", 0, "maximum input file size in bytes (default 64 MiB)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		port, err := listenPort(*addr)
		if err != nil {
			return err
		}
		*addr, err = meshListenAddr(ctx, *addr)
		if err != nil {
			return err
		}
		resolvedBaseURL, err := resolveBaseURL(*baseURL, port)
		if err != nil {
			return err
		}
		peerPortsMap := parsePeerPorts(*peerPorts)
		configuredPeers, configuredPeerPorts, err := parsePeers(*peersFlag)
		if err != nil {
			return err
		}
		peerPortsMap = mergePeerPorts(peerPortsMap, configuredPeerPorts)
		excludeList := parseExcludeList(*excludePeersFlag)
		fmt.Printf("serving shards from %s at http://%s/shards/{hash}\n", *shards, displayAddr(*addr))
		fmt.Printf("watching %s; manifest URLs use %s\n", *origin, resolvedBaseURL)
		cfg := dstore.Config{OriginDir: *origin, ShardDir: *shards, BaseURL: resolvedBaseURL, ListenPort: port, PeerPorts: peerPortsMap, Peers: configuredPeers, ExcludePeers: excludeList, MaxFileBytes: *maxFileBytes}
		return serveAndWatch(ctx, *addr, shardserver.HandlerWithIdentity(localstore.New(*shards), peer.WhoIs), cfg, *interval)
	case "process":
		fs := flag.NewFlagSet("process", flag.ExitOnError)
		origin := fs.String("origin", "outfiles", "directory containing files to shard")
		shards := fs.String("shards", ".dstore-shards", "directory for local shard storage")
		baseURL := fs.String("base-url", "", "base URL used in emailed manifests; defaults to this node's Tailscale IPv4 on port 8080")
		port := fs.String("port", "8080", "port used when auto-detecting the Tailscale base URL")
		peersFlag := fs.String("peers", "", "comma-separated shard peers as hostname=host[:port] (overrides Tailscale auto-selection)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		resolvedBaseURL, err := resolveBaseURL(*baseURL, *port)
		if err != nil {
			return err
		}
		configuredPeers, peerPortsMap, err := parsePeers(*peersFlag)
		if err != nil {
			return err
		}
		cfg := dstore.Config{OriginDir: *origin, ShardDir: *shards, BaseURL: resolvedBaseURL, ListenPort: *port, PeerPorts: peerPortsMap, Peers: configuredPeers}
		results, err := dstore.ProcessDirectory(ctx, cfg)
		if err != nil {
			return err
		}
		for _, result := range results {
			fmt.Printf("created %s from %s\n", result.StubPath, result.OriginalPath)
		}
		return nil
	case "watch":
		fs := flag.NewFlagSet("watch", flag.ExitOnError)
		origin := fs.String("origin", "outfiles", "directory to watch for files to shard")
		shards := fs.String("shards", ".dstore-shards", "directory for local shard storage")
		baseURL := fs.String("base-url", "", "base URL used in emailed manifests; defaults to this node's Tailscale IPv4 on port 8080")
		port := fs.String("port", "8080", "port used when auto-detecting the Tailscale base URL")
		peersFlag := fs.String("peers", "", "comma-separated shard peers as hostname=host[:port] (overrides Tailscale auto-selection)")
		interval := fs.Duration("interval", 2*time.Second, "poll interval")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		resolvedBaseURL, err := resolveBaseURL(*baseURL, *port)
		if err != nil {
			return err
		}
		configuredPeers, peerPortsMap, err := parsePeers(*peersFlag)
		if err != nil {
			return err
		}
		fmt.Printf("watching %s; writing shards to %s; manifest URLs use %s\n", *origin, *shards, resolvedBaseURL)
		cfg := dstore.Config{OriginDir: *origin, ShardDir: *shards, BaseURL: resolvedBaseURL, ListenPort: *port, PeerPorts: peerPortsMap, Peers: configuredPeers}
		return dstore.Watch(ctx, cfg, *interval, func(result dstore.ProcessResult) {
			fmt.Printf("created %s from %s\n", result.StubPath, result.OriginalPath)
		})
	case "restore":
		fs := flag.NewFlagSet("restore", flag.ExitOnError)
		stub := fs.String("stub", "", "manifest stub path to restore")
		shards := fs.String("shards", "", "optional local shard directory fallback")
		output := fs.String("output", "", "restored output path; defaults to stub path without .dstore")
		outputDir := fs.String("output-dir", "", "directory to restore into; uses original filename")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *stub == "" {
			return fmt.Errorf("-stub is required")
		}
		if *outputDir != "" && *output == "" {
			if err := os.MkdirAll(*outputDir, 0o755); err != nil {
				return fmt.Errorf("create output dir: %w", err)
			}
			stubBytes, err := os.ReadFile(*stub)
			if err != nil {
				return fmt.Errorf("read stub: %w", err)
			}
			m, err := manifest.Unmarshal(stubBytes)
			if err != nil {
				return err
			}
			*output = filepath.Join(*outputDir, m.FileName)
		}
		path, err := dstore.RestoreFile(ctx, *stub, *shards, *output)
		if err != nil {
			return err
		}
		fmt.Printf("restored %s\n", path)
		return nil
	case "serve-shards":
		fs := flag.NewFlagSet("serve-shards", flag.ExitOnError)
		shards := fs.String("shards", ".dstore-shards", "directory containing local shards")
		addr := fs.String("addr", ":8080", "HTTP listen address")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		fmt.Printf("serving shards from %s at http://%s/shards/{hash}\n", *shards, displayAddr(*addr))
		boundAddr, err := meshListenAddr(ctx, *addr)
		if err != nil {
			return err
		}
		srv := &http.Server{
			Addr:              boundAddr,
			Handler:           shardserver.HandlerWithIdentity(localstore.New(*shards), peer.WhoIs),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: time.Minute, WriteTimeout: time.Minute, IdleTimeout: time.Minute,
		}
		go func() {
			<-ctx.Done()
			srv.Close()
		}()
		err = srv.ListenAndServe()
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func resolveBaseURL(baseURL, port string) (string, error) {
	if strings.TrimSpace(baseURL) != "" {
		return strings.TrimRight(baseURL, "/"), nil
	}
	resolved, err := tailnet.LocalBaseURL(context.Background(), port)
	if err != nil {
		return "", fmt.Errorf("auto-detect tailscale base URL: %w; pass -base-url explicitly if needed", err)
	}
	return resolved, nil
}

func listenPort(addr string) (string, error) {
	_, port, err := net.SplitHostPort(addr)
	if err == nil {
		return port, nil
	}
	if strings.HasPrefix(addr, ":") && len(addr) > 1 {
		return strings.TrimPrefix(addr, ":"), nil
	}
	return "", fmt.Errorf("listen address %q must include a port", addr)
}

func displayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, port)
}

func printPeers(w io.Writer, peers []peer.Peer, onlineOnly bool) error {
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].HostName == peers[j].HostName {
			return peers[i].TailIP < peers[j].TailIP
		}
		return peers[i].HostName < peers[j].HostName
	})

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "HOSTNAME	TAILSCALE_IP	STATUS"); err != nil {
		return err
	}
	for _, p := range peers {
		if onlineOnly && !p.Online {
			continue
		}
		status := "offline"
		if p.Online {
			status = "online"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", p.HostName, p.TailIP, status); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func parsePeers(s string) ([]peer.Peer, map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil, nil
	}

	var peers []peer.Peer
	ports := make(map[string]string)
	for _, spec := range strings.Split(s, ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}

		name, addr := splitPeerSpec(spec)
		host := addr
		if h, port, err := net.SplitHostPort(addr); err == nil {
			host = h
			ports[name] = port
		}

		host = strings.Trim(host, "[]")
		if name == "" || host == "" {
			return nil, nil, fmt.Errorf("invalid peer %q; use hostname=host[:port]", spec)
		}
		peers = append(peers, peer.Peer{HostName: name, TailIP: host, Online: true})
	}
	return peers, ports, nil
}

func splitPeerSpec(spec string) (string, string) {
	for _, sep := range []string{"=", "@"} {
		if before, after, ok := strings.Cut(spec, sep); ok {
			return strings.TrimSpace(before), strings.TrimSpace(after)
		}
	}
	return spec, spec
}

func mergePeerPorts(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]string, len(src))
	}
	for host, port := range src {
		dst[host] = port
	}
	return dst
}

func parsePeerPorts(s string) map[string]string {
	if s == "" {
		return nil
	}
	m := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	return m
}

func parseExcludeList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var result []string
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			result = append(result, name)
		}
	}
	return result
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  dstore init [-config node.json] -members node-a,node-b,node-c [-share personal] [-role storage|client]")
	fmt.Fprintln(os.Stderr, "  dstore node -config node.json")
	fmt.Fprintln(os.Stderr, "  dstore add -config node.json /path/to/file-or-directory [...]")
	fmt.Fprintln(os.Stderr, "  dstore restore-tree -source library/folder -output restored-folder [-shards .dstore-shards]")
	fmt.Fprintln(os.Stderr, "  dstore peers   [-online-only]")
	fmt.Fprintln(os.Stderr, "  dstore agent   [-origin outfiles] [-shards .dstore-shards] [-addr :8080] [-base-url http://tailscale-ip:8080] [-peers node=100.x.y.z[:8080]] [-exclude-peers Tim_Laptop,Redmi A5] [-peer-ports node:8081]")
	fmt.Fprintln(os.Stderr, "  dstore process [-origin outfiles] [-shards .dstore-shards] [-base-url http://tailscale-ip:8080] [-peers node=100.x.y.z[:8080]]")
	fmt.Fprintln(os.Stderr, "  dstore watch   [-origin outfiles] [-shards .dstore-shards] [-base-url http://tailscale-ip:8080] [-peers node=100.x.y.z[:8080]] [-interval 2s]")
	fmt.Fprintln(os.Stderr, "  dstore restore -stub outfiles/file.dstore [-shards .dstore-shards] [-output file] [-output-dir ./restored]")
	fmt.Fprintln(os.Stderr, "  dstore serve-shards [-shards .dstore-shards] [-addr :8080]")
}
