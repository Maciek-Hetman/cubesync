.PHONY: build run test test-integration lint generate migrate-up compose-up compose-down

build:
	go build ./cmd/api

run:
	go run ./cmd/api serve

test:
	go test ./...

test-integration:
	go test -tags=integration ./...

lint:
	gofmt -w $$(git ls-files '*.go')
	go vet ./...

generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest generate
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest --config api/oapi-codegen.yaml api/openapi.yaml

migrate-up:
	go run ./cmd/api migrate

compose-up:
	docker compose up --build

compose-down:
	docker compose down
