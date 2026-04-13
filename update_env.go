//go:build ignore

package main

import (
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"os"
	"strings"
	"time"
)

func main() {
	secret := []byte("nhb-master-admin-secret-2026!")
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "nhb-rpc",
		"aud": []string{"wallets"},
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour * 24 * 365 * 10).Unix(),
	})
	tokenString, err := token.SignedString(secret)
	if err != nil {
		panic(err)
	}

	envPath := "../nhbportal/.env"
	data, err := os.ReadFile(envPath)
	if err != nil {
		panic(err)
	}

	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "NHB_CHAIN_ID=") {
			lines[i] = "NHB_CHAIN_ID=5756470643927894962"
		} else if strings.HasPrefix(line, "NODE_RPC_TOKEN=") {
			lines[i] = "NODE_RPC_TOKEN=" + tokenString
		} else if strings.HasPrefix(line, "L1_NODE_RPC_TOKEN=") {
			lines[i] = "L1_NODE_RPC_TOKEN=" + tokenString
		}
	}

	err = os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0644)
	if err != nil {
		panic(err)
	}

	fmt.Println("Successfully updated .env with new Chain ID and JWT token!")
}
