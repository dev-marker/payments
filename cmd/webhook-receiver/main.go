// Command webhook-receiver is the other side of our webhooks: a small server,
// like a merchant would run, that verifies every delivery, and handles each
// event once.
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/fiston-user/payments/internal/webhook/signature"
)

func main() {
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret == "" {
		log.Fatal("WEBHOOK_SECRET is required: the secret you got with your endpoint")
	}
	// The events we've handled, by ID. In memory, for an example: a real
	// receiver keeps them in its database, with a unique key.
	var seen sync.Map

	http.HandleFunc("POST /webhooks", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		// First, before decoding anything, or trusting a single byte of it.
		if err := signature.Verify(r.Header, secret, body, time.Now()); err != nil {
			log.Printf("rejected: %v", err)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		id := r.Header.Get(signature.HeaderID)
		if _, dup := seen.LoadOrStore(id, true); dup {
			log.Printf("duplicate %s: already handled, skipping", id)
			w.WriteHeader(http.StatusNoContent) // a success: don't send it again
			return
		}
		var evt struct {
			Type string `json:"type"`
			Data struct {
				Amount   int64  `json:"amount"`
				Currency string `json:"currency"`
			} `json:"data"`
		}
		json.Unmarshal(body, &evt) // verified: it's ours, and well formed
		log.Printf("verified %s: %s, %d %s", id, evt.Type, evt.Data.Amount, evt.Data.Currency)
		w.WriteHeader(http.StatusNoContent) // at once: real work goes to a queue
	})

	log.Print("webhook receiver listening on :8090")
	srv := &http.Server{Addr: ":8090", ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
