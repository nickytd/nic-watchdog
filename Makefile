ROOT_DIR := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))
BINARY   := nic-watchdog
GOOS     ?= linux
GOARCH   ?= $(shell go env GOARCH)

.PHONY: fmt lint license check tidy build clean deploy

fmt:
	gofmt -s -w $(ROOT_DIR)
	goimports -w $(ROOT_DIR)

lint:
	go tool golangci-lint run $(ROOT_DIR)/...

license:
	go tool addlicense -s=only -c "nickytd" -l apache $(ROOT_DIR)/*.go

license-check:
	go tool addlicense -s=only -c "nickytd" -l apache -check $(ROOT_DIR)/*.go

check: fmt lint license
	go fix $(ROOT_DIR)/...

tidy:
	go mod tidy

build: check
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(ROOT_DIR)/bin/$(BINARY) $(ROOT_DIR)

clean:
	rm -rf $(ROOT_DIR)/bin/

deploy: build
	ansible-playbook $(ROOT_DIR)/deploy/playbook.yml
