package main

import (
	"log"

	oracle "nhbchain/services/oracle-attesterd"
)

func main() {
	if err := oracle.Main(); err != nil {
		log.Fatalf("oracle-attesterd: %v", err)
	}
}
