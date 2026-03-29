# package-checker

`package-checker` scans `package.json`, `pyproject.toml`, and `requirements.txt` in a project directory against the JFrog malicious package feed.

Release builds publish Linux packages (`.deb`, `.rpm`, Arch package artifact), a macOS Apple Silicon `.pkg`, and standalone Windows `.exe` artifacts.

CI also cross-compiles the release targets for Linux, macOS Apple Silicon, and Windows to catch portability regressions before release tags are cut.

## Usage

```bash
package-checker --dir /path/to/project
```

Override the cache file location when needed:

```bash
package-checker --dir /path/to/project --cache-file /tmp/package-checker-feed.json
```
