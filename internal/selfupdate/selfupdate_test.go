package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeStableVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "plain", value: "2.3.4", want: "2.3.4"},
		{name: "tag", value: "v2.3.4", want: "2.3.4"},
		{name: "tag ref", value: "refs/tags/v2.3.4", want: "2.3.4"},
		{name: "prerelease", value: "2.3.4-rc.1", wantErr: true},
		{name: "missing patch", value: "2.3", wantErr: true},
		{name: "leading zero", value: "2.03.4", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeStableVersion(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("normalizeStableVersion(%q) error = %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("normalizeStableVersion(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestIsNewerVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		current   string
		candidate string
		want      bool
	}{
		{current: "1.9.9", candidate: "2.0.0", want: true},
		{current: "2.0.0", candidate: "2.0.1", want: true},
		{current: "2.1.0", candidate: "2.1.0", want: false},
		{current: "2.2.0", candidate: "2.1.9", want: false},
	}
	for _, test := range tests {
		got, err := isNewerVersion(test.current, test.candidate)
		if err != nil {
			t.Fatalf("isNewerVersion(%q, %q): %v", test.current, test.candidate, err)
		}
		if got != test.want {
			t.Fatalf("isNewerVersion(%q, %q) = %t, want %t", test.current, test.candidate, got, test.want)
		}
	}
}

func TestReleaseBuildEligibility(t *testing.T) {
	t.Parallel()

	if !isReleaseBuild("2.1.0", "abc123", "2026-09-01T10:00:00Z") {
		t.Fatal("expected injected release metadata to be eligible")
	}
	if isReleaseBuild("2.1.0", "none", "unknown") {
		t.Fatal("development build must not self-update")
	}
	if isReleaseBuild("dev", "abc123", "2026-09-01T10:00:00Z") {
		t.Fatal("non-release version must not self-update")
	}
}

func TestUpdateCheckDue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	updater := testUpdater(t)
	updater.Now = func() time.Time { return now }

	due, err := updater.updateCheckDue()
	if err != nil || !due {
		t.Fatalf("new updater due = %t, error = %v", due, err)
	}
	if err := updater.writeUpdateState(updater.CurrentVersion); err != nil {
		t.Fatalf("write update state: %v", err)
	}

	now = now.Add(23 * time.Hour)
	due, err = updater.updateCheckDue()
	if err != nil || due {
		t.Fatalf("recent check due = %t, error = %v", due, err)
	}
	now = now.Add(2 * time.Hour)
	due, err = updater.updateCheckDue()
	if err != nil || !due {
		t.Fatalf("expired check due = %t, error = %v", due, err)
	}

	now = now.Add(-48 * time.Hour)
	due, err = updater.updateCheckDue()
	if err != nil || due {
		t.Fatalf("future timestamp due = %t, error = %v", due, err)
	}
	updater.CurrentVersion = "2.0.1"
	due, err = updater.updateCheckDue()
	if err != nil || !due {
		t.Fatalf("changed version due = %t, error = %v", due, err)
	}
}

func TestUpdateCheckDueNormalizesTaggedCurrentVersion(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	if err := updater.writeUpdateState("2.0.0"); err != nil {
		t.Fatalf("write update state: %v", err)
	}
	updater.CurrentVersion = "v2.0.0"

	due, err := updater.updateCheckDue()
	if err != nil {
		t.Fatalf("updateCheckDue: %v", err)
	}
	if due {
		t.Fatal("tagged form of the stored version triggered an immediate update check")
	}
}

func TestExpectedAssetName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		goos   string
		goarch string
		kind   installKind
		want   string
	}{
		{name: "linux archive", goos: "linux", goarch: "amd64", kind: installPortable, want: "package-checker_2.1.0_linux_amd64.tar.gz"},
		{name: "mac archive", goos: "darwin", goarch: "arm64", kind: installPortable, want: "package-checker_2.1.0_darwin_arm64.tar.gz"},
		{name: "windows", goos: "windows", goarch: "arm64", kind: installWindows, want: "package-checker_2.1.0_windows_arm64.exe"},
		{name: "deb", goos: "linux", goarch: "arm64", kind: installDeb, want: "package-checker_2.1.0_arm64.deb"},
		{name: "rpm", goos: "linux", goarch: "amd64", kind: installRPM, want: "package-checker-2.1.0-1.x86_64.rpm"},
		{name: "arch", goos: "linux", goarch: "arm64", kind: installArch, want: "package-checker-2.1.0-1-aarch64.pkg.tar.zst"},
		{name: "mac package", goos: "darwin", goarch: "amd64", kind: installMacPKG, want: "package-checker_v2.1.0_darwin_amd64.pkg"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := expectedAssetName("2.1.0", test.goos, test.goarch, test.kind)
			if err != nil {
				t.Fatalf("expectedAssetName: %v", err)
			}
			if got != test.want {
				t.Fatalf("expectedAssetName = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSelectAssetRequiresExactName(t *testing.T) {
	t.Parallel()

	assets := []asset{{Name: "package-checker_2.1.0_linux_arm64.tar.gz"}}
	if _, err := selectAsset(assets, "2.1.0", "linux", "amd64", installPortable); err == nil {
		t.Fatal("expected exact architecture mismatch to fail")
	}
	if _, err := expectedAssetName("2.1.0", "linux", "386", installPortable); err == nil {
		t.Fatal("expected unsupported architecture to fail")
	}
}

func TestAutomaticCheckSkipsRecentState(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	client := &fakeHTTPClient{}
	updater.Client = client
	if err := updater.writeUpdateState(updater.CurrentVersion); err != nil {
		t.Fatalf("write update state: %v", err)
	}

	result, err := updater.CheckAndInstall(context.Background(), false)
	if err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}
	if result.Checked || result.SkippedReason != "checked recently" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(client.requests) != 0 {
		t.Fatalf("made %d HTTP request(s), want none", len(client.requests))
	}
}

func TestForcedCheckNoNewerRelease(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	latest := release{TagName: "v2.0.0"}
	updater.Client = releaseClient(t, updater, latest, nil)

	result, err := updater.CheckAndInstall(context.Background(), true)
	if err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}
	if !result.Checked || result.UpdateFound || result.Installed {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDigestMismatchPreventsInstall(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	assetName, err := expectedAssetName("2.1.0", updater.GOOS, updater.GOARCH, installPortable)
	if err != nil {
		t.Fatalf("expected asset: %v", err)
	}
	download := []byte("not the expected release")
	latest := release{
		TagName: "v2.1.0",
		Assets: []asset{{
			URL:    "https://api.github.test/assets/1",
			Name:   assetName,
			State:  "uploaded",
			Size:   int64(len(download)),
			Digest: "sha256:" + strings.Repeat("0", 64),
		}},
	}
	updater.Client = releaseClient(t, updater, latest, download)

	result, err := updater.CheckAndInstall(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("CheckAndInstall error = %v", err)
	}
	if !result.UpdateFound || result.InstallStarted || result.Installed {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDownloadAssetPreservesWindowsExecutableSuffix(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	content := []byte("windows executable")
	digest := sha256.Sum256(content)
	selected := asset{
		URL:    "https://api.github.test/assets/windows",
		Name:   "package-checker_2.1.0_windows_amd64.exe",
		State:  "uploaded",
		Size:   int64(len(content)),
		Digest: "sha256:" + hex.EncodeToString(digest[:]),
	}
	updater.Client = &fakeHTTPClient{responses: map[string]fakeHTTPResponse{
		selected.URL: {status: http.StatusOK, body: content},
	}}

	path, err := updater.downloadAsset(context.Background(), selected)
	if err != nil {
		t.Fatalf("downloadAsset: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if filepath.Ext(path) != ".exe" {
		t.Fatalf("downloaded executable path = %q, want .exe suffix", path)
	}
}

func TestPortableUpdateReplacesExecutable(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	oldContents := []byte("old executable")
	if err := os.WriteFile(updater.ExecutablePath, oldContents, 0o600); err != nil {
		t.Fatalf("write current executable: %v", err)
	}
	archive := buildArchive(t, binaryName, []byte("new executable"))
	latest := portableRelease(t, updater, "2.1.0", archive)
	updater.Client = releaseClient(t, updater, latest, archive)
	updater.Runner = &fakeRunner{output: func(name string, args ...string) (string, error) {
		if len(args) == 1 && args[0] == "--version" {
			return "package-checker version 2.1.0 (commit release, built now)\n", nil
		}
		return "", errors.New("not package managed")
	}}

	result, err := updater.CheckAndInstall(context.Background(), true)
	if err != nil {
		t.Fatalf("CheckAndInstall: %v", err)
	}
	if !result.Installed {
		t.Fatalf("expected installed result: %+v", result)
	}
	contents, err := os.ReadFile(updater.ExecutablePath)
	if err != nil {
		t.Fatalf("read updated executable: %v", err)
	}
	if string(contents) != "new executable" {
		t.Fatalf("updated executable = %q", contents)
	}
	stateData, err := os.ReadFile(updater.statePath())
	if err != nil {
		t.Fatalf("read update state: %v", err)
	}
	if !bytes.Contains(stateData, []byte(`"current_version":"2.1.0"`)) {
		t.Fatalf("update state does not record installed version: %s", stateData)
	}
}

func TestPortableUpdateRollsBackFailedVerification(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	oldContents := []byte("old executable")
	if err := os.WriteFile(updater.ExecutablePath, oldContents, 0o600); err != nil {
		t.Fatalf("write current executable: %v", err)
	}
	archive := buildArchive(t, binaryName, []byte("new executable"))
	latest := portableRelease(t, updater, "2.1.0", archive)
	updater.Client = releaseClient(t, updater, latest, archive)
	updater.Runner = &fakeRunner{output: func(name string, args ...string) (string, error) {
		if len(args) != 1 || args[0] != "--version" {
			return "", errors.New("not package managed")
		}
		if strings.Contains(name, "candidate") {
			return "package-checker version 2.1.0 (commit release, built now)\n", nil
		}
		return "package-checker version 9.9.9 (commit wrong, built now)\n", nil
	}}

	if _, err := updater.CheckAndInstall(context.Background(), true); err == nil || !strings.Contains(err.Error(), "verify installed update") {
		t.Fatalf("CheckAndInstall error = %v", err)
	}
	contents, err := os.ReadFile(updater.ExecutablePath)
	if err != nil {
		t.Fatalf("read restored executable: %v", err)
	}
	if !bytes.Equal(contents, oldContents) {
		t.Fatalf("restored executable = %q, want %q", contents, oldContents)
	}
	if _, err := os.Stat(updater.backupPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup remains after rollback: %v", err)
	}
}

func TestSwapStagedExecutableKeepsTargetWhenInstallFails(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	target := updater.ExecutablePath
	backup := updater.backupPath()
	if err := os.WriteFile(target, []byte("current executable"), 0o600); err != nil {
		t.Fatalf("write current executable: %v", err)
	}

	err := updater.swapStagedExecutable(filepath.Join(t.TempDir(), "missing-stage"), target, backup)
	if err == nil {
		t.Fatal("expected missing staged executable to fail")
	}
	// target is fixed beneath the test's private temporary directory.
	contents, readErr := os.ReadFile(target) //nolint:gosec
	if readErr != nil {
		t.Fatalf("current executable disappeared after failed swap: %v", readErr)
	}
	if string(contents) != "current executable" {
		t.Fatalf("current executable changed after failed swap: %q", contents)
	}
	if _, statErr := os.Stat(backup); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed swap left backup behind: %v", statErr)
	}
}

func TestExtractPortableBinaryRejectsUnsafePath(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	archivePath := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := os.WriteFile(archivePath, buildArchive(t, "../"+binaryName, []byte("binary")), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if _, err := updater.extractPortableBinary(archivePath); err == nil {
		t.Fatal("expected archive with traversal path to be rejected")
	}
}

func TestDetectInstallKindAndSudoPackageInstall(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	runner := &fakeRunner{output: func(name string, args ...string) (string, error) {
		switch name {
		case "dpkg-query":
			return "", errors.New("not a deb")
		case "rpm":
			return "package-checker-2.0.0", nil
		case updater.ExecutablePath:
			return "package-checker version 2.1.0 (commit release, built now)\n", nil
		default:
			return "", fmt.Errorf("unexpected command %q", name)
		}
	}}
	updater.Runner = runner
	updater.EUID = 1000
	if got := updater.detectInstallKind(context.Background()); got != installRPM {
		t.Fatalf("detectInstallKind = %q, want %q", got, installRPM)
	}
	if err := updater.installPackage(context.Background(), "2.1.0", "rpm", "-Uvh", "/tmp/update.rpm"); err != nil {
		t.Fatalf("installPackage: %v", err)
	}
	if len(runner.runs) != 1 || strings.Join(runner.runs[0], " ") != "sudo rpm -Uvh /tmp/update.rpm" {
		t.Fatalf("run commands = %#v", runner.runs)
	}
}

func TestDevelopmentBuildNeverChecksGitHub(t *testing.T) {
	t.Parallel()

	updater := testUpdater(t)
	updater.Commit = "none"
	client := &fakeHTTPClient{}
	updater.Client = client
	result, err := updater.CheckAndInstall(context.Background(), false)
	if err != nil {
		t.Fatalf("automatic development check: %v", err)
	}
	if result.SkippedReason != "development build" || len(client.requests) != 0 {
		t.Fatalf("unexpected result or requests: %+v, %d", result, len(client.requests))
	}
	if _, err := updater.CheckAndInstall(context.Background(), true); err == nil {
		t.Fatal("forced development update should fail")
	}
}

func testUpdater(t *testing.T) *Updater {
	t.Helper()
	root := t.TempDir()
	return &Updater{
		CurrentVersion: "2.0.0",
		Commit:         "abc123",
		BuildDate:      "2026-09-01T10:00:00Z",
		Owner:          defaultOwner,
		Repo:           defaultRepo,
		APIURL:         "https://api.github.test",
		Client:         &fakeHTTPClient{},
		Runner:         &fakeRunner{},
		GOOS:           "linux",
		GOARCH:         "amd64",
		EUID:           1000,
		ExecutablePath: filepath.Join(root, binaryName),
		CacheDir:       filepath.Join(root, "cache"),
		TempDir:        root,
		Now:            func() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) },
		CheckInterval:  defaultCheckInterval,
	}
}

func portableRelease(t *testing.T, updater *Updater, version string, content []byte) release {
	t.Helper()
	name, err := expectedAssetName(version, updater.GOOS, updater.GOARCH, installPortable)
	if err != nil {
		t.Fatalf("expected asset: %v", err)
	}
	digest := sha256.Sum256(content)
	return release{
		TagName: "v" + version,
		Assets: []asset{{
			URL:    "https://api.github.test/assets/1",
			Name:   name,
			State:  "uploaded",
			Size:   int64(len(content)),
			Digest: "sha256:" + hex.EncodeToString(digest[:]),
		}},
	}
}

func releaseClient(t *testing.T, updater *Updater, latest release, download []byte) *fakeHTTPClient {
	t.Helper()
	releaseBody, err := json.Marshal(latest)
	if err != nil {
		t.Fatalf("marshal release: %v", err)
	}
	endpoint := updater.APIURL + "/repos/" + updater.Owner + "/" + updater.Repo + "/releases/latest"
	responses := map[string]fakeHTTPResponse{
		endpoint: {status: http.StatusOK, body: releaseBody},
	}
	if download != nil {
		responses["https://api.github.test/assets/1"] = fakeHTTPResponse{status: http.StatusOK, body: download}
	}
	return &fakeHTTPClient{responses: responses}
}

func buildArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatalf("write tar contents: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buffer.Bytes()
}

type fakeHTTPResponse struct {
	status int
	body   []byte
}

type fakeHTTPClient struct {
	responses map[string]fakeHTTPResponse
	requests  []*http.Request
}

func (client *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	client.requests = append(client.requests, req)
	response, ok := client.responses[req.URL.String()]
	if !ok {
		return nil, fmt.Errorf("unexpected request %s", req.URL)
	}
	return &http.Response{
		StatusCode: response.status,
		Status:     fmt.Sprintf("%d %s", response.status, http.StatusText(response.status)),
		Body:       io.NopCloser(bytes.NewReader(response.body)),
		Request:    req,
	}, nil
}

type fakeRunner struct {
	output func(name string, args ...string) (string, error)
	runErr error
	runs   [][]string
}

func (runner *fakeRunner) Output(_ context.Context, name string, args ...string) (string, error) {
	if runner.output == nil {
		return "", errors.New("command not found")
	}
	return runner.output(name, args...)
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	command := append([]string{name}, args...)
	runner.runs = append(runner.runs, command)
	return runner.runErr
}
