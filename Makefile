VERSION ?= dev
LDFLAGS := -s -w -X github.com/hivepaas/hivepaas-cli/internal/version.Version=$(VERSION)

.PHONY: build test lint gen update-spec spec-check

build:
	go build -ldflags "$(LDFLAGS)" -o bin/hivepaas ./cmd/hivepaas

test:
	go test ./...

lint:
	golangci-lint run ./...

# The client, from the pinned spec.
gen:
	go tool oapi-codegen -config internal/api/cfg.yaml internal/api/openapi.json
	go run ./tools/speclevel internal/api/openapi.json internal/api/SPEC_REF internal/api/level_gen.go

# Pin another spec: REF=<hivepaas tag or commit>, or SPEC=<file> for a local one.
update-spec:
	./scripts/update-spec.sh
	$(MAKE) gen

# Generate the client from SPEC and build and test against it. hivepaas's CI runs
# it on every change to the server's API (its cli-compat job). It leaves the
# client generated from SPEC in the tree: `git checkout internal/api` undoes it.
spec-check:
	@test -n "$(SPEC)" || (echo "usage: make spec-check SPEC=<openapi.json>" && exit 2)
	cp "$(SPEC)" internal/api/openapi.json
	echo "spec-check" > internal/api/SPEC_REF
	$(MAKE) gen
	go build ./...
	go test ./...
