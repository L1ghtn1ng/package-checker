package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const defaultFeedURL = "https://socket.dev/api/public/supply-chain-attacks/%d/packages"

type config struct {
	dir       string
	cacheFile string
	feedURL   string
	showHelp  bool
	showVer   bool
}

func main() {
	os.Exit(run(context.Background(), os.Stdout, os.Stderr, os.Args[1:]))
}

func run(ctx context.Context, stdout, stderr io.Writer, args []string) int {
	client := &http.Client{Timeout: 30 * time.Second}
	return runWithClient(ctx, stdout, stderr, args, client)
}

func runWithClient(ctx context.Context, stdout, stderr io.Writer, args []string, client *http.Client) int {
	cfg, err := parseFlags(stderr, args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	if cfg.showHelp {
		printUsage(stdout)
		return 0
	}

	if cfg.showVer {
		fmt.Fprintf(stdout, "%s version %s (commit %s, built %s)\n", binaryName, version, commit, date)
		return 0
	}

	cachePath, err := resolveCachePath(cfg.cacheFile)
	if err != nil {
		fmt.Fprintf(stderr, "error: resolve cache path: %v\n", err)
		return 2
	}

	entries, source, err := loadFeed(ctx, client, cfg.feedURL, cachePath, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "error: load feed: %v\n", err)
		return 2
	}

	findings, scannedFiles, err := scanDirectory(cfg.dir, entries)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	if scannedFiles == 0 {
		fmt.Fprintf(stderr, "error: no supported package manifests found in %s\n", cfg.dir)
		return 2
	}

	fmt.Fprintf(stdout, "Feed source: %s\n", source)
	fmt.Fprintf(stdout, "Cache file: %s\n", cachePath)

	if len(findings) == 0 {
		fmt.Fprintf(stdout, "No malicious packages detected in %d package manifest(s).\n", scannedFiles)
		return 0
	}

	fmt.Fprintf(stdout, "Detected %d malicious package match(es):\n", len(findings))
	for _, finding := range findings {
		fmt.Fprintf(
			stdout,
			"- %s%s in %s [%s, %s, published %s]\n",
			finding.Dependency.Name,
			formatDependencyVersion(finding.Dependency.Version),
			finding.Dependency.Source,
			feedEcosystem(finding.Feed),
			finding.Feed.Type,
			finding.Feed.DatePublished,
		)
	}

	return 1
}

func parseFlags(stderr io.Writer, args []string) (config, error) {
	cfg := config{}

	fs := flag.NewFlagSet(binaryName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.dir, "dir", ".", "directory containing package manifests to scan")
	fs.StringVar(&cfg.cacheFile, "cache-file", "", "path to the cached malicious package feed")
	fs.StringVar(&cfg.feedURL, "feed-url", defaultFeedURL, "URL of the malicious package JSON feed")
	fs.BoolVar(&cfg.showHelp, "help", false, "show help")
	fs.BoolVar(&cfg.showVer, "version", false, "show version information")

	if err := fs.Parse(args); err != nil {
		printUsage(stderr)
		return cfg, err
	}

	if fs.NArg() > 0 {
		printUsage(stderr)
		return cfg, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}

	cfg.dir = filepath.Clean(cfg.dir)
	if cfg.feedURL == "" {
		return cfg, fmt.Errorf("feed URL cannot be empty")
	}

	return cfg, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, "Usage: %s [flags]\n\n", binaryName)
	fmt.Fprintln(w, "Scans package manifests in a directory against the Socket supply-chain attack package feed.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --dir string         Directory containing package manifests to scan (default \".\")")
	fmt.Fprintln(w, "  --cache-file string  Path to the cached malicious package feed")
	fmt.Fprintf(w, "  --feed-url string    Socket feed URL pattern to fetch; use %%d for the campaign number (default %q)\n", defaultFeedURL)
	fmt.Fprintln(w, "  --version            Show version information")
	fmt.Fprintln(w, "  --help               Show help")
}

func formatDependencyVersion(version string) string {
	if version == "" {
		return ""
	}
	return "@" + version
}

func resolveCachePath(cacheFile string) (string, error) {
	if cacheFile != "" {
		return filepath.Abs(cacheFile)
	}

	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache dir: %w", err)
	}

	return filepath.Join(cacheRoot, binaryName, "malicious-feed.json"), nil
}
