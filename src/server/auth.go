package main

// 身份与准入（服务器版）。
//
// 两种角色：
//   主机 —— 服务启动时随机生成一个「一次性主机密钥」，打印在启动日志里；
//           用 ?host=<key> 打开一次即换成长期 cookie，密钥随即作废。
//           主机随时可以在页面上重新生成一张新链接（旧的作废），用于换设备或清了缓存。
//   访客 —— 主机为某个人生成一张「一次性邀请链接」（可备注姓名）；
//           对方点开即绑定到他的浏览器并换到 cookie，链接随即作废，转发给别人打不开。
//
// 两种 cookie 都是 HMAC 签名、HttpOnly，并且**滑动续期**：只要在有效期内上线过，
// 有效期就从这次访问重新计算（主机 30 天、访客 7 天）。
//
// 状态存放：
//   主机密钥、邀请码、最近上线的设备 —— 全部只在内存，服务重启即清空；
//   签名密钥 secret.key 落盘（0600），所以重启后已发出的 cookie 仍然有效，主客都不会掉线。
//
// 安全要点：注销邀请码后，用该邀请码换来的 cookie 会在下一次请求就被拒绝（cookie 里带邀请码 ID）。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	cookieHost  = "lanfile_host"
	cookieGuest = "lanfile_guest"

	// renewAfter：凭证签发超过这么久，就在这次响应里重新签一张（滑动过期）。
	// 不是每个请求都重发，避免无意义的 Set-Cookie。
	renewAfter = time.Hour

	secretBytes = 32 // secret.key 长度
	hostKeyLen  = 16 // 主机密钥/邀请码的随机字节数（16 字节 = 32 位十六进制）
)

// Role 是访问者身份。
type Role string

const (
	RoleHost  Role = "host"
	RoleGuest Role = "guest"
)

// Identity 是一次请求判定出的身份。
type Identity struct {
	Role     Role   // 主机 / 访客
	InviteID string // 访客来源邀请码 ID（主机为空）
	PassID   string // 访客凭证 ID：用于设备识别与「撤销邀请后立即失效」
	IP       string // 来源 IP（经 nginx 时取 X-Forwarded-For 的第一段）
	UA       string // User-Agent，仅作展示
}

// Valid 表示这个身份通过了校验（主机或访客）。
func (i Identity) Valid() bool { return i.Role == RoleHost || i.Role == RoleGuest }

// IsHost 表示是否主机。
func (i Identity) IsHost() bool { return i.Role == RoleHost }

// DeviceKey 是身份对应的设备标识：主机固定为 "host"，访客用凭证 ID。
func (i Identity) DeviceKey() string {
	if i.Role == RoleHost {
		return "host"
	}
	return i.PassID
}

// Invite 是主机发出的某一张邀请链接。
type Invite struct {
	ID        string `json:"id"`        // 邀请码，同时是链接里的 code
	Note      string `json:"note"`      // 备注：发给谁
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"` // 未被使用时的失效时间
	UsedAt    int64  `json:"usedAt,omitempty"`
	Revoked   bool   `json:"revoked,omitempty"`
	BoundIP   string `json:"boundIp,omitempty"`
	BoundUA   string `json:"boundUa,omitempty"`
	LastSeen  int64  `json:"lastSeen,omitempty"`
	Status    string `json:"status"` // 由服务端算好的状态：待使用/已使用/已撤销/已过期

	passID string // 绑定后发给访客的凭证 ID，只在服务端使用
}

// SeenDevice 是最近上线过的设备，供主机页面查看（只在内存，重启即清空）。
type SeenDevice struct {
	Key      string `json:"key"`
	Role     Role   `json:"role"`
	Name     string `json:"name,omitempty"` // 前端 hello 上报的昵称
	InviteID string `json:"inviteId,omitempty"`
	Note     string `json:"note,omitempty"`
	IP       string `json:"ip,omitempty"`
	UA       string `json:"ua,omitempty"`
	FirstAt  int64  `json:"firstSeen"`
	LastAt   int64  `json:"lastSeen"`
}

// Auth 管理主机密钥、邀请码、cookie 签发与身份判定。
type Auth struct {
	mu            sync.Mutex
	secret        []byte
	hostKey       string // 当前有效的一次性主机密钥，空表示已用过（需重新生成）
	trustLoopback bool   // 直连且来自回环地址时视为主机（本地开发用；经 nginx 时必须关闭）

	hostTTL   time.Duration // 主机 cookie 有效期（滑动）
	guestTTL  time.Duration // 访客 cookie 有效期（滑动）
	inviteTTL time.Duration // 邀请链接未被使用时的有效期

	invites map[string]*Invite     // 邀请码 ID -> 邀请
	seen    map[string]*SeenDevice // 设备标识 -> 最近上线记录
}

