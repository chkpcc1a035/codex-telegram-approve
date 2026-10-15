package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx)
	case "run":
		err = run(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr)
	case "pair":
		err = pair(ctx)
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`codex-telegram-approve
  serve   Run the Telegram approval backend (usually in Docker)
  pair    Print Telegram user/chat IDs from incoming messages (backend must be stopped)
  run     Start a local terminal Codex session; use run -h for options`)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
