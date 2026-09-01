package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxExtractedBinarySize = 128 << 20

func (updater *Updater) detectInstallKind(ctx context.Context) installKind {
	switch updater.GOOS {
	case "windows":
		return installWindows
	case "darwin":
		if filepath.Clean(updater.ExecutablePath) == macOSPackagePath {
			if _, err := updater.Runner.Output(ctx, "/usr/sbin/pkgutil", "--pkg-info", macOSPackageID); err == nil {
				return installMacPKG
			}
		}
		return installPortable
	case "linux":
		queries := []struct {
			kind installKind
			name string
			args []string
		}{
			{kind: installDeb, name: "dpkg-query", args: []string{"-S", updater.ExecutablePath}},
			{kind: installRPM, name: "rpm", args: []string{"-qf", updater.ExecutablePath}},
			{kind: installArch, name: "pacman", args: []string{"-Qo", updater.ExecutablePath}},
		}
		for _, query := range queries {
			if _, err := updater.Runner.Output(ctx, query.name, query.args...); err == nil {
				return query.kind
			}
		}
		return installPortable
	default:
		return installPortable
	}
}

func selectAsset(assets []asset, version, goos, goarch string, kind installKind) (asset, error) {
	want, err := expectedAssetName(version, goos, goarch, kind)
	if err != nil {
		return asset{}, err
	}
	for _, candidate := range assets {
		if candidate.Name == want {
			return candidate, nil
		}
	}
	return asset{}, fmt.Errorf("release does not contain required asset %q", want)
}

func expectedAssetName(version, goos, goarch string, kind installKind) (string, error) {
	if goarch != "amd64" && goarch != "arm64" {
		return "", fmt.Errorf("self-update does not support architecture %q", goarch)
	}
	switch kind {
	case installPortable:
		if goos != "linux" && goos != "darwin" {
			return "", fmt.Errorf("portable self-update does not support OS %q", goos)
		}
		return fmt.Sprintf("%s_%s_%s_%s.tar.gz", binaryName, version, goos, goarch), nil
	case installWindows:
		if goos != "windows" {
			return "", fmt.Errorf("windows executable selected for OS %q", goos)
		}
		return fmt.Sprintf("%s_%s_windows_%s.exe", binaryName, version, goarch), nil
	case installDeb:
		return fmt.Sprintf("%s_%s_%s.deb", binaryName, version, goarch), nil
	case installRPM:
		return fmt.Sprintf("%s-%s-1.%s.rpm", binaryName, version, packageArch(goarch)), nil
	case installArch:
		return fmt.Sprintf("%s-%s-1-%s.pkg.tar.zst", binaryName, version, packageArch(goarch)), nil
	case installMacPKG:
		return fmt.Sprintf("%s_v%s_darwin_%s.pkg", binaryName, version, goarch), nil
	default:
		return "", fmt.Errorf("unsupported installation kind %q", kind)
	}
}

func packageArch(goarch string) string {
	if goarch == "amd64" {
		return "x86_64"
	}
	return "aarch64"
}

func (updater *Updater) install(ctx context.Context, downloadedPath, assetName, version string, kind installKind) error {
	switch kind {
	case installDeb:
		return updater.installPackage(ctx, version, "dpkg", "-i", downloadedPath)
	case installRPM:
		return updater.installPackage(ctx, version, "rpm", "-Uvh", downloadedPath)
	case installArch:
		return updater.installPackage(ctx, version, "pacman", "-U", "--noconfirm", downloadedPath)
	case installMacPKG:
		return updater.installPackage(ctx, version, "/usr/sbin/installer", "-pkg", downloadedPath, "-target", "/")
	case installPortable:
		candidatePath, err := updater.extractPortableBinary(downloadedPath)
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(candidatePath) }()
		return updater.replacePortable(ctx, candidatePath, version)
	case installWindows:
		if !strings.HasSuffix(strings.ToLower(assetName), ".exe") {
			return fmt.Errorf("windows update asset %q is not an executable", assetName)
		}
		return updater.replacePortable(ctx, downloadedPath, version)
	default:
		return fmt.Errorf("unsupported installation kind %q", kind)
	}
}

func (updater *Updater) installPackage(ctx context.Context, version, command string, args ...string) error {
	name := command
	commandArgs := args
	if updater.EUID != 0 {
		name = "sudo"
		commandArgs = append([]string{command}, args...)
	}
	if err := updater.Runner.Run(ctx, name, commandArgs...); err != nil {
		return err
	}
	return updater.verifyBinaryVersion(ctx, updater.ExecutablePath, version)
}

