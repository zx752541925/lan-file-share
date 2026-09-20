package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 70 * time.Second
	pingPeriod = 25 * time.Second
	maxMessage = 1 << 20 // 单条 WebSocket 消息上限 1 MB
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// 局域网自用服务，允许任意来源连接
	CheckOrigin: func(*http.Request) bool { return true },
}

// Client 表示一个已连接的浏览器。
type Client struct {
	conn *websocket.Conn
	send chan []byte
	id   string
	name string
	host bool
	// number 是访客昵称里的数字，用来保证同时在线设备不重号
	number int
	// 以下字段用于主机页面的「在线设备」列表
	deviceKey   string // 主机固定 "host"，访客为凭证 ID
	inviteID    string // 访客来源邀请码（主机为空）
	ip          string
	ua          string
	connectedAt int64
}

// ClientInfo 是给「在线设备」列表用的连接快照。
type ClientInfo struct {
	Key         string
	ID          string
	Name        string
	Host        bool
	InviteID    string
	IP          string
	UA          string
	ConnectedAt int64
}

// Hub 维护所有连接，并按顺序处理注册、注销与广播。
type Hub struct {
	clients    map[*Client]struct{}
	register   chan *Client
	unregister chan *Client
	broadcast  chan []byte
	countReq   chan chan int
	numbersReq chan numbersRequest
	devicesReq chan chan []ClientInfo
}

type numbersRequest struct {
	exclude string
	reply   chan map[int]bool
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]struct{}),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan []byte, 128),
		countReq:   make(chan chan int),
		numbersReq: make(chan numbersRequest),
		devicesReq: make(chan chan []ClientInfo),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = struct{}{}
			log.Printf("设备上线 %s（当前 %d 台）", client.id, len(h.clients))
			h.pushPeers()

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				log.Printf("设备离线 %s（当前 %d 台）", client.id, len(h.clients))
				h.pushPeers()
			}

		case payload := <-h.broadcast:
			h.deliver(payload)

		case reply := <-h.countReq:
			reply <- len(h.clients)

		case request := <-h.numbersReq:
			used := make(map[int]bool, len(h.clients))
			for client := range h.clients {
				// 同一台设备（同一个设备 ID）的旧连接不算重号，
				// 否则刷新页面时新旧连接重叠会导致自己被改名
				if client.number > 0 && client.id != request.exclude {
					used[client.number] = true
				}
			}
			request.reply <- used

		case reply := <-h.devicesReq:
			list := make([]ClientInfo, 0, len(h.clients))
			for client := range h.clients {
				list = append(list, ClientInfo{
					Key:         client.deviceKey,
					ID:          client.id,
					Name:        client.name,
					Host:        client.host,
					InviteID:    client.inviteID,
					IP:          client.ip,
					UA:          client.ua,
					ConnectedAt: client.connectedAt,
				})
			}
			reply <- list
		}
	}
}

func (h *Hub) Send(payload []byte) {
	h.broadcast <- payload
}

func (h *Hub) Count() int {
	reply := make(chan int, 1)
	h.countReq <- reply
	return <-reply
}

// Devices 返回当前在线连接的快照（供主机页面显示）。
func (h *Hub) Devices() []ClientInfo {
	reply := make(chan []ClientInfo, 1)
	h.devicesReq <- reply
	return <-reply
}

// UsedNumbers 返回当前在线设备已经占用的昵称数字，排除指定设备自己的连接。
func (h *Hub) UsedNumbers(excludeClientID string) map[int]bool {
	reply := make(chan map[int]bool, 1)
	h.numbersReq <- numbersRequest{exclude: excludeClientID, reply: reply}
	return <-reply
}

func (h *Hub) deliver(payload []byte) {
	for client := range h.clients {
		select {
		case client.send <- payload:
		default: // 发送缓冲已满，视为掉线
			delete(h.clients, client)
			close(client.send)
		}
	}
}

func (h *Hub) pushPeers() {
	payload, err := json.Marshal(Event{Type: "peers", Peers: len(h.clients)})
	if err != nil {
		return
	}
	h.deliver(payload)
}

// writePump 负责向浏览器写数据并保持心跳。
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case payload, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
