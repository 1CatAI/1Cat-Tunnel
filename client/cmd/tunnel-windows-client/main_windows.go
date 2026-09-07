//go:build windows

package main

import (
	"fmt"
	"os"

	"tunnel/internal/winclient"
)

func main() {
	if err := winclient.Main(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "1CatTunnel 错误：%v\n", err)
		os.Exit(1)
	}
}
