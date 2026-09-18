package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// 大于这个大小的文件才分片上传，小文件保持一次传完
	chunkThreshold = 8 << 20
	// 未完成的分片保留时间，超时清理
	chunkTTL = 24 * time.Hour
	// 单片最大字节数（前端按 8MB 切，这里留一倍余量）
	maxChunkSize = 16 << 20
)

// chunkedUploads 管理大文件的分片上传，让手机切网/刷新后能续传。
type chunkedUploads struct {
	mu      sync.Mutex
	root    string // <data>/tmp
	app     *App
	uploads map[string]*uploadSession
}

type uploadSession struct {
	mu        sync.Mutex
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"createdAt"`
}

func newChunkedUploads(root string, app *App) *chunkedUploads {
	manager := &chunkedUploads{root: root, app: app, uploads: make(map[string]*uploadSession)}
	manager.cleanup()
	go func() {
		for range time.Tick(time.Hour) {
			manager.cleanup()
		}
	}()
	return manager
}

func (c *chunkedUploads) dir(uploadID string) string {
	return filepath.Join(c.root, uploadID)
}

// cleanup 删除超时未完成的分片目录。
func (c *chunkedUploads) cleanup() {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < chunkTTL {
			continue
		}
		_ = os.RemoveAll(filepath.Join(c.root, entry.Name()))
		log.Printf("清理未完成的上传 %s", entry.Name())
	}
}

// handleInit 开始一次分片上传，返回上传 ID 和分片大小。
func (c *chunkedUploads) handleInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var request struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "参数解析失败"})
		return
	}

	uploadID := newID()
	session := &uploadSession{
		ID:        uploadID,
		Name:      cleanFileName(request.Name),
		Size:      request.Size,
		CreatedAt: time.Now().UnixMilli(),
	}

	dir := c.dir(uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "创建上传目录失败"})
		return
	}
	if err := writeUploadMeta(dir, session); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存上传信息失败"})
		return
	}

	c.mu.Lock()
	c.uploads[uploadID] = session
	c.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"uploadId":  uploadID,
		"chunkSize": chunkThreshold,
		"received":  []int{},
	})
}

// handleStatus 返回已经收到的分片序号，用于断点续传。
func (c *chunkedUploads) handleStatus(w http.ResponseWriter, r *http.Request) {
	uploadID := r.URL.Query().Get("uploadId")
	if !isFileID(uploadID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "上传 ID 非法"})
		return
	}
	if !fileExists(c.dir(uploadID)) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "上传已过期"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploadId": uploadID, "received": c.receivedChunks(uploadID)})
}

// handleChunk 接收一个分片，可重复提交同一片（幂等）。
func (c *chunkedUploads) handleChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	uploadID := r.URL.Query().Get("uploadId")
	index, err := strconv.Atoi(r.URL.Query().Get("index"))
	if !isFileID(uploadID) || err != nil || index < 0 || index > 100000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "分片参数非法"})
		return
	}

	dir := c.dir(uploadID)
	if !fileExists(dir) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "上传已过期，请重新上传"})
		return
	}

	lock := c.lockFor(uploadID)
	lock.Lock()
	defer lock.Unlock()

	r.Body = http.MaxBytesReader(w, r.Body, maxChunkSize)
	target := filepath.Join(dir, fmt.Sprintf("%06d.part", index))
	file, err := os.Create(target)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "写入分片失败"})
		return
	}

	size, copyErr := io.CopyBuffer(file, r.Body, make([]byte, 512<<10))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(target)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "分片接收中断"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"index": index, "size": size})
}

// handleComplete 合并所有分片成正式文件，并建消息广播。
func (c *chunkedUploads) handleComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var request completeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "参数解析失败"})
		return
	}
	if !isFileID(request.UploadID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "上传 ID 非法"})
		return
	}

	dir := c.dir(request.UploadID)
	meta, err := readUploadMeta(dir)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "上传已过期，请重新上传"})
		return
	}

	lock := c.lockFor(request.UploadID)
	lock.Lock()
	defer lock.Unlock()

	chunks := c.receivedChunks(request.UploadID)
	if len(chunks) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有收到任何分片"})
		return
	}
	if meta.Size > 0 {
		var total int64
		for _, index := range chunks {
			if info, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%06d.part", index))); err == nil {
				total += info.Size()
			}
		}
		if total != meta.Size {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("分片不完整：收到 %d 字节，应为 %d 字节", total, meta.Size),
			})
			return
		}
	}

	metaResult, err := c.merge(request, meta, chunks)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	_ = os.RemoveAll(dir)
	c.mu.Lock()
	delete(c.uploads, request.UploadID)
	c.mu.Unlock()

	writeJSON(w, http.StatusOK, metaResult)
}

type completeRequest struct {
	UploadID string `json:"uploadId"`
	Text     string `json:"text"`
	Name     string `json:"name"`
	ClientID string `json:"clientId"`
}

// merge 按顺序拼接分片，落进会话目录并生成消息。
func (c *chunkedUploads) merge(request completeRequest, meta *uploadSession, chunks []int) (FileMeta, error) {

	session := c.app.sessions.Current()
	if err := c.app.sessions.Ensure(session); err != nil {
		return FileMeta{}, fmt.Errorf("创建会话目录失败")
	}

	fileID := newID()
	target := filepath.Join(c.app.sessions.UploadsDir(session), fileID)
	dst, err := os.Create(target)
	if err != nil {
		return FileMeta{}, fmt.Errorf("写入文件失败")
	}

	for _, index := range chunks {
		part, err := os.Open(filepath.Join(c.dir(request.UploadID), fmt.Sprintf("%06d.part", index)))
		if err != nil {
			_ = dst.Close()
			_ = os.Remove(target)
			return FileMeta{}, fmt.Errorf("分片 %d 读取失败", index)
		}
		_, copyErr := io.CopyBuffer(dst, part, make([]byte, 512<<10))
		_ = part.Close()
		if copyErr != nil {
			_ = dst.Close()
			_ = os.Remove(target)
			return FileMeta{}, fmt.Errorf("合并分片失败")
		}
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(target)
		return FileMeta{}, fmt.Errorf("写入文件失败")
	}

	sender := cleanName(request.Name)
	if strings.TrimSpace(request.Name) == "" {
		sender = "未知设备"
	}
	clientID := request.ClientID
	if !isClientID(clientID) {
		clientID = newID()
	}

	fileMeta := FileMeta{
		ID:   fileID,
		Name: meta.Name,
		Size: meta.Size,
		Type: sniffType(target, meta.Name, ""),
		From: sender,
	}
	c.app.sessions.AddFile(fileMeta)
	message := Message{
		ID:       newID(),
		ClientID: clientID,
		Name:     sender,
		TS:       time.Now().UnixMilli(),
		Text:     strings.TrimSpace(request.Text),
		File:     &fileMeta,
	}
	c.app.sessions.AddMessage(message)
	c.app.broadcast(Event{Type: "message", Message: &message})

	return fileMeta, nil
}

func (c *chunkedUploads) receivedChunks(uploadID string) []int {
	entries, err := os.ReadDir(c.dir(uploadID))
	if err != nil {
		return []int{}
	}

	chunks := make([]int, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".part") {
			continue
		}
		if index, err := strconv.Atoi(strings.TrimSuffix(name, ".part")); err == nil {
			chunks = append(chunks, index)
		}
	}
	sort.Ints(chunks)
	return chunks
}

// lockFor 给单个上传加锁，避免同一片并发写入互相踩。
func (c *chunkedUploads) lockFor(uploadID string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()

	session, ok := c.uploads[uploadID]
	if !ok {
		session = &uploadSession{ID: uploadID}
		c.uploads[uploadID] = session
	}
	return &session.mu
}

func writeUploadMeta(dir string, session *uploadSession) error {
	raw, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), raw, 0o644)
}

func readUploadMeta(dir string) (*uploadSession, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}

	var session uploadSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, err
	}
	return &session, nil
}
