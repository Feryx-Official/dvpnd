# SPDX-License-Identifier: Apache-2.0
# Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.
.DEFAULT_GOAL := default
# The tag without its v, a pre-release suffix included, and past the tag the
# number of commits since it: v9.5.0-rc1 is 9.5.0-rc1, three commits after
# v9.4.4 is 9.4.4-3. --long always appends -N-g<hash>: that is cut, with N
# when it is 0.
VERSION := $(shell git describe --tags --match 'v*' --long | sed -E 's/^v//; s/-(0-)?g[0-9a-f]+$$//')
COMMIT := $(shell git log -1 --format='%H')

TAGS := $(strip netgo)
LD_FLAGS := -s -w \
	-X github.com/cosmos/cosmos-sdk/version.Name=dvpnd \
	-X github.com/cosmos/cosmos-sdk/version.AppName=dvpnd \
	-X github.com/cosmos/cosmos-sdk/version.Version=${VERSION} \
	-X github.com/cosmos/cosmos-sdk/version.Commit=${COMMIT} \
	-X github.com/cosmos/cosmos-sdk/version.BuildTags=${TAGS}

.PHONY: check-architecture
check-architecture:
	@bash scripts/check-architecture-doc.sh

# The regression gate (test/README.md): every change passes it before it is
# committed. The rule registry (every rule in docs/invariants/ pinned by a test
# or excused in invariants/status.json), formatting, vet (the integration
# tests included), every unit test with the race detector, SPDX headers, the
# licence rule and the provenance record. No root, no Docker; the provenance
# script asks a few public archives when it can reach them.
GO_FILES = $$(git ls-files --cached --others --exclude-standard '*.go')
.PHONY: check
check:
	@python3 invariants/registry.py
	@unformatted=$$(gofmt -l $(GO_FILES)); \
	  [ -z "$$unformatted" ] || { echo "gofmt: not formatted:"; echo "$$unformatted"; exit 1; }
	go vet ./...
	go vet -tags integration ./test/integration/
	go test -race -shuffle=on -count=1 ./...
	@missing=$$(grep -L 'SPDX-License-Identifier: Apache-2.0' $(GO_FILES)); \
	  [ -z "$$missing" ] || { echo "missing SPDX header:"; echo "$$missing"; exit 1; }
	@! grep -q 'sentinel-official/sentinel-go-sdk' go.mod go.sum || \
	  { echo "sentinel-go-sdk carries no licence and must not be a dependency"; exit 1; }
	@out=$$(bash docs/provenance/verify-fork.sh 2>&1) || { echo "$$out"; exit 1; }; echo "$$out" | tail -1

# The integration suite (test/README.md): every protocol with its
# real daemon and a real client, in Docker. Run it for any change to a service,
# the egress policy, the runtime, the Dockerfile, the runner or the unit.
.PHONY: check-integration
check-integration:
	@bash test/integration/run.sh

# The mutation canaries (test/README.md): each breaks one rule on purpose in
# a copy of the tree, and the rule's own test must fail.
.PHONY: check-canaries
check-canaries:
	@python3 invariants/canaries.py

# What every commit passes: the gate, then the canaries.
.PHONY: verify
verify: check check-canaries

.PHONY: check-all
check-all: verify check-integration

.PHONY: benchmark
benchmark:
	@go test -run '^$$' -bench . -mod=readonly ./...

.PHONY: build
build:
	go build -ldflags="${LD_FLAGS}" -mod=readonly -tags="${TAGS}" -trimpath \
		-o ./bin/dvpnd main.go

.PHONY: clean
clean:
	rm -rf ./bin ./vendor

.PHONY: default
default: clean build

# Into Go's bin directory, $GOBIN or the first GOPATH's bin, as dvpnd.
.PHONY: install
install:
	go install -ldflags="${LD_FLAGS}" -mod=readonly -tags="${TAGS}" -trimpath .

.PHONY: build-image
build-image:
	@docker build --compress --file Dockerfile --force-rm --tag dvpnd .

# What CI's golangci-lint job runs; it reports, and changes no file.
.PHONY: go-lint
go-lint:
	@golangci-lint run

.PHONY: test
test:
	@go test -cover -mod=readonly -v ./...

# The golangci-lint CI runs (.github/workflows/ci.yml).
.PHONY: tools
tools:
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
