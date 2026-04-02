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

## Notes
As this uses the JFrog feed, it does not give version information. This can lead to false positives if there are version(s)
that are malicious, but not the overall package. Generally, this tool is intended to be used as a pre-release check to
ensure that no known malicious packages are being used in your project. 

As there are malicious packages in the feed, this tool should be used as part of a comprehensive security strategy and not relied upon as the sole method of package security.
It gives a quick indication of whether a package is malicious and can help identify potential security risks early in the development process.
and is a good 2nd line of defense against malicious packages, as there are loads of new npm and pypi packages being published every day that are getting flagged as malicious.
so its hard to keep up with all of them. This tool is a good way to keep up with the malicious packages as much as possible.

## Dependencies
Requires Go 1.26 to build from source

This tool only uses a 3rd party library to support toml based configuration files
github.com/pelletier/go-toml/v2 other than that its all golang standard library as golang does not have support for toml built in.