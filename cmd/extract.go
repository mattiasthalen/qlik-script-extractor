package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/mattiasthalen/qlik-parser/internal/extractor"
	"github.com/mattiasthalen/qlik-parser/internal/ui"
)

func newExtractCmd() *cobra.Command {
	var sourceDir string
	var outDir string
	var dryRun bool
	var script bool
	var measures bool
	var dimensions bool
	var variables bool
	var sheets bool
	var lineage bool

	cmd := &cobra.Command{
		Use:   "extract",
		Short: "Extract artifacts from .qvw and .qvf files",
		Long: `Recursively scans --source for .qvw and .qvf files and extracts embedded
artifacts to a per-file folder alongside or under --out.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			anyChanged := cmd.Flags().Changed("script") ||
				cmd.Flags().Changed("measures") ||
				cmd.Flags().Changed("dimensions") ||
				cmd.Flags().Changed("variables") ||
				cmd.Flags().Changed("sheets") ||
				cmd.Flags().Changed("lineage")
			extractAll := !anyChanged
			doScript := extractAll || script
			doMeasures := extractAll || measures
			doDimensions := extractAll || dimensions
			doVariables := extractAll || variables
			doSheets := extractAll || sheets
			doLineage := extractAll || lineage

			if anyChanged && !doScript && !doMeasures && !doDimensions && !doVariables && !doSheets && !doLineage {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "error: no artifact type selected\n")
				return ExitError(1)
			}

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

			if outDir != "" {
				if err := os.MkdirAll(outDir, 0755); err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "error: cannot create --out directory %q: %v\n", outDir, err)
					return ExitError(1)
				}
			}

			qlikPaths, walkWarns := extractor.Walk(sourceDir)
			for _, w := range walkWarns {
				log.Warn().Msg(w)
			}

			isTTY := ui.IsTTY(os.Stdout)
			printer := ui.NewPrinter(cmd.OutOrStdout(), isTTY, dryRun)

			hasErr := false

			for i, srcPath := range qlikPaths {
				printer.UpdateSpinner(i+1, len(qlikPaths))

				relPath, err := filepath.Rel(sourceDir, srcPath)
				if err != nil {
					relPath = filepath.Base(srcPath)
				}

				isQVF := filepath.Ext(srcPath) == ".qvf"

				// Build the artifact slice.
				var artifacts []extractor.Artifact

				if isQVF {
					qvfData, parseErr := extractor.ParseQVF(srcPath)
					if parseErr != nil {
						hasErr = true
						printer.ClearSpinner()
						errMsg := parseErr.Error()
						if after, ok := strings.CutPrefix(errMsg, srcPath+": "); ok {
							errMsg = after
						}
						printer.FileResult(ui.Result{
							Status:  ui.StatusErr,
							SrcPath: relPath,
							Message: errMsg,
						})
						continue
					}

					if doScript && qvfData.Script != "" {
						artifacts = append(artifacts, extractor.Artifact{
							Name:    "script.qvs",
							Content: []byte(qvfData.Script),
						})
					}
					if doMeasures {
						b, marshalErr := json.MarshalIndent(qvfData.Measures, "", "  ")
						if marshalErr != nil {
							hasErr = true
							printer.ClearSpinner()
							printer.FileResult(ui.Result{
								Status:  ui.StatusErr,
								SrcPath: relPath,
								Message: fmt.Sprintf("measures: %v", marshalErr),
							})
							continue
						}
						artifacts = append(artifacts, extractor.Artifact{Name: "measures.json", Content: b})
					}
					if doDimensions {
						b, marshalErr := json.MarshalIndent(qvfData.Dimensions, "", "  ")
						if marshalErr != nil {
							hasErr = true
							printer.ClearSpinner()
							printer.FileResult(ui.Result{
								Status:  ui.StatusErr,
								SrcPath: relPath,
								Message: fmt.Sprintf("dimensions: %v", marshalErr),
							})
							continue
						}
						artifacts = append(artifacts, extractor.Artifact{Name: "dimensions.json", Content: b})
					}
					if doVariables {
						b, marshalErr := json.MarshalIndent(qvfData.Variables, "", "  ")
						if marshalErr != nil {
							hasErr = true
							printer.ClearSpinner()
							printer.FileResult(ui.Result{
								Status:  ui.StatusErr,
								SrcPath: relPath,
								Message: fmt.Sprintf("variables: %v", marshalErr),
							})
							continue
						}
						artifacts = append(artifacts, extractor.Artifact{Name: "variables.json", Content: b})
					}
					if doSheets {
						b, marshalErr := json.MarshalIndent(qvfData.Sheets, "", "  ")
						if marshalErr != nil {
							hasErr = true
							printer.ClearSpinner()
							printer.FileResult(ui.Result{
								Status:  ui.StatusErr,
								SrcPath: relPath,
								Message: fmt.Sprintf("sheets: %v", marshalErr),
							})
							continue
						}
						artifacts = append(artifacts, extractor.Artifact{Name: "sheets.json", Content: b})
					}
					if doLineage {
						b, marshalErr := json.MarshalIndent(qvfData.Lineage, "", "  ")
						if marshalErr != nil {
							hasErr = true
							printer.ClearSpinner()
							printer.FileResult(ui.Result{
								Status:  ui.StatusErr,
								SrcPath: relPath,
								Message: fmt.Sprintf("lineage: %v", marshalErr),
							})
							continue
						}
						artifacts = append(artifacts, extractor.Artifact{Name: "lineage.json", Content: b})
					}
				} else {
					// QVW: script only
					if doScript {
						scriptContent, extractErr := extractor.ExtractScript(srcPath)
						if extractErr != nil {
							var noScript *extractor.NoScriptError
							if errors.As(extractErr, &noScript) {
								printer.ClearSpinner()
								printer.FileResult(ui.Result{
									Status:  ui.StatusWarn,
									SrcPath: relPath,
									Message: "no script found",
								})
								continue
							}
							hasErr = true
							printer.ClearSpinner()
							errMsg := extractErr.Error()
							if after, ok := strings.CutPrefix(errMsg, srcPath+": "); ok {
								errMsg = after
							}
							printer.FileResult(ui.Result{
								Status:  ui.StatusErr,
								SrcPath: relPath,
								Message: errMsg,
							})
							continue
						}
						artifacts = append(artifacts, extractor.Artifact{
							Name:    "script.qvs",
							Content: []byte(scriptContent),
						})
					}
				}

				if len(artifacts) == 0 {
					printer.ClearSpinner()
					printer.FileResult(ui.Result{
						Status:  ui.StatusWarn,
						SrcPath: relPath,
						Message: "no script found",
					})
					continue
				}

				resolvedOutDir := extractor.ResolveOutputDir(srcPath, sourceDir, outDir)
				relOut, err := filepath.Rel(sourceDir, resolvedOutDir)
				if err != nil {
					relOut = filepath.Base(resolvedOutDir)
				}
				if outDir != "" && outDir != sourceDir {
					if r, err := filepath.Rel(outDir, resolvedOutDir); err == nil {
						relOut = r
					}
				}

				fileNames := make([]string, len(artifacts))
				for j, a := range artifacts {
					fileNames[j] = a.Name
				}

				writeErr := extractor.WriteArtifacts(resolvedOutDir, artifacts, dryRun)
				if writeErr != nil {
					hasErr = true
					printer.ClearSpinner()
					printer.FileResult(ui.Result{
						Status:  ui.StatusErr,
						SrcPath: relPath,
						Message: writeErr.Error(),
					})
					continue
				}

				printer.ClearSpinner()
				printer.FileResult(ui.Result{
					Status:  ui.StatusOK,
					SrcPath: relPath,
					OutDir:  relOut,
					Files:   fileNames,
				})
			}

			printer.Summary()

			if hasErr {
				return ExitError(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&sourceDir, "source", "s", "", "Source directory to scan for .qvw and .qvf files (default: current directory)")
	cmd.Flags().StringVarP(&outDir, "out", "o", "", "Export directory (default: alongside source files)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be extracted without writing files")
	cmd.Flags().BoolVar(&script, "script", false, "Extract load scripts")
	cmd.Flags().BoolVar(&measures, "measures", false, "Extract master measures (QVF only)")
	cmd.Flags().BoolVar(&dimensions, "dimensions", false, "Extract master dimensions (QVF only)")
	cmd.Flags().BoolVar(&variables, "variables", false, "Extract variables (QVF only)")
	cmd.Flags().BoolVar(&sheets, "sheets", false, "Extract sheets & visualisations inventory (QVF only)")
	cmd.Flags().BoolVar(&lineage, "lineage", false, "Extract load-script lineage: connections, tables, sources (QVF only)")

	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		_, _ = fmt.Fprintf(c.ErrOrStderr(), "error: %v\n", err)
		return ExitError(2)
	})

	return cmd
}

// ExitCodeError signals a specific exit code to main.
type ExitCodeError struct {
	Code int
}

func (e *ExitCodeError) Error() string {
	return fmt.Sprintf("exit %d", e.Code)
}

// ExitError creates an ExitCodeError.
func ExitError(code int) error {
	return &ExitCodeError{Code: code}
}
