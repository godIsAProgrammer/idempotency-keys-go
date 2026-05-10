package main

import (
	"log"
	"net/http"
	"os"

	"idempotency-keys-go/idem"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8803"
	}
	server := idem.NewServer(nil)
	log.Printf("idempotency-keys-go listening on %s", port)
	if err := http.ListenAndServe("0.0.0.0:"+port, server.Handler()); err != nil {
		log.Fatal(err)
	}
}