func (updater *Updater) extractPortableBinary(archivePath string) (string, error) {
	// archivePath is a private temporary file created by the updater.
	archive, err := os.Open(archivePath) //nolint:gosec
	if err != nil {
		return "", err
	}
	defer func() { _ = archive.Close() }()
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return "", err
	}
	defer func() { _ = gzipReader.Close() }()

	tmp, err := os.CreateTemp(updater.TempDir, binaryName+"-candidate-*")
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

	found := false
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if filepath.Clean(header.Name) != binaryName {
			continue
		}
		if found || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > maxExtractedBinarySize {
			return "", errors.New("release archive contains an invalid package-checker binary")
		}
		written, err := io.CopyN(tmp, tarReader, header.Size)
		if err != nil || written != header.Size {
			return "", errors.New("release archive contains a truncated package-checker binary")
		}
		found = true
	}
	if !found {
		return "", errors.New("release archive does not contain package-checker")
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// The extracted file is an executable release binary.
	if err := os.Chmod(tmpPath, 0o755); err != nil { //nolint:gosec
		return "", err
	}
	keep = true
	return tmpPath, nil
}

func (updater *Updater) replacePortable(ctx context.Context, candidatePath, version string) error {
	if err := updater.verifyBinaryVersion(ctx, candidatePath, version); err != nil {
		return fmt.Errorf("verify update candidate: %w", err)
	}
	target := filepath.Clean(updater.ExecutablePath)
	targetDir := filepath.Dir(target)
	staged, err := os.CreateTemp(targetDir, ".package-checker-update-*")
	if err != nil {
		return fmt.Errorf("stage update beside %s: %w", target, err)
	}
	stagedPath := staged.Name()
	defer func() { _ = os.Remove(stagedPath) }()

	// candidatePath is a verified private temporary file.
	candidate, err := os.Open(candidatePath) //nolint:gosec
	if err != nil {
		_ = staged.Close()
		return err
	}
	_, copyErr := io.Copy(staged, candidate)
	closeCandidateErr := candidate.Close()
	if copyErr != nil {
		_ = staged.Close()
		return copyErr
	}
	if closeCandidateErr != nil {
		_ = staged.Close()
		return closeCandidateErr
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	// The staged file is the verified executable update candidate.
	if err := os.Chmod(stagedPath, 0o755); err != nil { //nolint:gosec
		return err
	}

	backup := updater.backupPath()
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove previous update backup: %w", err)
	}
	if err := updater.swapStagedExecutable(stagedPath, target, backup); err != nil {
		return err
	}
	if err := updater.verifyBinaryVersion(ctx, target, version); err != nil {
		if restoreErr := updater.restorePortableBackup(target, backup); restoreErr != nil {
			return errors.Join(fmt.Errorf("verify installed update: %w", err), fmt.Errorf("restore previous executable: %w", restoreErr))
		}
		return fmt.Errorf("verify installed update: %w", err)
	}
	if err := os.Remove(backup); err != nil && updater.GOOS != "windows" {
		return fmt.Errorf("remove update backup: %w", err)
	}
	return nil
}

func (updater *Updater) swapStagedExecutable(stagedPath, target, backup string) error {
	if updater.GOOS == "windows" {
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("back up current executable: %w", err)
		}
		if err := os.Rename(stagedPath, target); err != nil {
			_ = os.Rename(backup, target)
			return fmt.Errorf("install updated executable: %w", err)
		}
		return nil
	}

	// A hard link preserves the old executable for rollback without removing
	// the live path. Renaming the staged file over target is atomic on Unix.
	if err := os.Link(target, backup); err != nil {
		return fmt.Errorf("back up current executable: %w", err)
	}
	if err := os.Rename(stagedPath, target); err != nil {
		_ = os.Remove(backup)
		return fmt.Errorf("install updated executable: %w", err)
	}
	return nil
}

func (updater *Updater) restorePortableBackup(target, backup string) error {
	if updater.GOOS == "windows" {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(backup, target)
}

func (updater *Updater) verifyBinaryVersion(ctx context.Context, path, wantVersion string) error {
	output, err := updater.Runner.Output(ctx, path, "--version")
	if err != nil {
		return err
	}
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) < 3 || fields[0] != binaryName || fields[1] != "version" {
		return fmt.Errorf("unexpected version output %q", strings.TrimSpace(output))
	}
	gotVersion, err := normalizeStableVersion(fields[2])
	if err != nil {
		return fmt.Errorf("invalid reported version %q: %w", fields[2], err)
	}
	if gotVersion != wantVersion {
		return fmt.Errorf("binary reports version %s, want %s", gotVersion, wantVersion)
	}
	return nil
}

func (updater *Updater) backupPath() string {
	return filepath.Clean(updater.ExecutablePath) + ".update-backup"
}

func (updater *Updater) cleanupBackup() error {
	err := os.Remove(updater.backupPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
