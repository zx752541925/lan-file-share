package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Event 是服务端推送给浏览器的统一消息格式。
type Event struct {
	Type     string        `json:"type"`
	SelfID   string        `json:"selfId,omitempty"`
	Name     string        `json:"name,omitempty"`
	Host     bool          `json:"host,omitempty"`
	Peers    int           `json:"peers"`
	Session  *SessionInfo  `json:"session,omitempty"`
	History  []Message     `json:"history"`
	Sessions []SessionInfo `json:"sessions,omitempty"`
	LAN      []LANAddress  `json:"lan,omitempty"`
	Message  *Message      `json:"message,omitempty"`
	FileID   string        `json:"fileId,omitempty"`
}

// Incoming 是浏览器发来的消息。
type Incoming struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	ClientID  string `json:"clientId"`
	Text      string `json:"text"`
	FileID    string `json:"fileId"`
	SessionID string `json:"sessionId"`
}

func (a *App) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	client := &Client{
		conn: conn,
		send: make(chan []byte, 64),
		id:   newID(),
		host: a.isHost(r),
	}
	if client.host {
		client.name = hostName
	}

	a.hub.register <- client
	go client.writePump()
	defer func() { a.hub.unregister <- client }()

	a.serveClient(client)
}

// isHost 判定连接是否来自启动服务的这台机器：
// 优先看启动时打印的主机口令，其次看是否从本机回环地址连进来。
func (a *App) isHost(r *http.Request) bool {
	if token := r.URL.Query().Get("host"); token != "" {
		if subtle.ConstantTimeCompare([]byte(token), []byte(a.hostToken)) == 1 {
			return true
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (a *App) serveClient(client *Client) {
	client.conn.SetReadLimit(maxMessage)
	_ = client.conn.SetReadDeadline(time.Now().Add(pongWait))
	client.conn.SetPongHandler(func(string) error {
		return client.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	session := a.sessions.Current()
	a.sendTo(client, Event{
		Type:    "init",
		SelfID:  client.id,
		Name:    client.name,
		Host:    client.host,
		Peers:   a.hub.Count(),
		Session: a.sessions.Info(session),
		History: a.sessions.Messages(session),
		LAN:     a.lanAddresses(),
	})

	for {
		_, payload, err := client.conn.ReadMessage()
		if err != nil {
			return
		}

		var incoming Incoming
		if err := json.Unmarshal(payload, &incoming); err != nil {
			continue
		}

		switch incoming.Type {
		case "hello":
			a.applyIdentity(client, incoming)

		case "chat":
			a.handleChat(client, incoming)

		case "sessions":
			// 历史会话列表只有主机能看
			if !client.host {
				continue
			}
			a.sendTo(client, Event{Type: "sessions", Sessions: a.sessions.List()})

		case "switch":
			if !client.host {
				continue
			}
			a.handleSwitch(incoming)

		case "delete":
			// 只有主机能删除文件
			if !client.host {
				continue
			}
			a.handleDelete(incoming.FileID)

		case "deleteSession":
			// 只有主机能删除历史会话
			if !client.host {
				continue
			}
			a.handleDeleteSession(incoming.SessionID)
		}
	}
}

// applyIdentity 固定设备身份：id 由浏览器生成并长期保存，昵称没给就自动分配。
func (a *App) applyIdentity(client *Client, incoming Incoming) {
	if id := strings.TrimSpace(incoming.ClientID); isClientID(id) {
		client.id = id
	}

	name := strings.TrimSpace(incoming.Name)
	if name == "" {
		a.assignDefaultName(client)
		a.replySelf(client)
		return
	}

	client.name = cleanName(name)
	if number := numberFromName(client.name); number > 0 {
		if a.hub.UsedNumbers(client.id)[number] {
			// 这个数字已被在线设备占用，换一个，保证同时在线不重号
			a.assignDefaultName(client)
		} else {
			client.number = number
		}
	}
	a.replySelf(client)
}

func (a *App) assignDefaultName(client *Client) {
	if client.host {
		client.name = hostName
		client.number = 0
		return
	}

	used := a.hub.UsedNumbers(client.id)
	name, number := randomGuestName(func(candidate int) bool { return used[candidate] })
	client.name = name
	client.number = number
}

func (a *App) replySelf(client *Client) {
	a.sendTo(client, Event{Type: "self", SelfID: client.id, Name: client.name})
}

func (a *App) handleChat(client *Client, incoming Incoming) {
	text := strings.TrimSpace(incoming.Text)
	if text == "" && incoming.FileID == "" {
		return
	}

	session := a.sessions.Current()
	message := Message{
		ID:       newID(),
		ClientID: client.id,
		Name:     client.name,
		TS:       time.Now().UnixMilli(),
		Text:     text,
	}

	if incoming.FileID != "" {
		meta, ok := a.sessions.File(session.ID, incoming.FileID)
		if !ok {
			return
		}
		message.File = &meta
	}

	a.sessions.AddMessage(message)
	a.broadcast(Event{Type: "message", Message: &message})
}

func (a *App) handleSwitch(incoming Incoming) {
	session, err := a.sessions.Switch(incoming.SessionID)
	if err != nil {
		log.Printf("切换会话失败：%v", err)
		return
	}

	log.Printf("切换会话到 %s", session.ID)
	a.broadcast(Event{
		Type:    "session",
		Session: a.sessions.Info(session),
		History: a.sessions.Messages(session),
	})
}

func (a *App) handleDelete(fileID string) {
	path, ok := a.sessions.DeleteFile(fileID)
	if !ok {
		return
	}

	_ = os.Remove(path) // 磁盘实体一并删除
	log.Printf("删除文件 %s", fileID)
	a.broadcast(Event{Type: "deleted", FileID: fileID})
}

func (a *App) handleDeleteSession(sessionID string) {
	replacement, err := a.sessions.DeleteSession(sessionID)
	if err != nil {
		log.Printf("删除会话失败：%v", err)
		return
	}

	log.Printf("删除会话 %s", sessionID)

	// 删掉的正好是当前会话：立刻开一个新会话，所有设备一起切过去
	if replacement != nil {
		log.Printf("已开启新会话 %s", replacement.ID)
		a.broadcast(Event{
			Type:    "session",
			Session: a.sessions.Info(replacement),
			History: a.sessions.Messages(replacement),
		})
	}

	a.broadcast(Event{Type: "sessions", Sessions: a.sessions.List()})
}

func (a *App) sendTo(client *Client, event Event) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	select {
	case client.send <- payload:
	default:
	}
}

func (a *App) broadcast(event Event) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	a.hub.Send(payload)
}

func (a *App) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, a.maxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "文件过大或表单解析失败"})
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	src, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少文件字段 file"})
		return
	}
	defer func() { _ = src.Close() }()

	// 文件存进当前会话的 uploads/，会话目录在此刻才真正创建
	session := a.sessions.Current()
	if err := a.sessions.Ensure(session); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "创建会话目录失败"})
		return
	}

	saved := filepath.Join(a.sessions.UploadsDir(session), newID())
	dst, err := os.Create(saved)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "写入文件失败"})
		return
	}

	size, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(saved)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存文件失败"})
		return
	}

	meta := FileMeta{
		ID:   filepath.Base(saved),
		Name: cleanFileName(header.Filename),
		Size: size,
		Type: sniffType(saved, header.Filename, header.Header.Get("Content-Type")),
	}
	a.sessions.AddFile(meta)
	writeJSON(w, http.StatusOK, meta)
}

