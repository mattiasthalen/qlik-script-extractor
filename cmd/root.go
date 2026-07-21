package cmd

import (
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// NewRootCmd constructs the root cobra command.
func NewRootCmd() *cobra.Command {
	var logLevel string

	root := &cobra.Command{
		Use:   "qlik-parser",
		Short: "Parse and extract artifacts from QlikView .qvw files",
		Long: `qlik-parser recursively scans a directory for QVW files
and extracts embedded artifacts (load scripts, and more to come).`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			level, err := zerolog.ParseLevel(logLevel)
			if err != nil {
				level = zerolog.Disabled
			}
			zerolog.SetGlobalLevel(level)
			return nil
		},
	}

	root.AddCommand(newVersionCmd())
	root.AddCommand(newExtractCmd())
	root.AddCommand(newCatalogCmd())
	root.AddCommand(newServeCmd())

	root.PersistentFlags().StringVar(&logLevel, "log-level", "disabled",
		"Log level: debug, info, warn, error, disabled")

	return root
}
