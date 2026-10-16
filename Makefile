.PHONY: build test vet race
TAGS = with_quic,with_utls,with_wireguard,with_gvisor,with_clash_api
build:
	go build -tags '$(TAGS)' -o dist/node-quality ./cmd/node-quality
test:
	go test -tags '$(TAGS)' ./...
vet:
	go vet -tags '$(TAGS)' ./...
race:
	go test -race -tags '$(TAGS)' ./...
