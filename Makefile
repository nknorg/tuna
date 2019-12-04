.DEFAULT_GOAL:=all_or_with_proxy

USE_PROXY=GOPROXY=https://goproxy.io
BUILD_ENTRY=go build -ldflags "-s -w" -o entry bin/entry/main.go
BUILD_EXIT=go build -ldflags "-s -w" -o exit bin/exit/main.go

.PHONY: entry
entry:
	$(BUILD_ENTRY)

.PHONY: exit
exit:
	$(BUILD_EXIT)

.PHONY: all
all:
	$(BUILD_ENTRY)
	$(BUILD_EXIT)

.PHONY: all_with_proxy
all_with_proxy:
	$(USE_PROXY) $(BUILD_ENTRY)
	$(USE_PROXY) $(BUILD_EXIT)

.PHONY: all_or_with_proxy
all_or_with_proxy:
	${MAKE} all || ${MAKE} all_with_proxy
