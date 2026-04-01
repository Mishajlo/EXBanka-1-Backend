package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	kafkago "github.com/segmentio/kafka-go"

	authpb "github.com/exbanka/contract/authpb"
	kafkamsg "github.com/exbanka/contract/kafka"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type WebSocketHandler struct {
	authClient  authpb.AuthServiceClient
	connections map[int64]*wsConnection
	mu          sync.RWMutex
}

type wsConnection struct {
	conn     *websocket.Conn
	deviceID string
	lastPong time.Time
}

func NewWebSocketHandler(authClient authpb.AuthServiceClient) *WebSocketHandler {
	return &WebSocketHandler{
		authClient:  authClient,
		connections: make(map[int64]*wsConnection),
	}
}

// HandleConnect upgrades HTTP to WebSocket, validates mobile JWT + device_id.
func (h *WebSocketHandler) HandleConnect(c *gin.Context) {
	// Extract and validate token
	token := c.Query("token")
	if token == "" {
		header := c.GetHeader("Authorization")
		if header != "" {
			parts := strings.SplitN(header, " ", 2)
			if len(parts) == 2 && parts[0] == "Bearer" {
				token = parts[1]
			}
		}
	}
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
		return
	}

	resp, err := h.authClient.ValidateToken(c.Request.Context(), &authpb.ValidateTokenRequest{Token: token})
	if err != nil || !resp.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}
	if resp.DeviceType != "mobile" {
		c.JSON(http.StatusForbidden, gin.H{"error": "mobile token required"})
		return
	}

	deviceID := c.GetHeader("X-Device-ID")
	if deviceID == "" {
		deviceID = c.Query("device_id")
	}
	if deviceID != resp.DeviceId {
		c.JSON(http.StatusForbidden, gin.H{"error": "device ID mismatch"})
		return
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("websocket upgrade error: %v", err)
		return
	}

	userID := resp.UserId

	// Replace existing connection for this user
	h.mu.Lock()
	if existing, ok := h.connections[userID]; ok {
		existing.conn.Close()
	}
	h.connections[userID] = &wsConnection{
		conn:     conn,
		deviceID: deviceID,
		lastPong: time.Now(),
	}
	h.mu.Unlock()

	log.Printf("websocket: user %d connected (device %s)", userID, deviceID)

	// Handle pong responses
	conn.SetPongHandler(func(string) error {
		h.mu.Lock()
		if ws, ok := h.connections[userID]; ok {
			ws.lastPong = time.Now()
		}
		h.mu.Unlock()
		return nil
	})

	// Start ping loop
	go h.pingLoop(userID, conn)

	// Read loop (to detect disconnection)
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}

	// Cleanup on disconnect
	h.mu.Lock()
	delete(h.connections, userID)
	h.mu.Unlock()
	conn.Close()
	log.Printf("websocket: user %d disconnected", userID)
}

func (h *WebSocketHandler) pingLoop(userID int64, conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.RLock()
		ws, ok := h.connections[userID]
		h.mu.RUnlock()
		if !ok {
			return
		}
		// Check for dead connection (no pong in 60s)
		if time.Since(ws.lastPong) > 60*time.Second {
			h.mu.Lock()
			delete(h.connections, userID)
			h.mu.Unlock()
			conn.Close()
			log.Printf("websocket: user %d timed out (no pong)", userID)
			return
		}
		if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
			return
		}
	}
}

// PushToUser sends a message to a connected user's WebSocket.
func (h *WebSocketHandler) PushToUser(userID int64, message interface{}) {
	h.mu.RLock()
	ws, ok := h.connections[userID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	data, err := json.Marshal(message)
	if err != nil {
		return
	}
	if err := ws.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("websocket: push to user %d failed: %v", userID, err)
	}
}

// StartKafkaConsumer listens for mobile-push events and routes to WebSocket connections.
func (h *WebSocketHandler) StartKafkaConsumer(ctx context.Context, brokers string) {
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:  strings.Split(brokers, ","),
		Topic:    kafkamsg.TopicMobilePush,
		GroupID:  "api-gateway-ws",
		MinBytes: 1,
		MaxBytes: 10e6,
	})

	go func() {
		defer reader.Close()
		log.Println("websocket kafka consumer started (topic: notification.mobile-push)")
		for {
			msg, err := reader.ReadMessage(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("websocket kafka consumer: read error: %v", err)
				continue
			}

			var pushMsg kafkamsg.MobilePushMessage
			if err := json.Unmarshal(msg.Value, &pushMsg); err != nil {
				log.Printf("websocket kafka consumer: unmarshal error: %v", err)
				continue
			}

			h.PushToUser(int64(pushMsg.UserID), pushMsg)
		}
	}()
}
