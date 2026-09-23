# Releasing tswipoexp

Releases are cut by pushing a version tag. GitHub Actions
(`.github/workflows/release.yml`) then runs
[GoReleaser](https://goreleaser.com/) with the config in
`.goreleaser.yaml`, which cross-compiles the Windows binaries and
creates a draft GitHub Release with a zip of `tswipoexp.exe` and
`tspo.exe` plus a changelog generated from the commit log. The draft
is invisible to watchers until published.

Only Windows amd64 is built. The GUI needs cgo (Fyne), so the
workflow installs mingw-w64; a Windows arm64 build would need
llvm-mingw instead, which mingw-w64 doesn't provide.

## Cutting a release

1. Run the tag script, which creates an SSH-signed annotated tag
   after checking that the tag doesn't already exist on origin (a tag
   that exists only locally is replaced). It requires git's
   `user.signingkey` to be set to your SSH public key.

   ```sh
   ./tag.sh v0.1.0
   ```

2. Push the tag as the script instructs:

   ```sh
   git push origin v0.1.0
   ```

3. Watch the Release workflow in the Actions tab. When it finishes,
   a draft release with the zip and `checksums.txt` appears on the
   Releases page. Write the notes and publish.

## Checking the config locally

```sh
go run github.com/goreleaser/goreleaser/v2@latest check
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
```

The second command builds everything into `dist/` without a tag or a
GitHub token; it needs the same mingw-w64 compiler as the workflow.
