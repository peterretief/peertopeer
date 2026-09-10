package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/peterretief/peertopeer/internal/dstore"
	"github.com/peterretief/peertopeer/internal/erasure"
	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/nodeconfig"
	"github.com/peterretief/peertopeer/internal/peer"
	"github.com/peterretief/peertopeer/internal/shardserver"
	"github.com/peterretief/peertopeer/internal/tailnet"
)

func runInit(args []string) error {
	name, err := os.Hostname()
	if err != nil {
		return err
	}
	c := nodeconfig.Default(name)
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	path := fs.String("config", "node.json", "new configuration path")
	fs.StringVar(&c.Name, "name", c.Name, "this node's mesh identity name")
	fs.StringVar(&c.ShareID, "share", c.ShareID, "sharing group name")
	fs.StringVar(&c.Role, "role", c.Role, "storage or client")
	fs.StringVar(&c.Port, "port", c.Port, "agent port on the VPN interface")
	fs.StringVar(&c.Peers, "peers", "", "explicit targets as name=host[:port]")
	fs.Int64Var(&c.QuotaBytes, "quota-bytes", c.QuotaBytes, "maximum local ciphertext storage")
	fs.Int64Var(&c.MaxFileBytes, "max-file-bytes", c.MaxFileBytes, "maximum file size")
	fs.BoolVar(&c.AllowMesh, "allow-mesh", false, "allow any authenticated VPN device")
	members := fs.String("members", "", "comma-separated allowed mesh identity names")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected init arguments")
	}
	c.Members = parseExcludeList(*members)
	if !c.AllowMesh && len(c.Members) == 0 {
		return fmt.Errorf("-members is required unless -allow-mesh is explicitly selected")
	}
	// A node must also recognize requests from its own local client.
	c.Members = append(c.Members, c.Name)
	if _, _, err := parsePeers(c.Peers); err != nil {
		return err
	}
	if err := nodeconfig.SaveNew(*path, c); err != nil {
		return err
	}
	fmt.Printf("created %s; role=%s share=%s quota=%d bytes\n", *path, c.Role, c.ShareID, c.QuotaBytes)
	return nil
}

func configuredPipeline(c nodeconfig.Config) (dstore.Config, error) {
	peers, ports, err := parsePeers(c.Peers)
	if err != nil {
		return dstore.Config{}, err
	}
	ports = mergePeerPorts(parsePeerPorts(c.PeerPorts), ports)
	cfg := dstore.Config{OriginDir: c.OriginDir, ShardDir: c.ShardDir, ListenPort: c.Port,
		PeerPorts: ports, Peers: peers, ShareID: c.ShareID, MinPeers: erasure.TotalShards,
		RequireCapabilities: true, KeepOriginal: true, MaxFileBytes: c.MaxFileBytes, QuotaBytes: c.QuotaBytes}
	if !c.AllowMesh {
		cfg.Members = c.Members
	}
	return cfg, nil
}

func runNode(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("node", flag.ContinueOnError)
	path := fs.String("config", "node.json", "device configuration")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected node arguments")
	}
	c, err := nodeconfig.Load(*path)
	if err != nil {
		return err
	}
	cfg, err := configuredPipeline(c)
	if err != nil {
		return err
	}
	ip, err := tailnet.LocalIPv4(ctx)
	if err != nil {
		return err
	}
	store := localstore.WithQuota(c.ShardDir, c.QuotaBytes)
	cfg.Store = &store
	opts := shardserver.Options{Identify: peer.WhoIs, Members: c.Members, RestrictMembers: !c.AllowMesh,
		ShareID: c.ShareID, Name: c.Name, ReadOnly: c.Role == "client", MaxShardBytes: c.MaxShardBytes}
	fmt.Printf("node %s; share=%s role=%s address=%s\n", c.Name, c.ShareID, c.Role, c.ListenAddr(ip))
	return serveAndWatch(ctx, c.ListenAddr(ip), shardserver.HandlerWithOptions(store, opts), cfg, 5*time.Second)
}

