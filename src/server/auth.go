package main

import (
	"crypto/subtle"
	"net"
	"net/http"
)

// Auth 负责判断请求来自哪台设备、有什么权限。
//
// 目前只有两种角色：主机（启动服务的那台机器）和其他设备。
// 主机身份用两种方式认定：启动时打印的一次性口令，或从本机回环地址连进来。
// 将来要放到公网时，在这里扩展登录态 / 房间密码 / Token 校验即可，
// 调用方（api.go）不用改。
type Auth struct {
	token string
}

func NewAuth(token string) *Auth {
	return &Auth{token: token}
}

// IsHost 判断一个 HTTP 请求是否来自主机。
func (a *Auth) IsHost(r *http.Request) bool {
	return a.IsHostRequest(r.URL.Query().Get("host"), r.RemoteAddr)
}

// IsHostRequest 判断令牌或来源地址是否代表主机。
func (a *Auth) IsHostRequest(token, remoteAddr string) bool {
	if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(a.token)) == 1 {
		return true
	}

	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
