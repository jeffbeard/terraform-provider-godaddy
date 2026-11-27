.PHONY: docs local
# ==================== [START] Global Variable Declaration =================== #
SHELL := /bin/bash
# 'shell' removes newlines
ARCH := $(shell go env GOARCH)

BASE_DIR := $(shell pwd)

COMMIT := $(shell git rev-parse --short HEAD)

OS := $(shell go env GOOS)

UNAME_S := $(shell uname -s)

VERSION := $(shell grep "version=" install.sh | cut -d= -f2)

BINARY := "terraform-provider-godaddy_v$(VERSION)"

# exports all variables
export
# ===================== [END] Global Variable Declaration ==================== #

docs:
	@go generate

local:
	go build -o $(BINARY) -ldflags='-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)' .
	rm -rf ~/.terraform/plugins/terraform-godaddy
	rm -rf ~/.terraform.d/plugins/registry.terraform.io/jeffbeard/godaddy/$(VERSION)/$(OS)_$(ARCH)
	mkdir -p ~/.terraform.d/plugins/registry.terraform.io/jeffbeard/godaddy/$(VERSION)/$(OS)_$(ARCH)/
	mv $(BINARY) ~/.terraform.d/plugins/registry.terraform.io/jeffbeard/godaddy/$(VERSION)/$(OS)_$(ARCH)/
	chmod +x ~/.terraform.d/plugins/registry.terraform.io/jeffbeard/godaddy/$(VERSION)/$(OS)_$(ARCH)/$(BINARY)
