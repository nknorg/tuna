.DEFAULT_GOAL:=local_or_with_proxy

USE_PROXY=GOPROXY=https://goproxy.io
VERSION:=$(shell git describe --abbrev=7 --dirty --always --tags)
BUILD=go build -ldflags "-s -w -X main.Version=$(VERSION)"
BUILD_DIR=build
BIN_DIR=$(GOOS)-$(GOARCH)

.PHONY: tuna
tuna:
	$(BUILD) $(BUILD_PARAMS) ./cmd

.PHONY: local
local:
	${MAKE} tuna BUILD_PARAMS="-o tuna"

.PHONY: local_with_proxy
local_with_proxy:
	$(USE_PROXY) ${MAKE} tuna BUILD_PARAMS="-o tuna"

.PHONY: local_or_with_proxy
local_or_with_proxy:
	${MAKE} local || ${MAKE} local_with_proxy

.PHONY: build
build:
	mkdir -p $(BUILD_DIR)/$(BIN_DIR)
	${MAKE} tuna BUILD_PARAMS="-o $(BUILD_DIR)/$(BIN_DIR)/tuna$(EXT)" GOOS=$(GOOS) GOARCH=$(GOARCH)
	cp config.entry.json.example $(BUILD_DIR)/$(BIN_DIR)/config.entry.json
	cp config.exit.json.example $(BUILD_DIR)/$(BIN_DIR)/config.exit.json
	cp services.json.example $(BUILD_DIR)/$(BIN_DIR)/services.json
	${MAKE} zip

.PHONY: tar
tar:
	cd $(BUILD_DIR) && rm -f $(BIN_DIR).tar.gz && tar --exclude ".DS_Store" --exclude "__MACOSX" -czvf $(BIN_DIR).tar.gz $(BIN_DIR)

.PHONY: zip
zip:
	cd $(BUILD_DIR) && rm -f $(BIN_DIR).zip && zip --exclude "*.DS_Store*" --exclude "*__MACOSX*" -r $(BIN_DIR).zip $(BIN_DIR)

.PHONY: all
all:
	${MAKE} build GOOS=linux GOARCH=amd64
	${MAKE} build GOOS=linux GOARCH=arm
	${MAKE} build GOOS=linux GOARCH=arm64
	${MAKE} build GOOS=darwin GOARCH=amd64
	${MAKE} build GOOS=windows GOARCH=amd64 EXT=.exe

.PHONY: pb
pb:
	protoc -I=. -I=$(GOPATH)/src -I=$(GOPATH)/src/github.com/gogo/protobuf/protobuf --gogoslick_out=. pb/*.proto
