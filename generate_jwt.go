//go:build ignore

package main

import (
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"os"
	"time"
)

func main() {
	secretStr := os.Getenv("NHB_RPC_JWT_SECRET")
	if secretStr == "" {
		fmt.Println("Error: NHB_RPC_JWT_SECRET must be set in the environment before running this tool.")
		os.Exit(1)
	}
	secret := []byte(secretStr)
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
	fmt.Println(tokenString)
}
