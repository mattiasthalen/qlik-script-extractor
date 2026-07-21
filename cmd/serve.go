package cmd

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/mattiasthalen/qlik-parser/internal/service"
)

func newServeCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the QVF documentation HTTP service (Cloud Run)",
		Long: `Starts an HTTP service exposing:
  GET  /health   liveness/readiness probe
  POST /parse    document a .qvf given a gs:// path, signed URL or local path
  POST /events   Eventarc GCS object-finalize handler

Configuration is read from the environment:
  PORT                 listen port (default 8080; set by Cloud Run)
  QVF_API_KEY          required key for /parse (unauthenticated if unset)
  QVF_API_KEY_HEADER   header carrying the key (default X-API-Key)
  QVF_OUTPUT_BUCKET    default output prefix (gs://bucket/prefix or local dir)
  QVF_TMP_DIR          scratch dir for streaming remote inputs before mmap
  QVF_AI_ENABLED       "true" to enable the AI documentation stage
  QVF_AI_MODEL         Anthropic model id (default claude-sonnet-5)
  ANTHROPIC_API_KEY    API key for the AI stage`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.New(slog.NewJSONHandler(cmd.OutOrStdout(), &slog.HandlerOptions{Level: slog.LevelInfo}))

			if addr == "" {
				port := getenv("PORT", "8080")
				addr = ":" + port
			}

			cfg := service.Config{
				APIKey:       os.Getenv("QVF_API_KEY"),
				APIKeyHeader: os.Getenv("QVF_API_KEY_HEADER"),
				OutputBucket: os.Getenv("QVF_OUTPUT_BUCKET"),
				TmpDir:       os.Getenv("QVF_TMP_DIR"),
				Logger:       logger,
			}

			store := service.NewStorage()
			defer func() { _ = store.Close() }()

			documenter, err := service.DocumenterFromEnv(logger)
			if err != nil {
				return err
			}

			srv := service.NewServer(cfg, store, documenter)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return srv.Run(ctx, addr)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "Listen address (default :$PORT or :8080)")
	return cmd
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
