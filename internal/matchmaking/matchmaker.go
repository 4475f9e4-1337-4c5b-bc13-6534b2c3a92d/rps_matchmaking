package matchmaking

import (
	"container/list"
	"encoding/json"
	"log"
	"rps_matchmaking/internal/utils"
	"strconv"
	"sync"
	"time"

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

type PendingMatch struct {
	player1Ready bool
	player2Ready bool
	player1      *Player
	player2      *Player
	timeout      time.Time
}

type Player struct {
	id     string
	mode   string
	bestof int
	status Status
	conn   *websocket.Conn
	mq     *MatchmakingQueue
	send   chan []byte
	timer  *time.Ticker
	time   time.Time
}

type MatchmakingQueue struct {
	token   string
	queue   *list.List
	hash    map[string]*list.Element
	pending map[string]*PendingMatch
	join    chan *Player
	cancel  chan *Player
	mutex   *sync.RWMutex
}

func (p *Player) read() {
	defer func() {
		p.mq.cancel <- p
	}()

	for {
		_, msg, err := p.conn.ReadMessage()
		if err != nil {
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
		close(p.send)
		p.timer.Stop()
		p.mq.cancel <- p
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
				return
			}
		case <-p.timer.C:
			if err := p.conn.WriteMessage(websocket.TextMessage, []byte(renderQueueTimer(p))); err != nil {
				return
			}
		}
	}
}

// TODO: Implement pending and timeout and accept queue

func (mq *MatchmakingQueue) Enqueue(pid, mode string, bestof int, conn *websocket.Conn) *Player {
	mq.mutex.Lock()
	p := &Player{
		id:     pid,
		mode:   mode,
		bestof: bestof,
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

	go func() {
		mq.join <- p
	}()
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
	log.Println("player", pid, "removed")
	player.send <- []byte(renderRedirect("/profile/menu"))
	return player
}

func (mq *MatchmakingQueue) PushFront(player *Player) {
	mq.mutex.Lock()
	node := mq.queue.PushFront(player)
	mq.hash[player.id] = node
	mq.mutex.Unlock()
	player.status = Queued
}

func (mq *MatchmakingQueue) run() {

	log.Println("Running Matchmaking")
	mq.mutex.RLock()
	if mq.queue.Len() < 2 {
		mq.mutex.RUnlock()
		return
	}
	mq.mutex.RUnlock()

	player1 := mq.Dequeue()
	player2 := mq.Dequeue()
	player1.status = Pending
	player2.status = Pending

	settings := utils.GameSettings{
		GameMode:  "rps",
		Type:      player1.mode,
		BestOf:    max(player1.bestof, player2.bestof),
		PlayerOne: player1.id,
		PlayerTwo: player2.id,
	}
	matchID, err := utils.GetNewServerID(settings)
	if err != nil {
		log.Println("Error getting server ID:", err)
		return
	}

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
	log.Println("Match Found:", string(msg))

	html := renderQueuePop(pqm)
	buf := []byte(html)
	player1.send <- buf
	player2.send <- buf

	// TODO: handle pending and timeout
}

func (mq *MatchmakingQueue) tick() {
	for {
		select {
		case p := <-mq.cancel:
			p.conn.Close()
		case <-mq.join:
			mq.run()
		}
	}
}

func NewMatchmakingQueue() (*MatchmakingQueue, error) {
	token, err := utils.CreateJWT("matchmaker")
	if err != nil {
		log.Fatal("Error creating JWT:", err)
		return nil, err
	}
	q := &MatchmakingQueue{
		token:   token,
		queue:   list.New(),
		hash:    make(map[string]*list.Element),
		pending: make(map[string]*PendingMatch),
		join:    make(chan *Player),
		cancel:  make(chan *Player),
		mutex:   &sync.RWMutex{},
	}
	go q.tick()
	return q, nil
}

func renderRedirect(url string) string {
	return `<div id="queue-data" hx-swap-oob="true" hx-get="` + url + `" hx-trigger="load" hx-target="#game-menu" hx-swap="outerHTML"></div>`
}

func renderQueueTimer(p *Player) string {
	qTime := time.Now().Sub(p.time)
	elapsed := strconv.Itoa(int(qTime.Seconds()))
	return `<span id="queue-timer" hx-swap-oob="true" class="text-lg font-bold">` + elapsed + ` s</span>`
}

func renderQueuePop(data PopQueueMessage) string {
	return `<div id="queue-data" hx-swap-oob="true"	hx-get="/game/` + data.MatchID + `" hx-target="#game-menu" hx-trigger="load" hx-swap="innerHTML" </div>`
}
