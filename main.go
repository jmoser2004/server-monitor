// Command server-monitor serves a live system dashboard.
//
// It exposes two things:
//   - GET /        the static dashboard (embedded), built for a Raspberry Pi
//     touchscreen viewer.
//   - GET /ws      a WebSocket that streams a JSON system Snapshot every tick.
//
// The server runs on the machine being monitored; the Pi simply points its
// browser at http://<host>:<port>/.
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"servermonitor/sysinfo"
)

//go:embed static
var staticFiles embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // LAN dashboard, any origin
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	interval := flag.Duration("interval", 2*time.Second, "snapshot interval")
	flag.Parse()

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("embed: %v", err)
	}
	http.Handle("/", http.FileServer(http.FS(sub)))
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWS(w, r, *interval)
	})

	log.Printf("server-monitor listening on %s (interval %s)", *addr, *interval)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func serveWS(w http.ResponseWriter, r *http.Request, interval time.Duration) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}
	defer conn.Close()
	log.Printf("client connected: %s", r.RemoteAddr)

	// Drain reads so we notice disconnects (and respond to control frames).
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// cpu.Percent inside Snapshot blocks ~500ms to sample; keep it well under
	// the tick interval so the stream stays at roughly the requested cadence.
	sample := 500 * time.Millisecond
	if interval < sample*2 {
		sample = interval / 2
	}

	collector := sysinfo.NewCollector()

	// Send one immediately so the dashboard isn't blank on connect.
	if err := conn.WriteJSON(collector.Snapshot(sample)); err != nil {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			log.Printf("client disconnected: %s", r.RemoteAddr)
			return
		case <-ticker.C:
			snap := collector.Snapshot(sample)
			if err := conn.WriteJSON(snap); err != nil {
				log.Printf("write: %v", err)
				return
			}
		}
	}
}
