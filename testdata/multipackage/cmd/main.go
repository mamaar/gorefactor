package main

import (
	"fmt"
	"testdata/multipackage/internal/common"
	"testdata/multipackage/pkg/client"
	"testdata/multipackage/pkg/server"
)

func setupConfig() {
	cfg := common.Config{
		Host: "localhost",
		Port: 8080,
	}
}
func main() {
	fmt.Println("Starting multipackage application")

	setupConfig()

	srv := server.New(cfg)
	client := client.New(cfg.Host, cfg.Port)

	fmt.Printf("Server: %v\n", srv)
	fmt.Printf("Client: %v\n", client)
}
