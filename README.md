# package-checker

`package-checker` scans package manifests in a project directory against Socket supply-chain attack campaign packages.

Supported manifests:

- npm: `package.json`, `package-lock.json`
- Python/PyPI: `pyproject.toml`, `requirements.txt`
- Go modules: `go.mod`
- PHP/Composer: `composer.json`, `composer.lock`

When a project has `node_modules`, `package-checker` also scans installed npm package roots under `node_modules/<package>` and `node_modules/@scope/<package>`. It does not recursively walk nested dependency trees.

Release builds publish Linux packages (`.deb`, `.rpm`, Arch package artifact), macOS `.pkg` installers for Intel and Apple Silicon, and standalone Windows `.exe` artifacts.

CI also cross-compiles the release targets for Linux, macOS Intel, macOS Apple Silicon, and Windows to catch portability regressions before release tags are cut.

Build all release archives and native packages locally without publishing them:

```bash
make snapshot
```

The snapshot target uses the version reported by the program for package metadata without creating a Git tag. Published releases continue to derive their version from the release tag.

## Usage

```bash
package-checker --dir /path/to/project
```

Override the cache file location when needed:

```bash
package-checker --dir /path/to/project --cache-file /tmp/package-checker-feed.json
```

Install the latest stable GitHub release and exit:

```bash
package-checker --self-update
```

Release builds check for updates at most once every 24 hours before scanning. Use `--no-self-update` to skip that check for one invocation. Development builds created without release commit and build-date metadata never check for or install updates.

## Self-update

The updater reads the latest stable release from `L1ghtn1ng/package-checker`, selects an exact artifact for the current operating system and architecture, and verifies its GitHub-provided SHA-256 digest and size before installation. Supported release targets are Linux, macOS, and Windows on `amd64` and `arm64`.

Package-managed installations remain package-managed: Debian, RPM, Arch Linux, and macOS package installations use their native package or installer command, requesting `sudo` when required. Standalone Linux and macOS archives are extracted to a private temporary file and replace the resolved executable atomically with rollback on verification failure. Windows executable updates use a side-by-side backup because a running executable can remain locked; any backup that cannot be removed immediately is cleaned up on the next invocation.

Both the candidate and installed binary must report the requested release version. Updates only move to a strictly greater stable version, and the running process is never restarted after installation—the new binary is used on the next invocation. These checks prevent development builds, stale releases, and a newly installed binary from entering an update loop.

## Notes

The default feed uses Socket's public supply-chain attack package API. The scanner starts at campaign `16`, tolerates sparse campaign IDs before the known `/25/packages` feed, and stops after three consecutive `404 Not Found` responses once that range is covered.

Socket campaign entries can include package versions. When an entry has a version, `package-checker` only reports a match when the manifest contains the same exact pinned version. Entries without a version match by package name.

Generally, this tool is intended to be used as a pre-release check to ensure that no known malicious packages are being used in your project.

As there are malicious packages in the feed, this tool should be used as part of a comprehensive security strategy and not relied upon as the sole method of package security.
It gives a quick indication of whether a package is malicious and can help identify potential security risks early in the development process.
and is a good 2nd line of defense against malicious packages, as there are loads of new packages being published every day that are getting flagged as malicious.
so its hard to keep up with all of them. This tool is a good way to keep up with the malicious packages as much as possible.

Linux release binaries are built as PIE executables with immediate binding enabled so they expose `GNU_RELRO`, `BIND_NOW`, and `PIE` in the ELF metadata.

## Dependencies
Requires Go 1.27.x to build from source

This tool only uses a 3rd party library to support toml based configuration files
github.com/pelletier/go-toml/v2 other than that its all golang standard library as golang does not have support for toml built in.
