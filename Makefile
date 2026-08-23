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
	test -z "$$(gofmt -l .)"
	go vet ./...

generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 --config api/oapi-codegen.yaml api/openapi.yaml

migrate-up:
	go run ./cmd/api migrate

compose-up:
	docker compose up --build

compose-down:
	docker compose down
