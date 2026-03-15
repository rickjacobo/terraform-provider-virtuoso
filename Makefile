BINARY=terraform-provider-virtuoso
VERSION?=0.1.0
OS_ARCH=$(shell go env GOOS)_$(shell go env GOARCH)
PLUGIN_DIR=~/.terraform.d/plugins/registry.terraform.io/rickjacobo/virtuoso/$(VERSION)/$(OS_ARCH)

.PHONY: build install clean

build:
	CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY)

install: build
	mkdir -p $(PLUGIN_DIR)
	cp $(BINARY) $(PLUGIN_DIR)/

clean:
	rm -f $(BINARY)
