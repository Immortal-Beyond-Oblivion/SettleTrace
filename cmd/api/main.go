// Command api starts the local SettleTrace operator API.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/api"
)

// main starts the API on the configured local address.
func main() {
	address := os.Getenv("API_ADDR")
	if address == "" {
		address = ":8080"
	}
	log.Printf("starting api on %s", address)
	if err := http.ListenAndServe(address, api.Server{}.Routes()); err != nil {
		log.Fatal(err)
	}
}
