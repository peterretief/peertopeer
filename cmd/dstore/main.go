package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/peterretief/peertopeer/internal/dstore"
	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/shardserver"
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
	case "process":
		fs := flag.NewFlagSet("process", flag.ExitOnError)
		origin := fs.String("origin", "outfiles", "directory containing files to shard")
		shards := fs.String("shards", ".dstore-shards", "directory for local shard storage")
		baseURL := fs.String("base-url", "", "public base URL used in emailed manifests, for example http://host:8080")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		results, err := dstore.ProcessDirectory(dstore.Config{OriginDir: *origin, ShardDir: *shards, BaseURL: *baseURL})
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
		baseURL := fs.String("base-url", "", "public base URL used in emailed manifests, for example http://host:8080")
		interval := fs.Duration("interval", 2*time.Second, "poll interval")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		fmt.Printf("watching %s; writing shards to %s\n", *origin, *shards)
		return dstore.Watch(dstore.Config{OriginDir: *origin, ShardDir: *shards, BaseURL: *baseURL}, *interval, func(result dstore.ProcessResult) {
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
		addr := fs.String("addr", "127.0.0.1:8080", "HTTP listen address")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		fmt.Printf("serving shards from %s at http://%s/shards/{hash}\n", *shards, *addr)
		return http.ListenAndServe(*addr, shardserver.Handler(localstore.New(*shards)))
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  dstore process [-origin outfiles] [-shards .dstore-shards] [-base-url http://host:8080]")
	fmt.Fprintln(os.Stderr, "  dstore watch   [-origin outfiles] [-shards .dstore-shards] [-base-url http://host:8080] [-interval 2s]")
	fmt.Fprintln(os.Stderr, "  dstore restore -stub outfiles/file.dstore [-shards .dstore-shards] [-output file]")
	fmt.Fprintln(os.Stderr, "  dstore serve-shards [-shards .dstore-shards] [-addr 127.0.0.1:8080]")
}
