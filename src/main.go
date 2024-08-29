package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		allowedOrigins := map[string]bool{
			"http://localhost:8080":       true,
			"http://internetgradient.com": true,
		}
		return allowedOrigins[origin]
	},
}

var (
	clients               = make(map[*websocket.Conn]bool)
	broadcast             = make(chan []byte)
	gradients             = []uint16{}
	locations             = make(map[string]int)
	max                   = 100
	maxG                  = 5
	globalCounter  uint32 = 0
	clientsMutex   sync.Mutex
	gradientsMutex sync.Mutex
	rateLimit      = 8
	clientLastTime = make(map[*websocket.Conn]time.Time)
	clientCount    = make(map[*websocket.Conn]int)
)

func main() {
	myhttp := http.NewServeMux()
	fs := http.FileServer(http.Dir("./views/"))
	myhttp.Handle("/", http.StripPrefix("", fs))

	myhttp.HandleFunc("/socket", handleConnections)

	go handleMessages()

	http.ListenAndServe(":8080", myhttp)
}

// websocket stuff
func handleConnections(w http.ResponseWriter, r *http.Request) {
	con, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade fail", err)
		return
	}
	defer con.Close()

	clientsMutex.Lock()
	clients[con] = true
	clientLastTime[con] = time.Now()
	clientCount[con] = 0
	clientsMutex.Unlock()

	gradientsMutex.Lock()
	initialData := make([]byte, len(gradients)*2+4)
	binary.LittleEndian.PutUint32(initialData[0:], globalCounter)
	for i, gradient := range gradients {
		binary.LittleEndian.PutUint16(initialData[(i*2)+4:], gradient)
	}
	gradientsMutex.Unlock()

	err = con.WriteMessage(websocket.BinaryMessage, initialData)
	if err != nil {
		log.Println("data send fail", err)
		return
	}

	for {
		_, msg, err := con.ReadMessage()
		if err != nil {
			log.Println("socket read error", err)
			clientsMutex.Lock()
			delete(clients, con)
			delete(clientLastTime, con)
			delete(clientCount, con)
			clientsMutex.Unlock()
			break
		}
		broadcast <- msg
	}
}

// handle gradients
func handleMessages() {
	for {
		msg := <-broadcast

		clientsMutex.Lock()

		if globalCounter == ^uint32(0) {
			clientsMutex.Unlock()
			continue
		}

		var gradient [2]uint16
		buf := bytes.NewReader(msg)
		err := binary.Read(buf, binary.LittleEndian, &gradient)
		if err != nil {
			log.Println("parse fail", err)
			continue
		}

		x := gradient[0]
		y := gradient[1]
		key := createKey(int(x), int(y))

		gradientsMutex.Lock()
		if count := locations[key]; count < maxG {
			if len(gradients) < max*2 {
				gradients = append(gradients, x, y)
			} else {
				gradients = append(gradients[2:], x, y)
			}
			locations[key]++
			globalCounter++

			if globalCounter == ^uint32(0) {
				gradientsMutex.Unlock()
				message := "It's over."
				for client := range clients {
					err := client.WriteMessage(websocket.TextMessage, []byte(message))
					if err != nil {
						log.Printf("socket error %v", err)
						client.Close()
						delete(clients, client)
					}
				}
				clientsMutex.Unlock()
				continue
			}
		} else {
			log.Println("max gradients @", x, y)
			gradientsMutex.Unlock()
			clientsMutex.Unlock()
			continue
		}
		gradientsMutex.Unlock()

		response := make([]byte, 8)
		binary.LittleEndian.PutUint32(response[0:], globalCounter)
		binary.LittleEndian.PutUint16(response[4:], x)
		binary.LittleEndian.PutUint16(response[6:], y)

		for client := range clients {
			if !rateLimitClient(client) {
				log.Println("rate limit")
				continue
			}

			err := client.WriteMessage(websocket.BinaryMessage, response)
			if err != nil {
				log.Printf("socket error %v", err)
				client.Close()
				delete(clients, client)
				delete(clientLastTime, client)
				delete(clientCount, client)
			}
		}
		clientsMutex.Unlock()
	}
}

func rateLimitClient(client *websocket.Conn) bool {
	now := time.Now()
	lastTime := clientLastTime[client]

	if now.Sub(lastTime) >= time.Second {
		clientLastTime[client] = now
		clientCount[client] = 1
		return true
	}

	if clientCount[client] < rateLimit {
		clientCount[client]++
		return true
	}

	return false
}

func createKey(x, y int) string {
	return fmt.Sprintf("%d,%d", x, y)
}
