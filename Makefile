.PHONY: fmt check build verify
fmt:
	gofmt -w cmd internal
check:
	@test -z "$$(gofmt -l cmd internal)"
	go vet ./...
build:
	bash build.sh local
verify: check build
