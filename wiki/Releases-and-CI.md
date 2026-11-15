# Releases and CI

The repository builds, tests and publishes itself with GitHub Actions. The workflows are in `.github/workflows/`.

## Workflows

| Workflow | Runs on | What it does |
|----------|---------|--------------|
| `ci.yml` | Every push to `main` and every pull request | Checks formatting (`gofmt`), runs `go vet` and `go test` on Linux, Windows and macOS, then cross-compiles for all six targets |
| `release.yml` | Pushing a tag that starts with `v` | Tests, builds a binary for each platform, packages it, writes `SHA256SUMS.txt` and creates a GitHub release with generated notes |
| `docker.yml` | Pushes to `main`, `v*` tags and pull requests | Builds a multi-architecture image (`linux/amd64` and `linux/arm64`) and pushes it to GitHub Container Registry. Pull requests build but do not push |

## Platforms built

| OS | amd64 | arm64 |
|----|:-----:|:-----:|
| Linux | yes | yes |
| macOS | yes | yes |
| Windows | yes | yes |

All builds use `CGO_ENABLED=0`: the SQLite driver is pure Go, so one Linux runner cross-compiles everything and the binaries have no system library dependencies. Windows builds are `.zip`, the others `.tar.gz`; each contains the binary and the README.

## Making a release

```
git tag v1.0.0
git push origin v1.0.0
```

The Release workflow attaches the archives to a new release. The tag is compiled into the program, so `brokerbuddy -version` prints it. The Docker workflow tags the image `1.0.0`, `1.0` and `latest`.

## Docker image tags

| Tag | Meaning |
|-----|---------|
| `latest` | The newest release |
| `1.2.3`, `1.2` | A specific release, or the newest patch of a minor version |
| `edge` | The current `main` branch |

Images are at `ghcr.io/<owner>/brokerbuddy`. After the first push, open the package's settings on GitHub and set its visibility (a package created by a public repository can be made public so anyone can pull it without logging in). See [Installing and running](Installing-and-Running#docker).

## Permissions

The workflows ask for the least they need: `ci.yml` reads the repository, `release.yml` can write contents (to create the release) and `docker.yml` can write packages (to push the image). They use the built-in `GITHUB_TOKEN`; no personal tokens or secrets are needed.

## Running the same checks locally

```
gofmt -l .
go vet ./...
go test -count=1 ./...
docker build -t brokerbuddy .
```

## Keeping a public repository safe

Because the repository is public, never commit real data. The CI does not look at your database, key files or certificates because `.gitignore` and `.dockerignore` keep them out. See [repository hygiene](Development-and-Testing#repository-hygiene).
