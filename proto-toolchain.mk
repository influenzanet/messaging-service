# Pinned protobuf toolchain for the api target.
#
# pkg/api must be generated with exactly these versions: the generated files record them in
# their headers, and the drift check (make api-check, run in CI) compares byte for byte.
# Nothing is taken from the PATH. The tools are fetched once into PROTO_TOOLS_DIR, in a
# directory named after their version, so changing a version below fetches the new one.
# PROTO_TOOLS_DIR is per platform: the same working tree may be used from macOS and from a
# Linux container, and a binary fetched by one cannot run on the other.
#
# This file is kept identical in messaging-service and user-management-service.

PROTOC_VERSION := 25.3
PROTOC_GEN_GO_VERSION := v1.34.1
PROTOC_GEN_GO_GRPC_VERSION := v1.3.0

# The Go toolchain that builds the two plugins. protoc-gen-go formats its output with the
# go/format package of the Go that compiled it, and that output changes between Go releases
# (Go 1.27 drops a "//" line inside the oneof comments that Go 1.26 writes). Building the
# plugins with one pinned Go keeps the generated code the same on every machine and in CI,
# whatever Go is installed. go install fetches this toolchain on its own (Go 1.21 or later).
PROTO_GO_TOOLCHAIN := go1.26.0

# SHA-256 of the protoc release archives of PROTOC_VERSION, computed from the GitHub release
# downloads on 2026-11-13. Update them together with PROTOC_VERSION. The Go plugins need no
# entry here: go install checks them against the Go checksum database.
PROTOC_SHA256_linux-x86_64 := f853e691868d0557425ea290bf7ba6384eef2fa9b04c323afab49a770ba9da80
PROTOC_SHA256_linux-aarch_64 := 9eae1f20f70cccc912d1c318c3929b86aebf5afd4b0f32c196ef682c222ed5ae
PROTOC_SHA256_osx-universal_binary := c06b6c5527c9b11a30a9c103400ee59eb6882f1766ea786fe4bdbb7f55ee5e6f

PROTO_HOST := $(shell uname -s)-$(shell uname -m)
PROTO_TOOLS_DIR ?= $(CURDIR)/.proto-tools/$(PROTO_HOST)

ifneq ($(filter Darwin-%,$(PROTO_HOST)),)
PROTOC_PLATFORM := osx-universal_binary
else ifeq ($(PROTO_HOST),Linux-x86_64)
PROTOC_PLATFORM := linux-x86_64
else ifneq ($(filter Linux-aarch64 Linux-arm64,$(PROTO_HOST)),)
PROTOC_PLATFORM := linux-aarch_64
endif

SHA256_CHECK := $(if $(shell command -v sha256sum),sha256sum -c -,shasum -a 256 -c -)

PROTOC_DIR := $(PROTO_TOOLS_DIR)/protoc-$(PROTOC_VERSION)
PROTOC := $(PROTOC_DIR)/bin/protoc
PROTOC_GEN_GO := $(PROTO_TOOLS_DIR)/protoc-gen-go-$(PROTOC_GEN_GO_VERSION)-$(PROTO_GO_TOOLCHAIN)/protoc-gen-go
PROTOC_GEN_GO_GRPC := $(PROTO_TOOLS_DIR)/protoc-gen-go-grpc-$(PROTOC_GEN_GO_GRPC_VERSION)-$(PROTO_GO_TOOLCHAIN)/protoc-gen-go-grpc
PROTO_TOOLS := $(PROTOC) $(PROTOC_GEN_GO) $(PROTOC_GEN_GO_GRPC)

# The well-known types (google/protobuf/empty.proto) come from the pinned release, not from
# whatever protobuf headers the machine has installed.
PROTOC_CMD = $(PROTOC) --proto_path=$(PROTOC_DIR)/include --plugin=protoc-gen-go=$(PROTOC_GEN_GO) --plugin=protoc-gen-go-grpc=$(PROTOC_GEN_GO_GRPC)

.PHONY: proto-tools
proto-tools: $(PROTO_TOOLS)

# The archive is checked before it is unpacked, and unpacked aside then moved into place, so
# that an interrupted or tampered download never leaves a protoc behind for the next run.
$(PROTOC):
	@test -n "$(PROTOC_PLATFORM)" || { echo "no pinned protoc release for $(PROTO_HOST)"; exit 1; }
	rm -rf $(PROTOC_DIR) $(PROTOC_DIR).zip $(PROTOC_DIR).tmp
	mkdir -p $(PROTO_TOOLS_DIR)
	curl -fsSL -o $(PROTOC_DIR).zip https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/protoc-$(PROTOC_VERSION)-$(PROTOC_PLATFORM).zip
	echo "$(PROTOC_SHA256_$(PROTOC_PLATFORM))  $(PROTOC_DIR).zip" | $(SHA256_CHECK)
	unzip -q $(PROTOC_DIR).zip -d $(PROTOC_DIR).tmp
	rm $(PROTOC_DIR).zip
	mv $(PROTOC_DIR).tmp $(PROTOC_DIR)

$(PROTOC_GEN_GO):
	GOTOOLCHAIN=$(PROTO_GO_TOOLCHAIN) GOBIN=$(dir $@) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)

$(PROTOC_GEN_GO_GRPC):
	GOTOOLCHAIN=$(PROTO_GO_TOOLCHAIN) GOBIN=$(dir $@) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
