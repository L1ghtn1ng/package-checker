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

## Usage

```bash
package-checker --dir /path/to/project
```

Override the cache file location when needed:

```bash
package-checker --dir /path/to/project --cache-file /tmp/package-checker-feed.json
```

## Notes

The default feed uses Socket's public supply-chain attack package API. The scanner starts at campaign `16`, tolerates sparse campaign IDs before the known `/25/packages` feed, and stops at the first `404 Not Found` after that range is covered.

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
