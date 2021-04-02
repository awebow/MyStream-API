package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

func (app *App) InitWebsocket() {
	app.connects = make(chan *WebsocketClient)
	app.disconnects = make(chan *WebsocketClient)
	app.websockets = make(map[uint64]*WebsocketClient)
	app.rooms = make(map[string]*Room)
	app.roomsLock = new(sync.RWMutex)
	app.newRooms = make(chan *Room)
	go app.WebsocketLoop()
}

type WebsocketClient struct {
	ID     uint64
	UserID string
	conn   *websocket.Conn
	sender chan []byte
	rooms  map[string]*Room
}

func NewWebsocketClient() *WebsocketClient {
	return &WebsocketClient{sender: make(chan []byte), rooms: make(map[string]*Room)}
}

func (client *WebsocketClient) ReadLoop(app *App) {
	client.conn.SetReadDeadline(time.Now().Add(time.Duration(app.Config.Websocket.PongTimeout) * time.Millisecond))
	client.conn.SetPongHandler(func(string) error {
		client.conn.SetReadDeadline(time.Now().Add(time.Duration(app.Config.Websocket.PongTimeout) * time.Millisecond))
		return nil
	})

	defer client.Disconnected(app)

	for {
		t, data, err := client.conn.ReadMessage()
		if err != nil {
			return
		}

		if t == websocket.TextMessage {
			parsed := map[string]interface{}{}
			if json.Unmarshal(data, &parsed) != nil {
				continue
			}

			if cmd, ok := parsed["cmd"]; ok && cmd == "join" {
				if path, ok := parsed["data"].(string); !ok {
					continue
				} else if s := strings.Split(path, "/"); s[0] == "video" && s[2] == "encode" {
					app.WatchVideoEncode(client, s[1])
				}
			}
		}
	}
}

func (client *WebsocketClient) WriteLoop(app *App) {
	pingTicker := time.NewTicker(time.Duration(app.Config.Websocket.PingInterval) * time.Millisecond)
	for {
		select {
		case data, ok := <-client.sender:
			if !ok {
				return
			}

			if err := client.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}

		case <-pingTicker.C:
			if err := client.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (client *WebsocketClient) Disconnected(app *App) {
	close(client.sender)

	for _, r := range client.rooms {
		r.leaves <- client
	}

	app.disconnects <- client
}

func (client *WebsocketClient) Emit(event string, data interface{}) error {
	blob, err := NewEmitMessage(event, data)
	if err != nil {
		return err
	}

	client.sender <- blob
	return nil
}

func (app *App) ServeWebsocket(c echo.Context) error {
	conn, err := app.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}

	client := NewWebsocketClient()
	client.UserID, _ = app.AuthUser(c.QueryParam("authorization"))
	client.conn = conn
	app.connects <- client
	return nil
}

func (app *App) WebsocketLoop() {
	count := uint64(0)
	for {
		select {
		case client := <-app.connects:
			client.ID = count
			app.websockets[count] = client

			go client.WriteLoop(app)
			go client.ReadLoop(app)

			count++

		case client := <-app.disconnects:
			delete(app.websockets, client.ID)

		case room := <-app.newRooms:
			app.roomsLock.Lock()
			app.rooms[room.Path] = room
			app.roomsLock.Unlock()

			go app.rooms[room.Path].Loop()
		}
	}
}

func (app *App) GetRoom(path string) *Room {
	app.roomsLock.RLock()
	defer app.roomsLock.RUnlock()

	return app.rooms[path]
}

func (app *App) EmitToRoom(path string, event string, data interface{}) error {
	if !app.Config.Websocket.Enabled {
		return nil
	}

	room := app.GetRoom(path)
	if room == nil {
		return nil
	}

	blob, err := NewEmitMessage(event, data)
	if err != nil {
		return err
	}

	room.broadcasts <- blob
	return nil
}

type Room struct {
	Path       string
	clients    map[uint64]*WebsocketClient
	joins      chan *WebsocketClient
	leaves     chan *WebsocketClient
	broadcasts chan []byte
}

func (room *Room) Loop() {
	for {
		select {
		case client, ok := <-room.joins:
			if !ok {
				return
			}

			room.clients[client.ID] = client

		case client, ok := <-room.leaves:
			if !ok {
				return
			}

			delete(room.clients, client.ID)

		case data := <-room.broadcasts:
			for _, c := range room.clients {
				c.sender <- data
			}
		}
	}
}

func NewEmitMessage(event string, data interface{}) ([]byte, error) {
	return json.Marshal(&struct {
		Command string      `json:"cmd"`
		Event   string      `json:"event"`
		Data    interface{} `json:"data"`
	}{"emit", event, data})
}

func (app *App) WatchVideoEncode(client *WebsocketClient, videoID string) {
	video, err := app.SelectVideo(videoID)
	if err != nil {
		return
	}

	ownerID, err := app.SelectChannelOwnerID(video.ChannelID)
	if err != nil {
		return
	}

	if client.UserID != ownerID {
		return
	}

	if video.Status != StatusEncoding {
		client.Emit("encoded", video)
		return
	}

	path := fmt.Sprintf("video/%s/encode", videoID)
	room, ok := app.rooms[path]
	if !ok {
		room = &Room{path, make(map[uint64]*WebsocketClient), make(chan *WebsocketClient), make(chan *WebsocketClient), make(chan []byte)}
		app.newRooms <- room
	}

	room.joins <- client
	client.rooms[path] = room
}
