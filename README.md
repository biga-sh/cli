# biga

Broadcast a local directory so others can watch it change in real time. There
are no accounts or sign-in: the session ID is the share link.

## Install

biga is distributed through [GitHub releases](https://github.com/biga-sh/cli/releases/latest).

**macOS, Linux, FreeBSD**

```sh
curl -fsSL https://raw.githubusercontent.com/biga-sh/cli/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/biga-sh/cli/main/install.ps1 | iex
```

The scripts pick the right build for your OS and CPU, check it against the
release's `checksums.txt`, and, if [cosign](https://docs.sigstore.dev/cosign/system_config/installation/)
is installed, check that file's signature too. They install to
`~/.local/bin` (Windows: `%LOCALAPPDATA%\Programs\biga`, added to your PATH).
Set `BIGA_VERSION` to install a specific version and `BIGA_INSTALL_DIR` to
choose the directory.

**Manually:** download the archive for your platform from the releases page,
unpack it, and put `biga` on your PATH. Builds are available for Linux (x64,
arm64, armv7), macOS (one universal binary), Windows (x64, arm64), and FreeBSD
(x64, arm64). Linux releases also include `.deb`, `.rpm`, `.apk`, and Arch
packages:

```sh
sudo apt install ./biga_1.0.0_amd64.deb
```

**With Go** (1.26 or newer):

```sh
go install github.com/biga-sh/cli/cmd/biga@latest
```

### Verifying a download

`checksums.txt` lists every file in a release and is signed with
[Sigstore](https://www.sigstore.dev) by the release workflow, so a valid
signature proves the files were built by this repository's CI from the tagged
commit. With [cosign](https://docs.sigstore.dev/cosign/system_config/installation/):

```sh
cosign verify-blob \
  --certificate-identity "https://github.com/biga-sh/cli/.github/workflows/release.yml@refs/tags/v1.0.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --check --ignore-missing checksums.txt
```

Or check a file's build provenance with the GitHub CLI:

```sh
gh attestation verify biga_1.0.0_linux_amd64.tar.gz --repo biga-sh/cli
```

Each archive also ships with an SBOM (`*.sbom.json`) listing its dependencies.

## Usage

```
biga start [dir...] [--dir <path>]... [--title <text>] [--session <id>] [--open]
biga status [dir] [--json]
biga list [--json]
biga open [dir]
biga stop [dir] [--all]
```

Run `biga` with no command in a terminal to pick one interactively, and
`biga <command> --help` for details.

`start` streams every saved change until you stop it. In a terminal it shows a
live panel: each project's link and status (live or reconnecting), how many
checkpoints, files and bytes it has sent, and a feed of changed files above it.
Press **c** to copy the link, **o** to open it in your browser, and **q** to
stop. Several projects start in parallel. When the output is not a terminal
(CI, a pipe, a log file) it prints plain lines instead.

`stop` asks a running broadcast, even one in another terminal, to send its
final changes and end its session. `open` opens a directory's link in the
browser. `list` and `status` take `--json` for scripts.

By default the CLI talks to `http://localhost:3000`. Point it at another
server with `--api-url` or `BIGA_API_URL`:

```sh
BIGA_API_URL=https://biga.example.com biga start
```

### What gets shared

Everything under the broadcast directory except what git would ignore, plus a
few things that should never leave your machine:

- paths matched by any `.gitignore`, at any depth, with git's precedence
  rules, plus `.git/info/exclude` and your global git excludes file
  (`core.excludesFile`)
- paths matched by any `.bigaignore`, which uses the same syntax for rules
  that only apply to broadcasting
- `node_modules/`, `.git/` and `.DS_Store`
- credentials: `.env` and `.env.*` (but not `.env.example`, `.env.sample` or
  `.env.template`), `*.pem`, `*.key`, `*.p12`, `*.pfx`, SSH keys (`id_rsa*`,
  `id_ed25519*`, …), `.npmrc`, `.pypirc`, `.netrc`, and the `.ssh/`, `.aws/`
  and `.gnupg/` directories
- symlinks and special files

To share one of the default exclusions anyway, re-include it in a
`.bigaignore` (for example `!.npmrc`). Editing an ignore file while
broadcasting takes effect immediately: newly ignored files disappear for
viewers, and newly included ones appear.

Build output directories (`dist`, `.next`, `build`, `out`, `coverage`,
`.build`) appear as empty folders; their contents are never read. Binary files
and files over 2 MB appear in the tree, but their contents are not uploaded.

### Sessions

`biga start` picks the session ID in this order:

1. `--session <id>`.
2. The ID saved for this directory, which **rejoins** the same share URL.
3. A new random 8-character ID, saved for next time.

On rejoin the CLI compares local files with the server's copy and uploads only
what changed. Local files are always the source of truth, so a crash, a server
restart or a dropped connection is fixed by running the same command again.
Changes made while the server is unreachable are buffered and sent when it
comes back.

### Workspaces

To broadcast several projects together, list them in a `biga.json` in the
directory you run `biga start` from:

```json
{
  "projects": [
    { "dir": "backend", "title": "API" },
    { "dir": "frontend" }
  ]
}
```

Each project gets its own session, linked on the server as one workspace. On
first use the CLI adds a generated `workspaceId` to the file so the link stays
stable; commit it along with the rest of the file. Directories passed on the
command line take precedence over `biga.json`.

### Transfer

Uploads are gzip-compressed when the server advertises support (an
`Accept-Encoding: gzip` response header, per RFC 7694), which typically cuts
source code to a third of its size; older servers get plain JSON. Large change
sets, like a branch switch, are split across several checkpoints so they stay
within the server's request limit. A heartbeat is skipped when a checkpoint
was just sent, since the server counts that as a sign of life too.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `BIGA_API_URL` | `http://localhost:3000` | Server base URL (`--api-url` overrides it) |
| `BIGA_HOME` | `~/.biga` | Where local state is kept |
| `BIGA_IDLE_MS` | `120` | Quiet time after the last change before a checkpoint is sent |
| `BIGA_MAX_WAIT_MS` | `1000` | Longest a burst of changes can delay a checkpoint |
| `BIGA_STABILITY_MS` | `75` | How long a file must stop changing before it is read |
| `BIGA_HEARTBEAT_MS` | `10000` | How often a broadcast tells the server it is live |
| `BIGA_NO_UPDATE_CHECK` | unset | Set to anything to skip the daily release check |

Raise the timing values on slow disks or filesystems that report partial
writes. Lowering them makes updates feel snappier.

### Local state

Everything lives in `~/.biga`:

- `sessions.json` maps each broadcast directory to its session. This is what
  keeps a project's share URL stable across restarts.
- `control/` holds stop requests from `biga stop`.
- `update-check.json` caches the latest release version for a day.

## Development

```sh
make test   # go test -race ./...
make lint   # golangci-lint run ./...
make build  # ./biga
```

CI runs the tests on Linux, macOS, and Windows, each on x64 and arm64, and
lints the code for each OS. It also runs `govulncheck`, lints and security-audits the
workflows (actionlint, zizmor), and does a full release dry run, whose builds
are attached to the run and installed with `install.sh` / `install.ps1` on
every one of those platforms.

### Releasing

1. Set the version in `internal/version/version.go` and merge it to `main`.
2. Tag the commit and push the tag:

   ```sh
   git tag -s v1.0.0 -m "biga 1.0.0"
   git push origin v1.0.0
   ```

The release workflow reruns CI, then GoReleaser builds every target, signs
`checksums.txt`, attests build provenance, and publishes the GitHub release.
Builds are reproducible: rebuilding a tag produces byte-identical files.

macOS binaries can also be signed and notarized, so Gatekeeper opens them
without a warning. Add these secrets to the `release` environment (Settings →
Environments) to turn that on; without them it is skipped:

| Secret | Value |
|--------|-------|
| `MACOS_SIGN_P12` | A *Developer ID Application* certificate exported as `.p12`, base64-encoded (`base64 -w0 < cert.p12`) |
| `MACOS_SIGN_PASSWORD` | The password set when exporting it |
| `MACOS_NOTARY_ISSUER_ID` | The issuer ID of an App Store Connect API key |
| `MACOS_NOTARY_KEY_ID` | That key's ID |
| `MACOS_NOTARY_KEY` | The key's `.p8` file, base64-encoded |

To bake a hosted server into release binaries, set the `DefaultAPIURL` ldflag
in `.goreleaser.yaml`. To try the whole release locally without publishing
anything, run `make snapshot`.

## License

[MIT](LICENSE)
