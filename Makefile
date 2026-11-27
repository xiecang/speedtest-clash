.PHONY: build test vet race fmt-check verify
TAGS = with_quic,with_utls,with_wireguard,with_gvisor,with_clash_api
build:
	go build -tags '$(TAGS)' -o dist/node-quality ./cmd/node-quality
fmt-check:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "gofmt required:"; echo "$$unformatted"; exit 1; fi
verify: fmt-check vet
	go mod verify
	@for target in linux/amd64 linux/arm64 darwin/arm64 windows/amd64; do \
		GOOS=$${target%/*} GOARCH=$${target#*/} CGO_ENABLED=0 go build -tags '$(TAGS)' -trimpath -ldflags '-s -w' -o /dev/null ./cmd/node-quality || exit 1; \
	done
	@for f in LICENSE README.md THIRD_PARTY_NOTICES.md docs/V2.md docs/FILTERS.md internal/filter/UPSTREAM.md; do \
		test -f "$$f" || { echo "missing release file: $$f"; exit 1; }; \
	done
	@ls internal/filter/assets/*LICENSE.txt >/dev/null
test:
	go test -tags '$(TAGS)' ./...
vet:
	go vet -tags '$(TAGS)' ./...
race:
	go test -race -timeout 10m -tags '$(TAGS)' ./...