func serveAndWatch(ctx context.Context, addr string, handler http.Handler, cfg dstore.Config, interval time.Duration) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: time.Minute,
		WriteTimeout: time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 8 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	serving := make(chan error, 1)
	watching := make(chan error, 1)
	go func() { serving <- srv.Serve(listener) }()
	go func() {
		watching <- dstore.Watch(ctx, cfg, interval, func(result dstore.ProcessResult) {
			fmt.Printf("created %s from %s\n", result.StubPath, result.OriginalPath)
		})
	}()
	var result error
	serverDone, watcherDone := false, false
	select {
	case result = <-serving:
		serverDone = true
	case result = <-watching:
		watcherDone = true
	case <-ctx.Done():
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := srv.Shutdown(shutdown); err != nil {
		srv.Close()
	}
	if !serverDone {
		<-serving
	}
	if !watcherDone {
		<-watching
	}
	if errors.Is(result, context.Canceled) || errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}

func meshListenAddr(ctx context.Context, addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host, err = tailnet.LocalIPv4(ctx)
		if err != nil {
			return "", err
		}
	}
	return net.JoinHostPort(host, port), nil
}

func runAdd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	path := fs.String("config", "node.json", "device configuration")
	to := fs.String("to", "", "manifest library override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("provide at least one file or directory after the flags")
	}
	c, err := nodeconfig.Load(*path)
	if err != nil {
		return err
	}
	cfg, err := configuredPipeline(c)
	if err != nil {
		return err
	}
	if *to == "" {
		*to = c.LibraryDir
	}
	for _, source := range fs.Args() {
		results, err := dstore.AddPath(ctx, source, *to, cfg)
		for _, result := range results {
			fmt.Printf("added %s -> %s (source retained)\n", result.OriginalPath, result.StubPath)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func runRestoreTree(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore-tree", flag.ContinueOnError)
	source := fs.String("source", "", "directory of manifests")
	output := fs.String("output", "", "new output directory")
	shards := fs.String("shards", "", "optional local shard fallback")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *source == "" || *output == "" {
		return fmt.Errorf("-source and -output are required")
	}
	paths, err := dstore.RestoreDirectory(ctx, *source, *output, *shards)
	for _, path := range paths {
		fmt.Printf("restored %s\n", path)
	}
	return err
}

func runPeers(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("peers", flag.ContinueOnError)
	path := fs.String("config", "", "device configuration (also checks storage readiness)")
	check := fs.Bool("check", false, "probe the storage agent")
	onlineOnly := fs.Bool("online-only", false, "show online VPN peers only")
	asJSON := fs.Bool("json", false, "machine-readable output")
	port := fs.String("port", "8080", "default peer agent port")
	peerPorts := fs.String("peer-ports", "", "hostname:port overrides")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected peers arguments")
	}
	shareID := ""
	ports := parsePeerPorts(*peerPorts)
	var peers []peer.Peer
	var c nodeconfig.Config
	var err error
	if *path != "" {
		c, err = nodeconfig.Load(*path)
		if err != nil {
			return err
		}
		cfg, err := configuredPipeline(c)
		if err != nil {
			return err
		}
		peers, ports, shareID, *port, *check = cfg.Peers, cfg.PeerPorts, cfg.ShareID, cfg.ListenPort, true
	}
	if len(peers) == 0 {
		peers, err = peer.OnlinePeers(ctx)
		if err != nil {
			return err
		}
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].HostName < peers[j].HostName })
	var visible []peer.Peer
	for _, p := range peers {
		if !*onlineOnly || p.Online {
			visible = append(visible, p)
		}
	}
	if !*check {
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(visible)
		}
		return printPeers(os.Stdout, visible, false)
	}
	statuses := peer.Inspect(ctx, visible, *port, ports, shareID)
	if *path != "" && !c.AllowMesh {
		for i := range statuses {
			allowed := false
			for _, name := range c.Members {
				if strings.EqualFold(name, statuses[i].Peer.HostName) {
					allowed = true
					break
				}
			}
			if !allowed {
				statuses[i].Ready = false
				statuses[i].Reason = "not in local member list"
			}
		}
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(statuses)
	}
	return printPeerStatus(os.Stdout, statuses)
}

func printPeerStatus(w io.Writer, statuses []peer.Status) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOSTNAME\tVPN_IP\tVPN\tSTORAGE\tUSED/QUOTA (bytes)")
	for _, status := range statuses {
		vpn := "offline"
		if status.Peer.Online {
			vpn = "online"
		}
		capacity := "-"
		if status.Info != nil {
			capacity = fmt.Sprintf("%d/%d", status.Info.UsedBytes, status.Info.QuotaBytes)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", status.Peer.HostName, status.Peer.TailIP, vpn, status.Reason, capacity)
	}
	return tw.Flush()
}
