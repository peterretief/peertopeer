package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/peterretief/peertopeer/internal/dstore"
	"github.com/peterretief/peertopeer/internal/localstore"
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

	switch args[0] {
	case "agent":
		fs := flag.NewFlagSet("agent", flag.ExitOnError)
		origin := fs.String("origin", "outfiles", "directory to watch for files to shard")
		shards := fs.String("shards", ".dstore-shards", "directory for local shard storage")
		addr := fs.String("addr", ":8080", "HTTP shard server listen address")
		baseURL := fs.String("base-url", "", "base URL used in emailed manifests; defaults to this node's Tailscale IPv4 on the server port")
		interval := fs.Duration("interval", 2*time.Second, "watch poll interval")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		port, err := listenPort(*addr)
		if err != nil {
			return err
		}
		resolvedBaseURL, err := resolveBaseURL(*baseURL, port)
		if err != nil {
			return err
		}
		fmt.Printf("serving shards from %s at http://%s/shards/{hash}\n", *shards, displayAddr(*addr))
		fmt.Printf("watching %s; manifest URLs use %s\n", *origin, resolvedBaseURL)
		go func() {
			if err := http.ListenAndServe(*addr, shardserver.Handler(localstore.New(*shards))); err != nil && err != http.ErrServerClosed {
				fmt.Fprintln(os.Stderr, "dstore: shard server:", err)
				os.Exit(1)
			}
		}()
		return dstore.Watch(dstore.Config{OriginDir: *origin, ShardDir: *shards, BaseURL: resolvedBaseURL}, *interval, func(result dstore.ProcessResult) {
			fmt.Printf("created %s from %s\n", result.StubPath, result.OriginalPath)
		})
	case "process":
		fs := flag.NewFlagSet("process", flag.ExitOnError)
		origin := fs.String("origin", "outfiles", "directory containing files to shard")
		shards := fs.String("shards", ".dstore-shards", "directory for local shard storage")
		baseURL := fs.String("base-url", "", "base URL used in emailed manifests; defaults to this node's Tailscale IPv4 on port 8080")
		port := fs.String("port", "8080", "port used when auto-detecting the Tailscale base URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		resolvedBaseURL, err := resolveBaseURL(*baseURL, *port)
		if err != nil {
			return err
		}
		results, err := dstore.ProcessDirectory(dstore.Config{OriginDir: *origin, ShardDir: *shards, BaseURL: resolvedBaseURL})
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
		interval := fs.Duration("interval", 2*time.Second, "poll interval")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		resolvedBaseURL, err := resolveBaseURL(*baseURL, *port)
		if err != nil {
			return err
		}
		fmt.Printf("watching %s; writing shards to %s; manifest URLs use %s\n", *origin, *shards, resolvedBaseURL)
		return dstore.Watch(dstore.Config{OriginDir: *origin, ShardDir: *shards, BaseURL: resolvedBaseURL}, *interval, func(result dstore.ProcessResult) {
			fmt.Printf("created %s from %s\n", result.StubPath, result.OriginalPath)
		})
	case "restore":
		fs := flag.NewFlagSet("restore", flag.ExitOnError)
		stub := fs.String("stub", "", "manifest stub path to restore")
		shards := fs.String("shards", "", "optional local shard directory fallback")
		output := fs.String("output", "", "restored output path; defaults to stub path without .dstore")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *stub == "" {
			return fmt.Errorf("-stub is required")
		}
		path, err := dstore.RestoreFile(*stub, *shards, *output)
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
		return http.ListenAndServe(*addr, shardserver.Handler(localstore.New(*shards)))
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

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  dstore agent   [-origin outfiles] [-shards .dstore-shards] [-addr :8080] [-base-url http://tailscale-ip:8080]")
	fmt.Fprintln(os.Stderr, "  dstore process [-origin outfiles] [-shards .dstore-shards] [-base-url http://tailscale-ip:8080]")
	fmt.Fprintln(os.Stderr, "  dstore watch   [-origin outfiles] [-shards .dstore-shards] [-base-url http://tailscale-ip:8080] [-interval 2s]")
	fmt.Fprintln(os.Stderr, "  dstore restore -stub outfiles/file.dstore [-shards .dstore-shards] [-output file]")
	fmt.Fprintln(os.Stderr, "  dstore serve-shards [-shards .dstore-shards] [-addr :8080]")
}
