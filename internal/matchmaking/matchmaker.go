package matchmaking

import (
	"container/list"
	"encoding/json"
	"log"
	"rps_matchmaking/internal/utils"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const PendingTimeout = 30 * time.Second
const PingTimeout = 1 * time.Second

type Status int

const (
	Queued Status = iota
	Pending
	Accepted
	Cancelled
)

type MessageType int

const (
	CancelQueue         MessageType = 0
	PopQueue            MessageType = 1
	AcceptQueue         MessageType = 2
	AcceptQueueResponse MessageType = 3
)

type BaseMessage struct {
	Headers interface{} `json:"HEADERS"`
	Type    string      `json:"type"`
}

type CancelQueueMessage struct {
	BaseMessage
	PlayerID string `json:"player_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type PopQueueMessage struct {
	BaseMessage
	MatchID string `json:"match_id,omitempty"`
	Timeout int64  `json:"timeout,omitempty"`
}

type AcceptQueueMessage struct {
	BaseMessage
	PlayerID string `json:"player_id,omitempty"`
	MatchID  string `json:"match_id"`
}

type ServerInfoMessage struct {
	BaseMessage
	MatchID    string `json:"match_id"`
	ServerIP   string `json:"server_ip"`
	ServerPort int    `json:"server_port"`
}

type Player struct {
	id     string
	status Status
	conn   *websocket.Conn
	mq     *MatchmakingQueue
	send   chan []byte
	timer  *time.Ticker
	time   time.Time
}

type PendingMatch struct {
	player1Ready bool
	player2Ready bool
	player1      *Player
	player2      *Player
	timeout      time.Time
}

type MatchmakingQueue struct {
	queue   *list.List
	hash    map[string]*list.Element
	pending map[string]*PendingMatch
	cancel  chan *Player
	mutex   *sync.Mutex
}

func (p *Player) read() {
	defer func() {
		p.Cancel()
	}()

	for {
		_, msg, err := p.conn.ReadMessage()
		if err != nil {
			log.Println("read err:", err)
			break
		}

		var baseMsg BaseMessage
		if ok := utils.UnmarshalMessage(msg, &baseMsg); !ok {
			continue
		}
		msgTypeInt, err := strconv.Atoi(baseMsg.Type)
		if err != nil {
			log.Println("Error parsing message type:", err)
			continue
		}
		msgType := MessageType(msgTypeInt)

		switch msgType {
		case CancelQueue:
			p.mq.Remove(p.id)
			return
		case AcceptQueue:
			var acceptMsg AcceptQueueMessage
			if ok := utils.UnmarshalMessage(msg, &acceptMsg); !ok {
				break
			}
			log.Println("AcceptingQueue")

		default:
			log.Println("Unknown message type:", baseMsg.Type)
		}
	}
}

func (p *Player) write() {
	defer func() {
		p.Cancel()
	}()

	for {
		select {
		case msg, ok := <-p.send:
			if !ok {
				p.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			err := p.conn.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				log.Println("Error writing msg:", err)
				return
			}
		case <-p.timer.C:
			if err := p.conn.WriteMessage(websocket.TextMessage, []byte(renderQueueTimer(p))); err != nil {
				log.Println("Error writing Timer:", err)
				return
			}
		}
	}
}

func (p *Player) Cancel() {
	p.timer.Stop()
	p.mq.cancel <- p
}

// TODO: Implement pending and timeout and accept queue

func (mq *MatchmakingQueue) Enqueue(pid string, conn *websocket.Conn) *Player {
	mq.mutex.Lock()
	p := &Player{
		id:     pid,
		status: Queued,
		conn:   conn,
		mq:     mq,
		send:   make(chan []byte),
		time:   time.Now(),
		timer:  time.NewTicker(PingTimeout),
	}
	node := mq.queue.PushBack(p)
	mq.hash[pid] = node
	mq.mutex.Unlock()

	go p.read()
	go p.write()
	return p
}

func (mq *MatchmakingQueue) Dequeue() *Player {
	if mq.queue.Len() == 0 {
		return nil
	}

	mq.mutex.Lock()
	player := mq.queue.Remove(mq.queue.Front()).(*Player)
	delete(mq.hash, player.id)
	mq.mutex.Unlock()
	return player
}

func (mq *MatchmakingQueue) Remove(pid string) *Player {
	mq.mutex.Lock()
	defer mq.mutex.Unlock()
	node, ok := mq.hash[pid]
	if !ok {
		return nil
	}
	player := mq.queue.Remove(node).(*Player)
	delete(mq.hash, pid)
	player.status = Cancelled
	log.Println("Player", pid, "cancelled and removed")
	player.send <- []byte(renderRedirect("/profile"))
	return player
}

func (mq *MatchmakingQueue) PushFront(player *Player) {
	mq.mutex.Lock()
	node := mq.queue.PushFront(player)
	mq.hash[player.id] = node
	mq.mutex.Unlock()
	player.status = Queued
}

func (mq *MatchmakingQueue) Run() {
	mq.mutex.Lock()
	defer mq.mutex.Unlock()

	if mq.queue.Len() < 2 {
		return
	}

	player1 := mq.Dequeue()
	player2 := mq.Dequeue()
	player1.status = Pending
	player2.status = Pending

	// TODO: Get match ID from Game Service
	matchID := uuid.New().String()
	pqm := PopQueueMessage{
		BaseMessage: BaseMessage{Type: strconv.Itoa(int(PopQueue))},
		MatchID:     matchID,
		Timeout:     time.Now().Add(PendingTimeout).Unix(),
	}
	msg, err := json.Marshal(pqm)
	if err != nil {
		log.Println("Error marshalling message:", err)
		return
	}
	log.Println("Match Found:", msg)

	// TODO: Send match ID to Game Service
	html := renderQueuePop(pqm)
	buf := []byte(html)
	player1.send <- buf
	player2.send <- buf

	// TODO: handle pending and timeout
}

func (mq *MatchmakingQueue) Tick() {
	go func() {
		for {
			select {
			case p := <-mq.cancel:
				mq.Remove(p.id)
			}
		}
	}()
}

func NewMatchmakingQueue() *MatchmakingQueue {
	return &MatchmakingQueue{
		queue:   list.New(),
		hash:    make(map[string]*list.Element),
		pending: make(map[string]*PendingMatch),
		cancel:  make(chan *Player),
		mutex:   &sync.Mutex{},
	}
}

func renderRedirect(url string) string {
	return `<div id="queue-data" hx-swap-oob="true" hx-get="` + url + `" hx-trigger="load" hx-target="body" hx-swap="outerHTML"></div>`
}

func renderQueueTimer(p *Player) string {
	qTime := time.Now().Sub(p.time)
	elapsed := strconv.Itoa(int(qTime.Seconds()))
	return `<span id="queue-timer" hx-swap-oob="true" class="text-lg font-bold">` + elapsed + ` s</span>`
}

func renderQueuePop(data PopQueueMessage) string {
	return `<div id="queue-data" hx-swap-oob="true">
			<h1>Match Found</h1>
			<p>Match ID: ` + data.MatchID + `</p>
			<p>Timeout: ` + time.Unix(data.Timeout, 0).String() + `</p>
		</div>`
}
