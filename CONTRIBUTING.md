# Contributing

Thanks for taking a look! A few notes for contributors or reviewers:

- Run the server locally:

```powershell
# Using docker-compose
docker-compose up --build

# Or run directly (requires local Redis running on 6379)
go run .
```

- Keep changes small and focused. Add tests for new behavior.
- Branch naming: `feat/<short-desc>`, `fix/<short-desc>`, `chore/<short-desc>`
- Run `go vet` and `go test` before opening a PR.

If you want to submit a major change (match tracking, scale, TURN), open an issue first describing the approach.
