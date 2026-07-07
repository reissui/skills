# Packaging

Release builds are produced by GoReleaser from `.goreleaser.yaml`.

```sh
goreleaser check
goreleaser release --clean
```

The release archives contain `clex`, `clexd`, README material, and service unit
templates for launchd/systemd. GoReleaser publishes a Homebrew cask into the tap
because formulas are deprecated in GoReleaser 2.10+. A formula template is kept
in `packaging/homebrew/clex.rb.tmpl` for tap maintainers who want a formula.
`install.sh` downloads one archive, verifies it against `checksums.txt`, and
installs both binaries.

Run the installer fixture test locally with:

```sh
sh packaging/test-install.sh
```

The fixture uses a `file://` release directory and verifies that checksum
validation is exercised without network access.
