//go:build ignore

package main

import (
	"fmt"
	"github.com/golang-jwt/jwt/v5"
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
	fmt.Println(tokenString)
}
