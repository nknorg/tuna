.DEFAULT_GOAL:=local_or_with_proxy

USE_PROXY=GOPROXY=https://goproxy.io
BUILD=go build -ldflags "-s -w"
BUILD_DIR=build
BIN_DIR=$(GOOS)-$(GOARCH)

.PHONY: entry
entry:
	$(BUILD) $(BUILD_PARAMS) bin/entry/main.go

.PHONY: exit
exit:
	$(BUILD) $(BUILD_PARAMS) bin/exit/main.go

.PHONY: local
local:
	${MAKE} entry BUILD_PARAMS="-o entry"
	${MAKE} exit BUILD_PARAMS="-o exit"

.PHONY: local_with_proxy
local_with_proxy:
	$(USE_PROXY) ${MAKE} entry BUILD_PARAMS="-o entry"
	$(USE_PROXY) ${MAKE} exit BUILD_PARAMS="-o exit"

.PHONY: local_or_with_proxy
local_or_with_proxy:
	${MAKE} local || ${MAKE} local_with_proxy

.PHONY: build
build:
	mkdir -p $(BUILD_DIR)/$(BIN_DIR)
	${MAKE} entry BUILD_PARAMS="-o $(BUILD_DIR)/$(BIN_DIR)/entry$(EXT)" GOOS=$(GOOS) GOARCH=$(GOARCH)
	${MAKE} exit BUILD_PARAMS="-o $(BUILD_DIR)/$(BIN_DIR)/exit$(EXT)" GOOS=$(GOOS) GOARCH=$(GOARCH)
	cp config.entry.json config.exit.json services.json $(BUILD_DIR)/$(BIN_DIR)/
	${MAKE} zip

.PHONY: tar
tar:
	cd $(BUILD_DIR) && rm -f $(BIN_DIR).tar.gz && tar --exclude ".DS_Store" --exclude "__MACOSX" -czvf $(BIN_DIR).tar.gz $(BIN_DIR)

.PHONY: zip
zip:
	cd $(BUILD_DIR) && rm -f $(BIN_DIR).zip && zip --exclude "*.DS_Store*" --exclude "*__MACOSX*" -r $(BIN_DIR).zip $(BIN_DIR)

.PHONY: all
all:
	${MAKE} build GOOS=darwin GOARCH=amd64
	${MAKE} build GOOS=linux GOARCH=amd64
	${MAKE} build GOOS=windows GOARCH=amd64 EXT=.exe
