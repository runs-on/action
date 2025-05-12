# Copyright 2021 Blend Labs, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# From: https://github.com/blend/require-conditional-status-checks

.PHONY: help
help:
	@echo 'Makefile for Require Conditional Status Checks GitHub Action'
	@echo ''
	@echo 'Usage:'
	@echo '   make generate-index        Generate `index.js` file for current VERSION'
	@echo '   make main-linux-amd64      Build static binary for linux/amd64'
	@echo '   make main-linux-arm64      Build static binary for linux/arm64'
	@echo '   make main-windows-amd64    Build static binary for windows/amd64'
	@echo '   make release               Build all static binaries and `index.js`'
	@echo ''

################################################################################
# Meta-variables
################################################################################
VERSION ?= $(shell git log -1 --pretty=%H 2> /dev/null)
UPX_BIN := $(shell command -v upx 2> /dev/null)
COMMAND := "./cmd/action/"

.PHONY: generate-index
generate-index:
	rm -f index.js
	echo 'package main; import ("os"; "text/template"); func main() { tmpl, _ := template.ParseFiles("index.template.js"); tmpl.Execute(os.Stdout, map[string]string{"Version": "$(VERSION)"}) }' > temp.go && go run temp.go > index.js && rm temp.go

# NOTE: Targets to build Go binaries are marked `.PHONY` even though they
#       produce real files. We do this intentionally to defer to Go's build
#       caching and related tooling rather than relying on `make` for this.
#
#       For more on strategies to keep binaries small, see:
#       https://blog.filippo.io/shrink-your-go-binaries-with-this-one-weird-trick/

.PHONY: main-linux-amd64
main-linux-amd64: _require-upx _require-version
	rm -f "main-linux-amd64-*"
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -installsuffix static -o "main-linux-amd64-$(VERSION)" $(COMMAND)
	upx -q -9 "main-linux-amd64-$(VERSION)"

.PHONY: main-linux-arm64
main-linux-arm64: _require-upx _require-version
	rm -f "main-linux-arm64-*"
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -installsuffix static -o "main-linux-arm64-$(VERSION)" $(COMMAND)
	upx -q -9 "main-linux-arm64-$(VERSION)"

.PHONY: main-windows-amd64
main-windows-amd64: _require-upx _require-version
	rm -f "main-windows-amd64-*"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -installsuffix static -o "main-windows-amd64-$(VERSION)" $(COMMAND)
	upx -q -9 "main-windows-amd64-$(VERSION)"

.PHONY: release
release: main-linux-amd64 main-linux-arm64 main-windows-amd64 generate-index

################################################################################
# Doctor Commands (these do not show up in `make help`)
################################################################################

.PHONY: _require-upx
_require-upx:
ifndef UPX_BIN
	$(error 'upx is not installed, it can be installed via "apt-get install upx", "apk add upx" or "brew install upx".')
endif

.PHONY: _require-version
_require-version:
ifeq ($(VERSION),)
	$(error 'VERSION variable is not set.')
endif
ifndef VERSION
	$(error 'VERSION variable is not set.')
endif