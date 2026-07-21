package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/mattiasthalen/qlik-parser/internal/catalog"
	"github.com/mattiasthalen/qlik-parser/internal/extractor"
)

func newCatalogCmd() *cobra.Command {
	var sourceDir string
	var outFile string
	var ndjson bool

	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Build a cross-app catalog of measures, dimensions and variables",
		Long: `Scans --source recursively for .qvf files and builds a combined index of
every master measure, master dimension and variable across all apps, noting
which app defines each so duplicated and conflicting definitions surface.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sourceDir == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("could not determine working directory: %w", err)
				}
				sourceDir = cwd
			}
			info, err := os.Stat(sourceDir)
			if err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "error: --source %q: %v\n", sourceDir, err)
				return ExitError(1)
			}
			if !info.IsDir() {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "error: --source %q is a file, not a directory\n", sourceDir)
				return ExitError(1)
			}

			paths, walkWarns := extractor.Walk(sourceDir)
			for _, w := range walkWarns {
				log.Warn().Msg(w)
			}

			builder := catalog.NewBuilder()
			apps := 0
			for _, p := range paths {
				if filepath.Ext(p) != ".qvf" {
					continue
				}
				data, parseErr := extractor.ParseQVF(p)
				if parseErr != nil {
					log.Warn().Str("file", p).Err(parseErr).Msg("skipping unparseable app")
					continue
				}
				builder.AddApp(filepath.Base(p), data)
				apps++
			}

			cat := builder.Build()

			var out []byte
			if ndjson {
				out, err = cat.NDJSON()
			} else {
				out, err = json.MarshalIndent(cat, "", "  ")
			}
			if err != nil {
				return fmt.Errorf("encoding catalog: %w", err)
			}

			if outFile == "" {
				_, _ = cmd.OutOrStdout().Write(out)
				if !ndjson {
					_, _ = fmt.Fprintln(cmd.OutOrStdout())
				}
			} else {
				if err := os.WriteFile(outFile, out, 0644); err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "error: writing %q: %v\n", outFile, err)
					return ExitError(1)
				}
			}

			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"catalogued %d apps: %d measures, %d dimensions, %d variables, %d conflicts\n",
				apps, len(cat.Measures), len(cat.Dimensions), len(cat.Variables), len(cat.Conflicts))
			return nil
		},
	}

	cmd.Flags().StringVarP(&sourceDir, "source", "s", "", "Source directory to scan for .qvf files (default: current directory)")
	cmd.Flags().StringVarP(&outFile, "out", "o", "", "Output file (default: stdout)")
	cmd.Flags().BoolVar(&ndjson, "ndjson", false, "Emit newline-delimited JSON rows for BigQuery loading")

	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		_, _ = fmt.Fprintf(c.ErrOrStderr(), "error: %v\n", err)
		return ExitError(2)
	})

	return cmd
}
