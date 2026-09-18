BINARY := bin/lanfile-server
WINDOWS_BINARY := dist/lanfile-server.exe

.PHONY: run build build-windows vet clean

run:
	go run ./src/server

build:
	go build -o $(BINARY) ./src/server

# 交叉编译 Windows 单文件版（前端已嵌入，拷到 Windows 双击即可运行）
build-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(WINDOWS_BINARY) ./src/server

vet:
	go vet ./...

clean:
	rm -f $(BINARY)
