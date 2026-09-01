package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"package-checker/internal/selfupdate"
)

const defaultFeedURL = "https://socket.dev/api/public/supply-chain-attacks/%d/packages"

type config struct {
	dir          string
	cacheFile    string
	feedURL      string
	showHelp     bool
	showVer      bool
	selfUpdate   bool
	noSelfUpdate bool
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
		return reportRunError(stderr, "%v", err)
	}

	if cfg.showHelp {
		if err := printUsage(stdout); err != nil {
			return reportRunError(stderr, "write output: %v", err)
		}
		return 0
	}

	if cfg.showVer {
		if err := writeFormatted(stdout, "%s version %s (commit %s, built %s)\n", binaryName, version, commit, date); err != nil {
			return reportRunError(stderr, "write output: %v", err)
		}
		return 0
	}

	if cfg.selfUpdate {
		result, err := runSelfUpdate(ctx, stderr, true)
		if err != nil {
			return reportRunError(stderr, "self-update: %v", err)
		}
		if result.Installed {
			if err := writeFormatted(stdout, "Updated %s from %s to %s.\n", binaryName, result.CurrentVersion, result.LatestVersion); err != nil {
				return reportRunError(stderr, "write output: %v", err)
			}
		} else if err := writeFormatted(stdout, "%s %s is already up to date.\n", binaryName, result.CurrentVersion); err != nil {
			return reportRunError(stderr, "write output: %v", err)
		}
		return 0
	}

	if !cfg.noSelfUpdate {
		result, updateErr := runSelfUpdate(ctx, stderr, false)
		if updateErr != nil {
			reportRunWarning(stderr, "self-update check failed: %v", updateErr)
		} else if result.Installed {
			reportRunWarning(stderr, "updated %s from %s to %s; the new version will be used on the next invocation", binaryName, result.CurrentVersion, result.LatestVersion)
		}
	}

	cachePath, err := resolveCachePath(cfg.cacheFile)
	if err != nil {
		return reportRunError(stderr, "resolve cache path: %v", err)
	}

	entries, source, err := loadFeed(ctx, client, cfg.feedURL, cachePath, time.Now())
	if err != nil {
		return reportRunError(stderr, "load feed: %v", err)
	}

	findings, scannedFiles, err := scanDirectory(cfg.dir, entries)
	if err != nil {
		return reportRunError(stderr, "%v", err)
	}

	if scannedFiles == 0 {
		return reportRunError(stderr, "no supported package manifests found in %s", cfg.dir)
	}

	if err := writeFormatted(stdout, "Feed source: %s\n", source); err != nil {
		return reportRunError(stderr, "write output: %v", err)
	}
	if err := writeFormatted(stdout, "Cache file: %s\n", cachePath); err != nil {
		return reportRunError(stderr, "write output: %v", err)
	}

	if len(findings) == 0 {
		if err := writeFormatted(stdout, "No malicious packages detected in %d package manifest(s).\n", scannedFiles); err != nil {
			return reportRunError(stderr, "write output: %v", err)
		}
		return 0
	}

	if err := writeFormatted(stdout, "Detected %d malicious package match(es):\n", len(findings)); err != nil {
		return reportRunError(stderr, "write output: %v", err)
	}
	for _, finding := range findings {
		if err := writeFormatted(
			stdout,
			"- %s%s in %s [%s, %s, published %s]\n",
			finding.Dependency.Name,
			formatDependencyVersion(finding.Dependency.Version),
			finding.Dependency.Source,
			feedEcosystem(finding.Feed),
			finding.Feed.Type,
			finding.Feed.DatePublished,
		); err != nil {
			return reportRunError(stderr, "write output: %v", err)
		}
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
	fs.BoolVar(&cfg.selfUpdate, "self-update", false, "install the latest stable GitHub release and exit")
	fs.BoolVar(&cfg.noSelfUpdate, "no-self-update", false, "skip the automatic GitHub release check")

	if err := fs.Parse(args); err != nil {
		return cfg, withUsageError(stderr, err)
	}

	if fs.NArg() > 0 {
		return cfg, withUsageError(stderr, fmt.Errorf("unexpected positional arguments: %v", fs.Args()))
	}
	if cfg.selfUpdate && cfg.noSelfUpdate {
		return cfg, withUsageError(stderr, errors.New("--self-update and --no-self-update cannot be used together"))
	}

	cfg.dir = filepath.Clean(cfg.dir)
	if cfg.feedURL == "" {
		return cfg, fmt.Errorf("feed URL cannot be empty")
	}

	return cfg, nil
}

func printUsage(w io.Writer) error {
	return writeFormatted(
		w,
		"Usage: %s [flags]\n\n"+
			"Scans package manifests in a directory against the Socket supply-chain attack package feed.\n\n"+
			"Flags:\n"+
			"  --dir string         Directory containing package manifests to scan (default \".\")\n"+
			"  --cache-file string  Path to the cached malicious package feed\n"+
			"  --feed-url string    Socket feed URL pattern to fetch; use %%d for the campaign number (default %q)\n"+
			"  --self-update        Install the latest stable GitHub release and exit\n"+
			"  --no-self-update     Skip the automatic GitHub release check\n"+
			"  --version            Show version information\n"+
			"  --help               Show help\n",
		binaryName,
		defaultFeedURL,
	)
}

func withUsageError(w io.Writer, cause error) error {
	if err := printUsage(w); err != nil {
		return errors.Join(cause, fmt.Errorf("write usage: %w", err))
	}
	return cause
}

func writeFormatted(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func reportRunError(w io.Writer, format string, args ...any) int {
	// There is no useful recovery if the error stream itself is unavailable.
	_ = writeFormatted(w, "error: "+format+"\n", args...)
	return 2
}

func reportRunWarning(w io.Writer, format string, args ...any) {
	// A failed warning write cannot affect the requested scan or update operation.
	_ = writeFormatted(w, "warning: "+format+"\n", args...)
}

func runSelfUpdate(ctx context.Context, stderr io.Writer, force bool) (selfupdate.Result, error) {
	updater := selfupdate.New(version, commit, date, log.New(stderr, "", 0))
	return updater.CheckAndInstall(ctx, force)
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
