default: build

.PHONY: build install lint fmt test testacc teste2e docs

build:
	go build -v ./...

install:
	go install -v ./...

lint:
	golangci-lint run

fmt:
	gofmt -s -w .

test:
	go test -count=1 -timeout=10m ./...

# Requires a reachable Docker daemon (DOCKER_HOST or the default socket).
testacc:
	TF_ACC=1 go test -count=1 -v -timeout=30m ./...

# Only the end-to-end test: builds the provider and runs the terraform CLI
# against it. Also part of testacc. Needs Docker and the terraform and docker CLIs.
teste2e:
	TF_ACC=1 go test -count=1 -v -timeout=30m ./e2e/...

docs:
	go generate ./...
