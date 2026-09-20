# 局域网文件传输 —— 构建与部署入口
#
# 三个产物分别给谁用：
#   bin/lanfile-server        make build          本机开发用（动态链接，只在编译它的机器上跑）
#   bin/lanfile-server-linux  make build-linux    云服务器部署用（静态链接，任何 x86_64 Linux 都能跑）
#   dist/lanfile-server.exe   make build-windows  Windows 单文件版（拷过去双击即运行）
#
# 前端源码在 src/web/，编译时由 webassets.go 嵌入二进制，改了前端必须重新编译才生效。

BINARY := bin/lanfile-server
WINDOWS_BINARY := dist/lanfile-server.exe
LINUX_BINARY := bin/lanfile-server-linux

.PHONY: run build build-windows build-linux deploy vet clean

# 本地开发：直接跑源码，改前端刷新浏览器即可生效（优先读磁盘上的 src/web）
run:
	go run ./src/server

# 本机运行版：默认动态链接、依赖本机 glibc，只在本机用，别拷到服务器（会报 GLIBC_x.x not found）
build:
	go build -o $(BINARY) ./src/server

# Windows 单文件版：交叉编译并关闭 CGO，产物不依赖任何运行库
build-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(WINDOWS_BINARY) ./src/server

# 服务器部署版：CGO 关闭 = 静态链接，不依赖目标机 glibc；-trimpath/-s/-w 去掉路径与调试信息
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(LINUX_BINARY) ./src/server

# 编译并部署到服务器：等价于 build-linux + scripts/deploy.sh
# 目标主机默认取 ssh 别名 myecs，可用 LANFILE_DEPLOY_HOST=别名 覆盖
deploy: build-linux
	scripts/deploy.sh

# 静态检查
vet:
	go vet ./...

# 清理构建产物（三个都清；src/、data/ 不动）
clean:
	rm -f $(BINARY) $(LINUX_BINARY) $(WINDOWS_BINARY)
