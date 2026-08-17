PLUGIN_NAME := cpa-route-allocator
GOOS ?= linux
GOARCH ?= amd64
EXT := .so

ifeq ($(GOOS),darwin)
EXT := .dylib
endif
ifeq ($(GOOS),windows)
EXT := .dll
endif

.PHONY: build test clean

build:
	mkdir -p dist
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildmode=c-shared -trimpath -o dist/$(PLUGIN_NAME)$(EXT) .

test:
	go test ./...

clean:
	rm -f dist/$(PLUGIN_NAME).so dist/$(PLUGIN_NAME).dylib dist/$(PLUGIN_NAME).dll dist/$(PLUGIN_NAME).h
