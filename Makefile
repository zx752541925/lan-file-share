# 局域网文件传输 —— 构建与部署入口
#
# 两个版本共用同一份代码（src/），差别只在启动参数与部署资产：
#   本地版（本机 WSL / Windows）→ deploy/local/，说明见 docs/local.md
#   服务器版（阿里云 ECS）      → deploy/server/，说明见 docs/server.md
#
# 前端源码在 src/web/（控制台页面在 src/web/local/），编译时由 webassets.go 嵌入二进制，
# 改了前端必须重新编译才生效。

BINARY := bin/lanfile-server
WINDOWS_BINARY := dist/lanfile-server.exe
LINUX_BINARY := bin/lanfile-server-linux
LAUNCHER_BINARY := dist/lanfile-start.exe

# ---------------- 本地版 ----------------
.PHONY: run build build-windows build-launcher

# 本地开发：直接跑源码，改前端刷新浏览器即可生效（优先读磁盘上的 src/web）
run:
	go run ./src/server

# 本机运行版：默认动态链接、依赖本机 glibc，只在本机用，别拷到服务器（会报 GLIBC_x.x not found）
build:
	go build -o $(BINARY) ./src/server

# Windows 单文件版：交叉编译并关闭 CGO，产物不依赖任何运行库
build-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(WINDOWS_BINARY) ./src/server

# Windows 双击启动器：唤醒 WSL → 起 shim 与服务 → 弹窗显示主机地址 → 打开浏览器
# -H=windowsgui 让 exe 不带控制台黑窗口（错误也用弹窗提示）
build-launcher:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H=windowsgui" -o $(LAUNCHER_BINARY) ./deploy/local/winlauncher

# ---------------- 服务器版 ----------------
.PHONY: build-linux deploy deploy-nginx

# 服务器部署版：CGO 关闭 = 静态链接，不依赖目标机 glibc；-trimpath/-s/-w 去掉路径与调试信息
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(LINUX_BINARY) ./src/server

# 编译并部署到服务器：等价于 build-linux + scripts/deploy-server.sh
# 目标主机默认取 ssh 别名 myecs，可用 LANFILE_DEPLOY_HOST=别名 覆盖
deploy: build-linux
	scripts/deploy-server.sh

# 只更新服务器上的 nginx 入口配置
deploy-nginx:
	scripts/deploy-nginx.sh

# ---------------- 通用 ----------------
.PHONY: vet clean

vet:
	go vet ./...

# 清理构建产物（src/、data/ 不动）
clean:
	rm -f $(BINARY) $(LINUX_BINARY) $(WINDOWS_BINARY) $(LAUNCHER_BINARY)