// NewAuth 读取（或首次生成）签名密钥，并生成第一张一次性主机密钥。
func NewAuth(secretPath string, trustLoopback bool, hostTTL, guestTTL, inviteTTL time.Duration) (*Auth, error) {
	secret, err := loadOrCreateSecret(secretPath)
	if err != nil {
		return nil, err
	}

	auth := &Auth{
		secret:        secret,
		hostKey:       randomToken(hostKeyLen),
		trustLoopback: trustLoopback,
		hostTTL:       hostTTL,
		guestTTL:      guestTTL,
		inviteTTL:     inviteTTL,
		invites:       make(map[string]*Invite),
		seen:          make(map[string]*SeenDevice),
	}
	return auth, nil
}

/* ---------------- 主机密钥与主机链接 ---------------- */

// HostKey 返回当前有效的一次性主机密钥（已用过则为空）。
func (a *Auth) HostKey() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hostKey
}

// HostLink 拼出主机访问链接；baseURL 为空时由调用方用请求地址兜底。
func (a *Auth) HostLink(baseURL string) string {
	key := a.HostKey()
	if key == "" {
		return ""
	}
	return fmt.Sprintf("%s/?host=%s", strings.TrimRight(baseURL, "/"), key)
}

// RotateHostKey 生成新的一次性主机密钥，旧的立即作废。
func (a *Auth) RotateHostKey() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hostKey = randomToken(hostKeyLen)
	return a.hostKey
}

// RedeemHostKey 校验主机密钥；成功即作废（一次性使用）。
func (a *Auth) RedeemHostKey(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hostKey == "" || key == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(a.hostKey)) != 1 {
		return false
	}
	a.hostKey = ""
	return true
}

/* ---------------- 邀请码 ---------------- */

// CreateInvite 生成一张一次性邀请链接（A 方案：谁先点谁绑定，转发无效）。
func (a *Auth) CreateInvite(note string) Invite {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanupLocked()

	now := time.Now()
	invite := &Invite{
		ID:        randomToken(hostKeyLen),
		Note:      cleanName(note),
		CreatedAt: now.UnixMilli(),
		ExpiresAt: now.Add(a.inviteTTL).UnixMilli(),
		Status:    statusPending,
	}
	a.invites[invite.ID] = invite
	return *invite
}

// ListInvites 返回全部邀请码（含状态），最近创建的排前面。
func (a *Auth) ListInvites() []Invite {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanupLocked()

	list := make([]Invite, 0, len(a.invites))
	for _, invite := range a.invites {
		invite.Status = a.statusLocked(invite)
		list = append(list, *invite)
	}
	sortInvites(list)
	return list
}

// RevokeInvite 撤销邀请码：已绑定的访客下一次请求就会被拒绝。
func (a *Auth) RevokeInvite(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	invite, ok := a.invites[id]
	if !ok {
		return false
	}
	invite.Revoked = true
	invite.Status = statusRevoked
	return true
}

// Invite 按 ID 查一张邀请（不带内部字段），用于展示备注。
func (a *Auth) Invite(id string) (Invite, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	invite, ok := a.invites[id]
	if !ok {
		return Invite{}, false
	}
	out := *invite
	out.Status = a.statusLocked(invite)
	return out, true
}

// RedeemInvite 用邀请码换访客身份；成功即绑定（A 方案），链接随即作废。
func (a *Auth) RedeemInvite(code, ip, ua string) (Identity, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanupLocked()

	invite, ok := a.invites[code]
	if !ok {
		return Identity{}, "邀请链接无效"
	}
	switch a.statusLocked(invite) {
	case statusRevoked:
		return Identity{}, "邀请链接已被主机撤销"
	case statusUsed:
		return Identity{}, "邀请链接已被使用（一次性的，请让主机重新发一张）"
	case statusExpired:
		return Identity{}, "邀请链接已过期（超过 24 小时未使用）"
	}

	invite.passID = randomToken(hostKeyLen)
	invite.UsedAt = time.Now().UnixMilli()
	invite.BoundIP = ip
	invite.BoundUA = ua
	invite.LastSeen = invite.UsedAt
	invite.Status = statusUsed

	return Identity{Role: RoleGuest, InviteID: invite.ID, PassID: invite.passID, IP: ip, UA: ua}, ""
}

