package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv); err != nil {
		log.Fatal(err)
	}
}

type runOptions struct {
	port      int
	storePath string
}

func parseRunOptions(args []string) (runOptions, error) {
	fs := flag.NewFlagSet("telegram-bot", flag.ContinueOnError)
	port := fs.Int("port", 8080, "loopback HTTP listener port")
	storePath := fs.String("store", "", "durable state file")
	if err := fs.Parse(args); err != nil {
		return runOptions{}, err
	}
	if *port < 1 || *port > 65535 {
		return runOptions{}, errors.New("invalid --port")
	}
	return runOptions{port: *port, storePath: *storePath}, nil
}
func run(ctx context.Context, args []string, getenv func(string) string) error {
	options, err := parseRunOptions(args)
	if err != nil {
		return err
	}
	token := getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	allowed, err := ParseAllowedUsers(getenv("TELEGRAM_ALLOWED_USER_IDS"))
	if err != nil {
		return err
	}
	if options.storePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		options.storePath = filepath.Join(home, ".afm", "telegram-bot", "state.json")
	}
	store, err := OpenStore(options.storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	registry := NewRegistry(store)
	service := NewService(registry, NewTelegramTransport(token), allowed)
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(options.port)))
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: registry.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	log.Printf("Telegram bot listener on 127.0.0.1:%d", options.port)
	syncTicker := time.NewTicker(15 * time.Second)
	defer syncTicker.Stop()
	if err := service.SyncOnce(ctx); err != nil {
		log.Printf("AFM sync: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
			}
			return nil
		case err := <-serverErr:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("HTTP listener: %w", err)
		case <-syncTicker.C:
			if err := service.SyncOnce(ctx); err != nil {
				log.Printf("AFM sync: %v", err)
			}
		default:
			pollCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
			err := service.PollOnce(pollCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("Telegram poll: %v", err)
				select {
				case <-time.After(3 * time.Second):
				case <-ctx.Done():
				}
			}
		}
	}
}
