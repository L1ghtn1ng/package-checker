package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOwner         = "L1ghtn1ng"
	defaultRepo          = "package-checker"
	defaultAPIURL        = "https://api.github.com"
	defaultCheckInterval = 24 * time.Hour
	maxDownloadSize      = 256 << 20
	stateFilename        = "update-state.json"
	binaryName           = "package-checker"
	macOSPackageID       = "io.github.package-checker"
	macOSPackagePath     = "/usr/local/bin/package-checker"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type CommandRunner interface {
	Output(ctx context.Context, name string, args ...string) (string, error)
	Run(ctx context.Context, name string, args ...string) error
}

type Updater struct {
	CurrentVersion string
	Commit         string
	BuildDate      string
	Owner          string
	Repo           string
	APIURL         string
	Client         HTTPClient
	Runner         CommandRunner
	Logger         *log.Logger
	GOOS           string
	GOARCH         string
	EUID           int
	ExecutablePath string
	CacheDir       string
	TempDir        string
	Now            func() time.Time
	CheckInterval  time.Duration
}

type Result struct {
	Checked        bool
	UpdateFound    bool
	InstallStarted bool
	Installed      bool
	CurrentVersion string
	LatestVersion  string
	AssetName      string
	SkippedReason  string
}

type release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

type asset struct {
	URL    string `json:"url"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type updateState struct {
	LastAttempt    time.Time `json:"last_attempt"`
	Executable     string    `json:"executable"`
	CurrentVersion string    `json:"current_version"`
}

type installKind string

const (
	installPortable installKind = "portable"
	installWindows  installKind = "windows-executable"
	installDeb      installKind = "debian-package"
	installRPM      installKind = "rpm-package"
	installArch     installKind = "arch-package"
	installMacPKG   installKind = "macos-package"
)

type realRunner struct{}

func (realRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	// Commands are selected internally from a fixed allowlist or are verified update candidates.
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("command %q failed: %w", formatCommand(name, args), err)
	}
	return string(output), nil
}

func (realRunner) Run(ctx context.Context, name string, args ...string) error {
	// Commands are selected internally from a fixed allowlist.
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command %q failed: %w", formatCommand(name, args), err)
	}
	return nil
}

func formatCommand(name string, args []string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

func New(currentVersion, commit, buildDate string, logger *log.Logger) *Updater {
	executablePath, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(executablePath); err == nil {
		executablePath = resolved
	}
	cacheRoot, _ := os.UserCacheDir()
	cacheDir := ""
	if cacheRoot != "" {
		cacheDir = filepath.Join(cacheRoot, binaryName)
	}
	if logger == nil {
		logger = log.New(os.Stderr, "", 0)
	}
	return &Updater{
		CurrentVersion: currentVersion,
		Commit:         commit,
		BuildDate:      buildDate,
		Owner:          defaultOwner,
		Repo:           defaultRepo,
		APIURL:         defaultAPIURL,
		Client:         &http.Client{Timeout: 2 * time.Minute},
		Runner:         realRunner{},
		Logger:         logger,
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		EUID:           os.Geteuid(),
		ExecutablePath: executablePath,
		CacheDir:       cacheDir,
		TempDir:        os.TempDir(),
		Now:            time.Now,
		CheckInterval:  defaultCheckInterval,
	}
}

func (updater *Updater) CheckAndInstall(ctx context.Context, force bool) (Result, error) {
	result := Result{}
	if updater != nil {
		result.CurrentVersion = updater.CurrentVersion
	}
	if err := updater.validate(); err != nil {
		return result, err
	}
	if !isReleaseBuild(updater.CurrentVersion, updater.Commit, updater.BuildDate) {
		result.SkippedReason = "development build"
		if force {
			return result, errors.New("self-update is unavailable for development builds")
		}
		return result, nil
	}
	if err := updater.cleanupBackup(); err != nil {
		updater.logger().Printf("WARN: could not remove a previous update backup: %v", err)
	}

	stateVersion := updater.CurrentVersion
	if !force {
		due, err := updater.updateCheckDue()
		if err != nil {
			updater.logger().Printf("WARN: update state could not be read: %v", err)
		}
		if !due {
			result.SkippedReason = "checked recently"
			return result, nil
		}
	}
	defer func() {
		if err := updater.writeUpdateState(stateVersion); err != nil {
			updater.logger().Printf("WARN: update state could not be saved: %v", err)
		}
	}()

	latest, err := updater.fetchLatestRelease(ctx)
	result.Checked = true
	if err != nil {
		return result, err
	}
	latestVersion, err := normalizeStableVersion(latest.TagName)
	if err != nil {
		return result, fmt.Errorf("latest release tag %q is invalid: %w", latest.TagName, err)
	}
	result.LatestVersion = latestVersion

	newer, err := isNewerVersion(updater.CurrentVersion, latestVersion)
	if err != nil {
		return result, err
	}
	if !newer {
		return result, nil
	}
	result.UpdateFound = true

	kind := updater.detectInstallKind(ctx)
	selected, err := selectAsset(latest.Assets, latestVersion, updater.GOOS, updater.GOARCH, kind)
	if err != nil {
		return result, err
	}
	result.AssetName = selected.Name

	downloadedPath, err := updater.downloadAsset(ctx, selected)
	if err != nil {
		return result, err
	}
	defer func() { _ = os.Remove(downloadedPath) }()

	result.InstallStarted = true
	if err := updater.install(ctx, downloadedPath, selected.Name, latestVersion, kind); err != nil {
		return result, err
	}
	result.Installed = true
	stateVersion = latestVersion
	return result, nil
}

func (updater *Updater) validate() error {
	if updater == nil {
		return errors.New("self updater is nil")
	}
	if updater.Client == nil {
		return errors.New("self-update HTTP client is nil")
	}
	if updater.Runner == nil {
		return errors.New("self-update command runner is nil")
	}
	if updater.Now == nil {
		return errors.New("self-update clock is nil")
	}
	if strings.TrimSpace(updater.CurrentVersion) == "" {
		return errors.New("current version is empty")
	}
	if strings.TrimSpace(updater.ExecutablePath) == "" {
		return errors.New("current executable path is empty")
	}
	if updater.Owner == "" {
		updater.Owner = defaultOwner
	}
	if updater.Repo == "" {
		updater.Repo = defaultRepo
	}
	if updater.APIURL == "" {
		updater.APIURL = defaultAPIURL
	}
	if updater.GOOS == "" {
		updater.GOOS = runtime.GOOS
	}
	if updater.GOARCH == "" {
		updater.GOARCH = runtime.GOARCH
	}
	if updater.TempDir == "" {
		updater.TempDir = os.TempDir()
	}
	if updater.CheckInterval <= 0 {
		updater.CheckInterval = defaultCheckInterval
	}
	return nil
}

func (updater *Updater) logger() *log.Logger {
	if updater != nil && updater.Logger != nil {
		return updater.Logger
	}
	return log.New(io.Discard, "", 0)
}

func (updater *Updater) statePath() string {
	return filepath.Join(updater.CacheDir, stateFilename)
}

func (updater *Updater) updateCheckDue() (bool, error) {
	if strings.TrimSpace(updater.CacheDir) == "" {
		return true, errors.New("user cache directory is unavailable")
	}
	currentVersion, err := normalizeStableVersion(updater.CurrentVersion)
	if err != nil {
		return true, fmt.Errorf("normalize current version for update state: %w", err)
	}
	// The state path is fixed beneath the user's cache directory.
	data, err := os.ReadFile(updater.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	var state updateState
	if err := json.Unmarshal(data, &state); err != nil {
		return true, err
	}
	if state.Executable != filepath.Clean(updater.ExecutablePath) || state.CurrentVersion != currentVersion {
		return true, nil
	}
	elapsed := updater.Now().UTC().Sub(state.LastAttempt)
	return elapsed >= updater.CheckInterval, nil
}

func (updater *Updater) writeUpdateState(currentVersion string) error {
	if strings.TrimSpace(updater.CacheDir) == "" {
		return errors.New("user cache directory is unavailable")
	}
	normalizedVersion, err := normalizeStableVersion(currentVersion)
	if err != nil {
		return fmt.Errorf("normalize version for update state: %w", err)
	}
	if err := os.MkdirAll(updater.CacheDir, 0o700); err != nil {
		return err
	}
	state := updateState{
		LastAttempt:    updater.Now().UTC(),
		Executable:     filepath.Clean(updater.ExecutablePath),
		CurrentVersion: normalizedVersion,
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(updater.CacheDir, ".update-state-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, updater.statePath()); err == nil {
		return nil
	} else if updater.GOOS != "windows" {
		return err
	}
	// Windows cannot replace an existing file with os.Rename. This state is a
	// cache, so a remove-and-rename fallback is preferable to checking forever.
	if err := os.Remove(updater.statePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tmpPath, updater.statePath())
}

func (updater *Updater) fetchLatestRelease(ctx context.Context) (release, error) {
	endpoint := strings.TrimRight(updater.APIURL, "/") + "/repos/" + url.PathEscape(updater.Owner) + "/" + url.PathEscape(updater.Repo) + "/releases/latest"
	if err := validateHTTPSURL(endpoint); err != nil {
		return release{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return release{}, err
	}
	setGitHubHeaders(req)
	resp, err := updater.Client.Do(req)
	if err != nil {
		return release{}, err
	}
	defer closeResponseBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return release{}, fmt.Errorf("GitHub latest release request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var latest release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&latest); err != nil {
		return release{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	if latest.Draft || latest.Prerelease {
		return release{}, fmt.Errorf("GitHub latest release %q is not stable", latest.TagName)
	}
	if strings.TrimSpace(latest.TagName) == "" {
		return release{}, errors.New("GitHub latest release response is missing tag_name")
	}
	return latest, nil
}

func setGitHubHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", binaryName)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

func validateHTTPSURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("self-update URL must use HTTPS: %q", rawURL)
	}
	return nil
}

func (updater *Updater) downloadAsset(ctx context.Context, selected asset) (string, error) {
	if selected.State != "uploaded" {
		return "", fmt.Errorf("release asset %q is not uploaded", selected.Name)
	}
	if selected.Size <= 0 || selected.Size > maxDownloadSize {
		return "", fmt.Errorf("release asset %q has invalid size %d", selected.Name, selected.Size)
	}
	expectedDigest, err := parseSHA256Digest(selected.Digest)
	if err != nil {
		return "", fmt.Errorf("release asset %q: %w", selected.Name, err)
	}
	if err := validateHTTPSURL(selected.URL); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, selected.URL, nil)
	if err != nil {
		return "", err
	}
	setGitHubHeaders(req)
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := updater.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer closeResponseBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download release asset %q failed: %s", selected.Name, resp.Status)
	}

	// Preserve the release suffix so Windows can execute a downloaded .exe
	// during candidate verification.
	tmp, err := os.CreateTemp(updater.TempDir, binaryName+"-update-*"+filepath.Ext(selected.Name))
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, selected.Size+1))
	if err != nil {
		return "", err
	}
	if written != selected.Size {
		return "", fmt.Errorf("release asset %q size mismatch: got %d, want %d", selected.Name, written, selected.Size)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedDigest) {
		return "", fmt.Errorf("release asset %q SHA-256 digest mismatch", selected.Name)
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	keep = true
	return tmpPath, nil
}

func parseSHA256Digest(digest string) (string, error) {
	value, ok := strings.CutPrefix(strings.TrimSpace(digest), "sha256:")
	if !ok {
		return "", errors.New("missing sha256 digest")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("invalid sha256 digest")
	}
	return value, nil
}

func closeResponseBody(body io.Closer) {
	_ = body.Close()
}

func isReleaseBuild(currentVersion, commit, buildDate string) bool {
	if strings.TrimSpace(commit) == "" || commit == "none" || strings.TrimSpace(buildDate) == "" || buildDate == "unknown" {
		return false
	}
	_, err := normalizeStableVersion(currentVersion)
	return err == nil
}

func normalizeStableVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "refs/tags/")
	value = strings.TrimPrefix(value, "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", errors.New("expected major.minor.patch")
	}
	for _, part := range parts {
		if part == "" {
			return "", errors.New("empty version component")
		}
		if len(part) > 1 && part[0] == '0' {
			return "", errors.New("version component has a leading zero")
		}
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return "", errors.New("version component is not numeric")
		}
	}
	return value, nil
}

func isNewerVersion(current, candidate string) (bool, error) {
	rawCurrent := current
	current, err := normalizeStableVersion(current)
	if err != nil {
		return false, fmt.Errorf("current version %q is invalid: %w", rawCurrent, err)
	}
	rawCandidate := candidate
	candidate, err = normalizeStableVersion(candidate)
	if err != nil {
		return false, fmt.Errorf("candidate version %q is invalid: %w", rawCandidate, err)
	}
	currentParts := strings.Split(current, ".")
	candidateParts := strings.Split(candidate, ".")
	for index := range currentParts {
		currentValue, _ := strconv.ParseUint(currentParts[index], 10, 64)
		candidateValue, _ := strconv.ParseUint(candidateParts[index], 10, 64)
		if candidateValue != currentValue {
			return candidateValue > currentValue, nil
		}
	}
	return false, nil
}