/* ---------------- 身份判定与 cookie ---------------- */

// IdentityOf 判定这次请求的身份；顺带把设备记录进内存（主机页面据此显示在线设备）。
// 注意：w 可以为 nil（只判定、不下发续期 cookie）。
func (a *Auth) IdentityOf(w http.ResponseWriter, r *http.Request) Identity {
	ip := clientIP(r)
	ua := r.UserAgent()
	now := time.Now()

	if payload, ok := a.readCookie(r, cookieHost); ok {
		if now.Sub(time.UnixMilli(payload.issuedAt)) <= a.hostTTL {
			id := Identity{Role: RoleHost, IP: ip, UA: ua}
			a.touch(id)
			if w != nil && now.Sub(time.UnixMilli(payload.issuedAt)) > renewAfter {
				a.setCookie(w, r, cookieHost, a.hostTTL, cookiePayload{role: RoleHost, issuedAt: now.UnixMilli()})
			}
			return id
		}
	}

	if payload, ok := a.readCookie(r, cookieGuest); ok {
		if now.Sub(time.UnixMilli(payload.issuedAt)) <= a.guestTTL {
			if invite := a.inviteFor(payload.inviteID, payload.passID); invite != nil {
				id := Identity{Role: RoleGuest, InviteID: invite.ID, PassID: payload.passID, IP: ip, UA: ua}
				a.touch(id)
				if w != nil && now.Sub(time.UnixMilli(payload.issuedAt)) > renewAfter {
					a.setCookie(w, r, cookieGuest, a.guestTTL, cookiePayload{
						role: RoleGuest, inviteID: invite.ID, passID: payload.passID, issuedAt: now.UnixMilli(),
					})
				}
				return id
			}
		}
	}

	// 本地开发用：直接在本机打开时算主机。经 nginx 转发时所有请求都来自回环，
	// 这个开关必须关闭（-trust-loopback=false），否则任何访客都会被当成主机。
	if a.trustLoopback && isLoopback(r) {
		id := Identity{Role: RoleHost, IP: ip, UA: ua}
		a.touch(id)
		return id
	}

	return Identity{IP: ip, UA: ua}
}

// GrantHost 给这次请求下发主机会话 cookie（已通过主机密钥校验）。
func (a *Auth) GrantHost(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UnixMilli()
	a.setCookie(w, r, cookieHost, a.hostTTL, cookiePayload{role: RoleHost, issuedAt: now})
	a.touch(Identity{Role: RoleHost, IP: clientIP(r), UA: r.UserAgent()})
}

// GrantGuest 给这次请求下发访客会话 cookie（已用邀请码换得身份）。
func (a *Auth) GrantGuest(w http.ResponseWriter, r *http.Request, id Identity) {
	now := time.Now().UnixMilli()
	a.setCookie(w, r, cookieGuest, a.guestTTL, cookiePayload{
		role: RoleGuest, inviteID: id.InviteID, passID: id.PassID, issuedAt: now,
	})
}

type cookiePayload struct {
	role     Role
	inviteID string
	passID   string
	issuedAt int64
}

func (a *Auth) setCookie(w http.ResponseWriter, r *http.Request, name string, ttl time.Duration, payload cookiePayload) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    a.sign(payload),
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
	})
}

func (a *Auth) readCookie(r *http.Request, name string) (cookiePayload, bool) {
	cookie, err := r.Cookie(name)
	if err != nil || cookie.Value == "" {
		return cookiePayload{}, false
	}
	return a.verify(cookie.Value)
}

// sign 用 HMAC-SHA256 签名，cookie 内容形如 base64(payload).base64(签名)。
func (a *Auth) sign(payload cookiePayload) string {
	raw := fmt.Sprintf("%s|%s|%s|%d", payload.role, payload.inviteID, payload.passID, payload.issuedAt)
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte(raw))
	return base64.RawURLEncoding.EncodeToString([]byte(raw)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Auth) verify(value string) (cookiePayload, bool) {
	head, tail, ok := strings.Cut(value, ".")
	if !ok {
		return cookiePayload{}, false
	}
	rawBytes, err := base64.RawURLEncoding.DecodeString(head)
	if err != nil {
		return cookiePayload{}, false
	}
	want, err := base64.RawURLEncoding.DecodeString(tail)
	if err != nil {
		return cookiePayload{}, false
	}

	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write(rawBytes)
	if !hmac.Equal(mac.Sum(nil), want) {
		return cookiePayload{}, false
	}

	parts := strings.Split(string(rawBytes), "|")
	if len(parts) != 4 {
		return cookiePayload{}, false
	}
	issuedAt, err := parseInt64(parts[3])
	if err != nil {
		return cookiePayload{}, false
	}
	return cookiePayload{role: Role(parts[0]), inviteID: parts[1], passID: parts[2], issuedAt: issuedAt}, true
}

