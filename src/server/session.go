package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

const maxStoredMessages = 500

var sessionIDPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}(-\d+)?$`)

type FileMeta struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Type    string `json:"type"`
	From    string `json:"from,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

type Message struct {
	ID       string    `json:"id"`
	ClientID string    `json:"clientId"`
	Name     string    `json:"name"`
	TS       int64     `json:"ts"`
	Text     string    `json:"text,omitempty"`
	File     *FileMeta `json:"file,omitempty"`
}

// Session 是一个会话，对应 data/sessions/<日期_时间>/ 目录。
// 目录里 session.json 存聊天与文件元数据，uploads/ 存文件本体。
type Session struct {
	ID        string     `json:"id"`
	CreatedAt int64      `json:"createdAt"`
	UpdatedAt int64      `json:"updatedAt"`
	Messages  []Message  `json:"messages"`
	Files     []FileMeta `json:"files"`

	persisted bool
	fileIndex map[string]FileMeta
}

// SessionInfo 是发给页面的会话摘要。
type SessionInfo struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	Messages  int    `json:"messages"`
	Files     int    `json:"files"`
	Current   bool   `json:"current"`
}

// Manager 管理所有会话。会话目录是懒创建的：只有真正产生消息或文件才落盘，
// 因此启动后什么都不做就退出，不会留下空文件夹。
type Manager struct {
	mu      sync.Mutex
	root    string
	current *Session
	index   map[string]*Session
}

func NewManager(root string) (*Manager, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}

	manager := &Manager{root: root, index: make(map[string]*Session)}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() || !sessionIDPattern.MatchString(entry.Name()) {
			continue
		}
		session, err := loadSession(filepath.Join(root, entry.Name(), "session.json"))
		if err != nil {
			continue
		}
		manager.index[session.ID] = session
	}
	return manager, nil
}

func loadSession(path string) (*Session, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var session Session
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, err
	}
	if !sessionIDPattern.MatchString(session.ID) {
		return nil, fmt.Errorf("非法会话 ID：%s", session.ID)
	}

	if session.CreatedAt == 0 {
		if info, err := os.Stat(path); err == nil {
			session.CreatedAt = info.ModTime().UnixMilli()
		}
	}
	if session.UpdatedAt == 0 {
		session.UpdatedAt = session.CreatedAt
	}

	session.persisted = true
	session.reindex()
	return &session, nil
}

func (s *Session) reindex() {
	s.fileIndex = make(map[string]FileMeta, len(s.Files))
	for _, file := range s.Files {
		s.fileIndex[file.ID] = file
	}
}

// BeginNew 在内存里开一个新会话（尚未落盘），文件夹名 = 启动时刻。
func (m *Manager) BeginNew() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := m.newSessionLocked()
	m.current = session
	return session
}

func (m *Manager) newSessionLocked() *Session {
	session := &Session{
		ID:        time.Now().Format("2006-01-02_15-04-05"),
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
		fileIndex: make(map[string]FileMeta),
	}

	if _, exists := m.index[session.ID]; exists {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s-%d", session.ID, i)
			if _, exists := m.index[candidate]; !exists {
				session.ID = candidate
				break
			}
		}
	}

	m.index[session.ID] = session
	return session
}

func (m *Manager) Current() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Switch 切换到历史会话：原样加载，不改文件夹名，也不产生新文件。
func (m *Manager) Switch(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !sessionIDPattern.MatchString(id) {
		return nil, fmt.Errorf("非法会话 ID")
	}

	if session, ok := m.index[id]; ok {
		m.current = session
		return session, nil
	}

	session, err := loadSession(filepath.Join(m.root, id, "session.json"))
	if err != nil {
		return nil, err
	}
	m.index[id] = session
	m.current = session
	return session, nil
}

func (m *Manager) List() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	infos := make([]SessionInfo, 0, len(m.index))
	for _, session := range m.index {
		infos = append(infos, m.infoLocked(session))
	}

	// 最近聊过的排最前
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].UpdatedAt != infos[j].UpdatedAt {
			return infos[i].UpdatedAt > infos[j].UpdatedAt
		}
		return infos[i].ID > infos[j].ID
	})
	return infos
}

func (m *Manager) Info(session *Session) *SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	info := m.infoLocked(session)
	return &info
}

func (m *Manager) infoLocked(session *Session) SessionInfo {
	files := 0
	for _, file := range session.Files {
		if !file.Deleted {
			files++
		}
	}

	return SessionInfo{
		ID:        session.ID,
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
		Messages:  len(session.Messages),
		Files:     files,
		Current:   session == m.current,
	}
}

