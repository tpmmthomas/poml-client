// pomld runs one independent PoML full node.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/thomas/poml-node/internal/app"
)

func main() {
	configPath := flag.String("config", "configs/node.toml", "path to the node TOML configuration")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	node, err := app.Open(*configPath)
	if err != nil {
		logger.Error("open node", "error", err)
		os.Exit(1)
	}
	defer node.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := node.Start(ctx); err != nil {
		logger.Error("start node", "error", err)
		os.Exit(1)
	}
	<-ctx.Done()
}
