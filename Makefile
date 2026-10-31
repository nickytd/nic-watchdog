ROOT_DIR := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))
BINARY   := nic-watchdog
GOOS     ?= linux
GOARCH   ?= $(shell go env GOARCH)

.PHONY: fmt lint license check test tidy build clean deploy

# Developer tools live in a separate module (tools/go.mod) so their large
# dependency tree stays out of the application module.
TOOL := go tool -modfile=$(ROOT_DIR)/tools/go.mod

fmt:
	gofmt -s -w $(ROOT_DIR)
	$(TOOL) goimports -w $(ROOT_DIR)

lint:
	$(TOOL) golangci-lint run $(ROOT_DIR)/...

license:
	$(TOOL) addlicense -s=only -c "nickytd" -l apache $(ROOT_DIR)/*.go

license-check:
	$(TOOL) addlicense -s=only -c "nickytd" -l apache -check $(ROOT_DIR)/*.go

test:
	$(TOOL) gotestsum -- -race $(ROOT_DIR)/...

check: fmt lint license
	go fix $(ROOT_DIR)/...

tidy:
	go mod tidy
	go -C $(ROOT_DIR)/tools mod tidy

build: check
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(ROOT_DIR)/bin/$(BINARY) $(ROOT_DIR)

clean:
	rm -rf $(ROOT_DIR)/bin/

deploy: build
	ansible-playbook $(ROOT_DIR)/deploy/playbook.yml