func (m *Manager) Messages(session *Session) []Message {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Message, len(session.Messages))
	copy(out, session.Messages)
	return out
}

func (m *Manager) AddMessage(message Message) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := m.current
	session.Messages = append(session.Messages, message)
	if len(session.Messages) > maxStoredMessages {
		session.Messages = session.Messages[len(session.Messages)-maxStoredMessages:]
	}
	session.UpdatedAt = message.TS
	m.persistLocked(session)
}

func (m *Manager) AddFile(file FileMeta) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := m.current
	session.Files = append(session.Files, file)
	session.fileIndex[file.ID] = file
	if time.Now().UnixMilli() > session.UpdatedAt {
		session.UpdatedAt = time.Now().UnixMilli()
	}
	m.persistLocked(session)
}

// Ensure 确保会话目录已创建（上传文件前需要先建目录）。
func (m *Manager) Ensure(session *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.ensureLocked(session)
}

func (m *Manager) UploadsDir(session *Session) string {
	return filepath.Join(m.root, session.ID, "uploads")
}

func (m *Manager) File(sessionID, fileID string) (FileMeta, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.index[sessionID]
	if !ok {
		return FileMeta{}, false
	}
	file, ok := session.fileIndex[fileID]
	return file, ok
}

// Files 返回会话里未被删除的文件（文件列表以此为准，不依赖聊天消息）。
func (m *Manager) Files(session *Session) []FileMeta {
	m.mu.Lock()
	defer m.mu.Unlock()

	files := make([]FileMeta, 0, len(session.Files))
	for _, file := range session.Files {
		if !file.Deleted {
			files = append(files, file)
		}
	}
	return files
}

// DeleteSession 删除整个会话目录（含里面的文件）。
// 如果删掉的是当前会话，会立刻建一个新会话并把新会话返回给调用方广播。
func (m *Manager) DeleteSession(sessionID string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !sessionIDPattern.MatchString(sessionID) {
		return nil, fmt.Errorf("非法会话 ID")
	}
	if _, ok := m.index[sessionID]; !ok {
		return nil, fmt.Errorf("会话不存在")
	}

	if err := os.RemoveAll(filepath.Join(m.root, sessionID)); err != nil {
		return nil, err
	}
	delete(m.index, sessionID)

	var replacement *Session
	if m.current != nil && m.current.ID == sessionID {
		// 不能留下 current = nil 的空档，否则并发写入会崩
		replacement = m.newSessionLocked()
		m.current = replacement
	}
	return replacement, nil
}

// DeleteFile 标记文件已删除并返回它在磁盘上的路径，由调用方删除实体文件。
func (m *Manager) DeleteFile(fileID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := m.current
	file, ok := session.fileIndex[fileID]
	if !ok || file.Deleted {
		return "", false
	}

	file.Deleted = true
	session.fileIndex[fileID] = file
	for i := range session.Files {
		if session.Files[i].ID == fileID {
			session.Files[i] = file
		}
	}
	// 消息里内嵌的文件信息也要标记，否则重新加载历史时被删的文件会"复活"
	for i := range session.Messages {
		if session.Messages[i].File != nil && session.Messages[i].File.ID == fileID {
			session.Messages[i].File.Deleted = true
		}
	}
	m.persistLocked(session)

	return filepath.Join(m.root, session.ID, "uploads", fileID), true
}

func (m *Manager) ensureLocked(session *Session) error {
	if session.persisted {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(m.root, session.ID, "uploads"), 0o755); err != nil {
		return err
	}
	session.persisted = true
	// 先把目录和元数据落盘，保证会话在列表里可见
	return m.persistLocked(session)
}

// persistLocked 原子写入 session.json，写入前确保目录存在。
func (m *Manager) persistLocked(session *Session) error {
	dir := filepath.Join(m.root, session.ID)
	if err := os.MkdirAll(filepath.Join(dir, "uploads"), 0o755); err != nil {
		return err
	}
	session.persisted = true

	files := make([]FileMeta, len(session.Files))
	copy(files, session.Files)
	sort.Slice(files, func(i, j int) bool { return files[i].ID < files[j].ID })

	payload, err := json.MarshalIndent(struct {
		ID        string     `json:"id"`
		CreatedAt int64      `json:"createdAt"`
		UpdatedAt int64      `json:"updatedAt"`
		Messages  []Message  `json:"messages"`
		Files     []FileMeta `json:"files"`
	}{
		ID:        session.ID,
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
		Messages:  session.Messages,
		Files:     files,
	}, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(dir, "session.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