func (a *Auth) inviteFor(inviteID, passID string) *Invite {
	a.mu.Lock()
	defer a.mu.Unlock()

	invite, ok := a.invites[inviteID]
	if !ok || invite.Revoked || invite.passID == "" {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(passID), []byte(invite.passID)) != 1 {
		return nil
	}
	invite.LastSeen = time.Now().UnixMilli()
	invite.Status = statusUsed
	out := *invite
	return &out
}

/* ---------------- 设备记录 ---------------- */

// touch 记录设备最近一次上线，供主机页面显示。
func (a *Auth) touch(id Identity) {
	if !id.Valid() {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	key := id.DeviceKey()
	now := time.Now().UnixMilli()
	entry, ok := a.seen[key]
	if !ok {
		entry = &SeenDevice{Key: key, Role: id.Role, FirstAt: now}
		a.seen[key] = entry
		// 访客记录跟着邀请码的状态走
		if invite := a.invites[id.InviteID]; invite != nil {
			entry.InviteID = invite.ID
			entry.Note = invite.Note
			invite.LastSeen = now
		}
	}
	entry.Role = id.Role
	entry.IP = id.IP
	entry.UA = id.UA
	entry.LastAt = now
}

// SetDeviceName 在浏览器上报昵称后更新设备记录。
func (a *Auth) SetDeviceName(deviceKey, name string) {
	if deviceKey == "" || name == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry, ok := a.seen[deviceKey]; ok {
		entry.Name = name
	}
}

// Devices 返回最近上线过的设备，最近活动的排前面。
func (a *Auth) Devices() []SeenDevice {
	a.mu.Lock()
	defer a.mu.Unlock()

	list := make([]SeenDevice, 0, len(a.seen))
	for _, entry := range a.seen {
		list = append(list, *entry)
	}
	sortDevices(list)
	return list
}

/* ---------------- 内部工具 ---------------- */

const (
	statusPending = "待使用"
	statusUsed    = "已使用"
	statusRevoked = "已撤销"
	statusExpired = "已过期"
)

func (a *Auth) statusLocked(invite *Invite) string {
	switch {
	case invite.Revoked:
		return statusRevoked
	case invite.UsedAt > 0:
		return statusUsed
	case time.Now().UnixMilli() > invite.ExpiresAt:
		return statusExpired
	default:
		return statusPending
	}
}

// cleanupLocked 清掉过期且没用过的邀请码（保持内存干净，重启本来也会清空）。
func (a *Auth) cleanupLocked() {
	now := time.Now().UnixMilli()
	for id, invite := range a.invites {
		if invite.UsedAt == 0 && !invite.Revoked && now > invite.ExpiresAt {
			delete(a.invites, id)
		}
	}
	// 设备记录最多保留 200 条
	if len(a.seen) > 200 {
		oldestKey, oldest := "", int64(1<<62)
		for key, entry := range a.seen {
			if entry.LastAt < oldest {
				oldestKey, oldest = key, entry.LastAt
			}
		}
		delete(a.seen, oldestKey)
	}
}

func sortInvites(list []Invite) {
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt > list[j].CreatedAt })
}

func sortDevices(list []SeenDevice) {
	sort.Slice(list, func(i, j int) bool { return list[i].LastAt > list[j].LastAt })
}

func parseInt64(text string) (int64, error) {
	var value int64
	_, err := fmt.Sscanf(text, "%d", &value)
	return value, err
}

// clientIP 取来源 IP：经 nginx 时优先用 X-Forwarded-For 的第一段。
func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, ok := strings.Cut(forwarded, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	if real := r.Header.Get("X-Real-IP"); real != "" {
		return strings.TrimSpace(real)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// randomToken 生成 n 字节随机数的十六进制串。
func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return newID()
	}
	return hex.EncodeToString(buf)
}

// loadOrCreateSecret 读取或首次生成签名密钥（0600），保证重启后 cookie 仍有效。
func loadOrCreateSecret(path string) ([]byte, error) {
	if raw, err := os.ReadFile(path); err == nil {
		if secret, err := hex.DecodeString(strings.TrimSpace(string(raw))); err == nil && len(secret) >= 16 {
			return secret, nil
		}
	}

	secret := make([]byte, secretBytes)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(secret)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return secret, nil
}
