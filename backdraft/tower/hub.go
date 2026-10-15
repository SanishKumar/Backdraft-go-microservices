package tower

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/analysis"
	"github.com/gorilla/websocket"
)

// message is everything the UI receives over its WebSocket.
type message struct {
	Type     string           `json:"type"`
	Tick     *Snapshot        `json:"tick,omitempty"`
	Events   []Event          `json:"events,omitempty"`
	State    *State           `json:"state,omitempty"`
	Findings *analysis.Report `json:"findings,omitempty"`
	History  []Snapshot       `json:"history,omitempty"`
}

// hub fans messages out to connected UIs. A client that cannot keep up
// loses ticks rather than slowing everyone else down.
type hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func newHub() *hub { return &hub{clients: map[chan []byte]struct{}{}} }

func (h *hub) broadcast(m message) {
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c <- raw:
		default:
		}
	}
}

var upgrader = websocket.Upgrader{
	// The API changes live configuration, so only local pages may open
	// the stream (the Vite dev server included).
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.EqualFold(u.Host, r.Host)
	},
}

func (t *Tower) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ch := make(chan []byte, 64)

	t.historyMu.Lock()
	history := append([]Snapshot(nil), t.history...)
	t.historyMu.Unlock()
	st := t.state()
	findings := t.currentFindings()
	hello, _ := json.Marshal(message{Type: "hello", State: &st, Findings: &findings, History: history})
	if err := conn.WriteMessage(websocket.TextMessage, hello); err != nil {
		return
	}

	t.hub.mu.Lock()
	t.hub.clients[ch] = struct{}{}
	t.hub.mu.Unlock()
	defer func() {
		t.hub.mu.Lock()
		delete(t.hub.clients, ch)
		t.hub.mu.Unlock()
	}()

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-closed:
			return
		case raw := <-ch:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				return
			}
		case <-ping.C:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
