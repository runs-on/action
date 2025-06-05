.PHONY: help
help:
	@echo 'Usage:'
	@echo '   make generate-index        Generate `index.js` and `post.js` files'
	@echo '   make main-linux-amd64      Build static binary for linux/amd64'
	@echo '   make main-linux-arm64      Build static binary for linux/arm64'
	@echo '   make main-windows-amd64    Build static binary for windows/amd64'
	@echo '   make release               Build all static binaries + `index.js` and `post.js`'
	@echo ''

UPX_BIN := $(shell command -v upx 2> /dev/null)
COMMAND := "."

.PHONY: js
js:
	rm -f index.js post.js
	echo 'package main; import ("os"; "text/template"); func main() { tmpl, _ := template.ParseFiles("index.template.js"); tmpl.Execute(os.Stdout, map[string]string{"Args": ""}) }' > temp.go && go run temp.go > index.js && rm temp.go
	echo 'package main; import ("os"; "text/template"); func main() { tmpl, _ := template.ParseFiles("index.template.js"); tmpl.Execute(os.Stdout, map[string]string{"Args": "--post"}) }' > temp.go && go run temp.go > post.js && rm temp.go

.PHONY: main-linux-amd64
main-linux-amd64: _require-upx
	rm -f main-linux-amd64-*
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -installsuffix static -o "main-linux-amd64" $(COMMAND)
	upx -q -9 "main-linux-amd64"

.PHONY: main-linux-arm64
main-linux-arm64: _require-upx
	rm -f main-linux-arm64-*
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -installsuffix static -o "main-linux-arm64" $(COMMAND)
	upx -q -9 "main-linux-arm64"

.PHONY: main-windows-amd64
main-windows-amd64: _require-upx
	rm -f main-windows-amd64-*
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -installsuffix static -o "main-windows-amd64" $(COMMAND)
	upx -q -9 "main-windows-amd64"

.PHONY: release
release: main-linux-amd64 main-linux-arm64 main-windows-amd64 js

.PHONY: _require-upx
_require-upx:
ifndef UPX_BIN
	$(error 'upx is not installed, it can be installed via "apt-get install upx", "apk add upx" or "brew install upx".')
endif