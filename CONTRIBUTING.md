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

## Disclaimer

This is not an officially supported Google product.