// handleFile 处理 /api/files/{会话ID}/{文件ID}[/download]
func (a *App) handleFile(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/files/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || !sessionIDPattern.MatchString(parts[0]) || !isFileID(parts[1]) {
		http.NotFound(w, r)
		return
	}

	meta, ok := a.sessions.File(parts[0], parts[1])
	if !ok || meta.Deleted {
		http.NotFound(w, r)
		return
	}

	file, err := os.Open(filepath.Join(a.sessions.root, parts[0], "uploads", meta.ID))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if meta.Type != "" {
		w.Header().Set("Content-Type", meta.Type)
	}
	w.Header().Set("Content-Disposition", contentDisposition(parts, meta.Name))
	// ServeContent 自带 Range 支持：图片预览、视频拖动、断点续传
	http.ServeContent(w, r, meta.Name, info.ModTime(), file)
}

func contentDisposition(parts []string, name string) string {
	kind := "inline"
	if len(parts) > 2 && parts[2] == "download" {
		kind = "attachment"
	}

	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)

	return fmt.Sprintf("%s; filename=\"%s\"; filename*=UTF-8''%s", kind, ascii, url.PathEscape(name))
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func newID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func isFileID(value string) bool {
	if len(value) != 16 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isClientID(value string) bool {
	if len(value) < 8 || len(value) > 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cleanName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "未命名设备"
	}

	runes := []rune(name)
	if len(runes) > 16 {
		runes = runes[:16]
	}
	return string(runes)
}

func cleanFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "未命名文件"
	}

	runes := []rune(name)
	if len(runes) > 120 {
		runes = runes[:120]
	}
	return string(runes)
}

func detectType(name, declared string) string {
	if declared != "" && declared != "application/octet-stream" {
		return declared
	}
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); byExt != "" {
		return byExt
	}
	return "application/octet-stream"
}

// sniffType 按文件真实内容判断类型，避免"扩展名是 .jpg、内容其实是 PDF"这类
// 名不副实导致前端按图片渲染失败。嗅探不可靠时仍沿用声明值和扩展名。
func sniffType(path, name, declared string) string {
	if declared == "" || declared == "application/octet-stream" {
		declared = detectType(name, declared)
	}

	file, err := os.Open(path)
	if err != nil {
		return declared
	}
	defer func() { _ = file.Close() }()

	buf := make([]byte, 512)
	size, err := io.ReadFull(file, buf)
	if size == 0 || (err != nil && err != io.ErrUnexpectedEOF && err != io.EOF) {
		return declared
	}

	sniffed := http.DetectContentType(buf[:size])
	switch {
	case strings.HasPrefix(sniffed, "image/"):
		// 真实图片：以内容为准，例如 .jpg 里其实是 PNG
		return sniffed
	case strings.HasPrefix(declared, "image/"):
		// 声明是图片但内容不是：以内容为准，例如 .jpg 里其实是 PDF
		if sniffed == "application/octet-stream" {
			return declared
		}
		return sniffed
	default:
		return declared
	}
}
