package matchmaking

import (
	"container/list"
	"encoding/json"
	"log"
	"rps_matchmaking/internal/utils"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const PendingTimeout = 30 * time.Second
const PingTimeout = 10 * time.Second

type Status int

const (
	Queued Status = iota
	Pending
	Accepted
	Cancelled
)

type MessageType int

const (
	CancelQueue MessageType = iota
	PopQueue
	AcceptQueue
	AcceptQueueResponse
)

type BaseMessage struct {
	Type MessageType `json:"type"`
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
		p.mq.cancel <- p
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

		switch baseMsg.Type {
		case CancelQueue:
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
	ticker := time.NewTicker(PingTimeout)
	defer func() {
		ticker.Stop()
		p.mq.cancel <- p
	}()

	for {
		select {
		case msg, ok := <-p.send:
			if !ok {
				p.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			p.conn.WriteMessage(websocket.TextMessage, msg)

		case <-ticker.C:
			if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// TODO: Implement pending and timeout and accept queue
// TODO: Implement cancel and decline queue

func (mq *MatchmakingQueue) Enqueue(pid string, conn *websocket.Conn) *Player {
	mq.mutex.Lock()
	p := &Player{
		id:     pid,
		status: Queued,
		conn:   conn,
		mq:     mq,
		send:   make(chan []byte),
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
	node, ok := mq.hash[pid]
	if !ok {
		mq.mutex.Unlock()
		return nil
	}
	player := mq.queue.Remove(node).(*Player)
	delete(mq.hash, pid)
	mq.mutex.Unlock()
	player.status = Cancelled
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
		BaseMessage: BaseMessage{Type: PopQueue},
		MatchID:     matchID,
		Timeout:     time.Now().Add(PendingTimeout).Unix(),
	}
	msg, err := json.Marshal(pqm)
	if err != nil {
		log.Println("Error marshalling message:", err)
		return
	}
	player1.send <- msg
	player2.send <- msg

	// TODO: handle pending and timeout
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
