BIN := bin/reviewd
PKG := ./...

.PHONY: build test lint fmt vet tidy clean install

build:
	go build -o $(BIN) ./cmd/reviewd

test:
	go test $(PKG)

vet:
	go vet $(PKG)

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

lint:
	@command -v golangci-lint >/dev/null 2>&1 \
		&& golangci-lint run \
		|| echo "golangci-lint not installed; skipping (make vet でも最低限は見られる)"

install: build
	install -m 0755 $(BIN) $(HOME)/.local/bin/reviewd

clean:
	rm -rf bin dist

pre-commit: fmt vet test
