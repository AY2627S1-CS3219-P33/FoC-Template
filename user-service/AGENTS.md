# User service

- Keep changes within `user-service/` unless explicitly authorized otherwise.
- Follow the ownership boundaries documented in `README.md`; never query another service's database directly.
- Requester and courier are activities of a normal USER account, not separate account roles.
- This is an internal PostgreSQL CRUD foundation. Implement additional workflows only when explicitly requested; do not add fake authentication, database behavior, or password handling. Keep business deletion blocked until coordinated eligibility checks and session revocation exist.
- Keep HTTP concerns in `internal/handler`, business logic in `internal/service`, and persistence contracts in `internal/repository`.
- Format Go files with `gofmt`, then run `go vet ./...`, `go test ./...`, and `go build ./...` from this directory. For persistence changes, also run integration tests with `TEST_DATABASE_URL` pointing to a dedicated test database; tests use isolated schemas.
