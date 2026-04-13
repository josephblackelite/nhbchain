package main

import (
	"log"

	"nhbchain/services/payoutd"
)

func main() {
	if err := payoutd.Main(); err != nil {
		log.Fatalf("payoutd: %v", err)
	}
}
