# How to Contribute

Contributions and pull requests are welcome.

## Code Reviews

All submissions require review via GitHub pull requests. See
[GitHub Help](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/proposing-changes-to-your-work-with-pull-requests/about-pull-requests)
for more information on using pull requests.

## Testing

Before opening a pull request, run the test suite with the race detector:

```bash
go test -race ./...
go vet ./...
```

## Releasing

Follow these steps to cut a new release `vX.Y.Z`:

1. **Bump version numbers and changelog:**
   - `internal/version/version.go`: update `DefaultVersion` to `"X.Y.Z"`.
   - `skills/kungfu/SKILL.md`: update `metadata.version` and the `CLI Version Check` lines to `X.Y.Z`.
   - `skills/kungfu/evals/evals.json`: update `"version"` to `"X.Y.Z"`.
   - `CHANGELOG.md`: add a `## vX.Y.Z - YYYY-MM-DD` section summarizing changes (and upgrade steps for breaking changes).
2. **Update the `SKILL.md` SHA-256 digest** in `.well-known/agent-skills/index.json` after editing `skills/kungfu/SKILL.md`:
   ```bash
   shasum -a 256 skills/kungfu/SKILL.md
   ```
   Copy the exact `sha256:<hash>` into the `"digest"` field of `.well-known/agent-skills/index.json`.
3. **Run pre-release checks:**
   ```bash
   test -z "$(gofmt -l .)"
   go mod tidy -diff
   go vet ./...
   modernize ./...
   golangci-lint run ./...
   go test -race ./...
   GOOS=windows GOARCH=amd64 go build ./...
   GOOS=linux GOARCH=amd64 go build ./...
   GOOS=darwin GOARCH=arm64 go build ./...
   ```
   Confirm `go run . --version` prints `kungfu version X.Y.Z` and the digest in `.well-known/agent-skills/index.json` matches `shasum -a 256 skills/kungfu/SKILL.md`.
4. **Commit, tag, and push:**
   ```bash
   git add -A
   git commit -m "release: vX.Y.Z <short summary>"
   git tag -a vX.Y.Z -m "Release vX.Y.Z"
   git push origin main
   git push origin vX.Y.Z
   ```
5. **Publish the GitHub Release** using the notes from `CHANGELOG.md`:
   ```bash
   NOTES=$(awk '/^## vX\.Y\.Z/{flag=1; next} /^## /{flag=0} flag' CHANGELOG.md)
   gh release create vX.Y.Z --title "vX.Y.Z" --notes "${NOTES}"
   ```

## Disclaimer

This is not an officially supported Google product.
