PLUGIN_NAME := cpa-route-allocator
PLUGIN_VERSION ?= 0.1.0-dev

.PHONY: test race vet build package clean

test:
	GOTOOLCHAIN=auto go test ./...

race:
	GOTOOLCHAIN=auto go test -race ./...

vet:
	GOTOOLCHAIN=auto go vet ./...

build:
	mkdir -p dist
	CGO_ENABLED=1 GOTOOLCHAIN=auto go build -buildmode=c-shared -trimpath \
		-ldflags="-s -w -X github.com/Zesuy/Plugin-Gpt-Allocator/internal/app.PluginVersion=$(PLUGIN_VERSION)" \
		-o dist/$(PLUGIN_NAME).so .

package:
	VERSION=$(PLUGIN_VERSION) ./scripts/package.sh

clean:
	rm -f dist/$(PLUGIN_NAME).so dist/$(PLUGIN_NAME).dylib dist/$(PLUGIN_NAME).dll dist/$(PLUGIN_NAME).h
