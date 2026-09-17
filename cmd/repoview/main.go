// Package main is the entry point for the repoview-go utility.
//
// OBJECTIVES:
// Provides the CLI frontend for repoview-go, transforming RPM repository
// metadata into high-performance, air-gap compliant static HTML browsing sites.
//
// CORE COMPONENTS:
//   - stringList: Custom flag.Value implementation for accumulating repeatable CLI options.
//   - run(): Core CLI runner accepting input arguments and output streams, returning exit codes.
//   - main(): CLI entry point delegating to run() and exiting with the resulting status code.
//
// FUNCTIONALITY:
//   - Parses long-only command-line flags (--output-dir, --baseurl, --force, etc.).
//   - Validates existence and readability of the target repository directory early.
//   - Assembles application configuration (app.Config) with safety validations.
//   - Executes the core generation engine (app.Generator).
//   - Translates domain errors into granular exit codes (ExitSuccess, ExitUsageError, ExitRepoMetadataError, etc.).
//
// DATA FLOW:
//
//	os.Args -> run() -> flag.Parse() -> app.Config -> app.NewGenerator() -> generator.Run() -> exit code
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/app"
	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// Version string for the repoview binary.
var version = "dev"

// stringList implements flag.Value to support repeatable command-line options
// such as --ignore-package and --exclude-arch.
type stringList []string

// String returns a comma-separated representation of the stringList values.
func (s *stringList) String() string {
	return strings.Join(*s, ", ")
}

// Set appends a new string value to the stringList slice.
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// run parses arguments, executes the generation pipeline, and returns the appropriate exit code.
// It accepts stdout and stderr writers to allow in-process unit testing without global state leaks.
func run(args []string, stdout, stderr io.Writer) int {
	// Enforce standard umask (0022) so directories (0755) and files (0644)
	// are created with web-accessible permissions even if the parent environment (systemd/cron) has a restrictive umask.
	util.SetUmask(util.DefaultUmask)

	var (
		repoDir     string
		outputDir   string
		stateDir    string
		compsFile   string
		templateDir string
		title       string
		url         string
		baseURL     string
		format      string
		force       bool
		quiet       bool
		showVer     bool
		ignoreList  stringList
		excludeArch stringList
	)

	fs := flag.NewFlagSet("repoview", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.StringVar(&outputDir, "output-dir", "repoview", "Output directory")
	fs.StringVar(&stateDir, "state-dir", "", "State directory (default: output directory)")
	fs.StringVar(&title, "title", "Repoview", "Repository title")
	fs.StringVar(&url, "url", "", "Repository URL (for RSS feed)")
	fs.StringVar(&baseURL, "baseurl", "", "Repository base URL for client configuration")
	fs.StringVar(&format, "format", "auto", "Repository format: auto, rpm, or deb")
	fs.StringVar(&templateDir, "template-dir", "", "Template directory")
	fs.BoolVar(&force, "force", false, "Force regeneration")
	fs.BoolVar(&quiet, "quiet", false, "Quiet mode")
	fs.StringVar(&compsFile, "comps", "", "Alternative comps.xml file")
	fs.Var(&ignoreList, "ignore-package", "Ignore package glob (can be repeated)")
	fs.Var(&excludeArch, "exclude-arch", "Exclude architecture (can be repeated)")
	fs.BoolVar(&showVer, "version", false, "Display version")

	// Custom Usage output to display clean long-only options to the user.
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: repoview [options] <repodir>\n\nOptions:\n")

		printOption := func(long, desc string, def interface{}) {
			defStr := ""
			if def != nil {
				defStr = fmt.Sprintf(" (default %v)", def)
			}
			if s, ok := def.(string); ok && s == "" {
				defStr = ""
			}
			if b, ok := def.(bool); ok && !b {
				defStr = ""
			}

			_, _ = fmt.Fprintf(stderr, "  --%-20s\t%s%s\n", long, desc, defStr)
		}

		printOption("output-dir <dir>", "Output directory", "repoview")
		printOption("state-dir <dir>", "State directory", "output directory")
		printOption("title <text>", "Repository title", "Repoview")
		printOption("url <url>", "Repository URL (for RSS feed)", "")
		printOption("baseurl <url>", "Repository base URL for client configuration", "")
		printOption("format <type>", "Repository format (auto, rpm, deb)", "auto")
		printOption("template-dir <dir>", "Template directory", "embedded")
		printOption("comps <file>", "Alternative comps.xml file", "")
		printOption("ignore-package <glob>", "Ignore package glob (can be repeated)", "")
		printOption("exclude-arch <arch>", "Exclude architecture (can be repeated)", "")
		printOption("force", "Force regeneration", false)
		printOption("quiet", "Quiet mode", false)
		printOption("version", "Display version information", false)
	}

	if err := fs.Parse(args); err != nil {
		return app.ExitUsageError
	}

	// Handle version query
	if showVer {
		_, _ = fmt.Fprintf(stdout, "repoview version %s\n", version)
		return app.ExitSuccess
	}

	// Positional repodir argument is required
	if fs.NArg() < 1 {
		fs.Usage()
		return app.ExitUsageError
	}
	repoDir = fs.Arg(0)

	// Validate repository directory existence
	if fi, err := os.Stat(repoDir); err != nil || !fi.IsDir() {
		_, _ = fmt.Fprintf(stderr, "Error: repository directory does not exist or is not a directory: %s\n", repoDir)
		return app.ExitRepoMetadataError
	}

	// Validate format option
	var repoFormat models.RepoFormat
	switch strings.ToLower(format) {
	case "auto", "":
		repoFormat = "" // Auto-detection
	case "rpm":
		repoFormat = models.FormatRPM
	case "deb":
		repoFormat = models.FormatDEB
	default:
		_, _ = fmt.Fprintf(stderr, "Error: invalid repository format '%s' (must be 'auto', 'rpm', or 'deb')\n", format)
		return app.ExitUsageError
	}

	// Resolve relative outputDir against repoDir, matching Python repoview behavior
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(repoDir, outputDir)
	}

	if !quiet {
		_, _ = fmt.Fprintf(stdout, "Repoview %s\n", version)
	}

	// If baseURL is omitted but URL is a full HTTP(S) address, reuse it as the default base URL
	if baseURL == "" && (strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
		baseURL = url
	}

	config := app.Config{
		RepoDir:     repoDir,
		OutputDir:   outputDir,
		StateDir:    stateDir,
		CompsFile:   compsFile,
		TemplateDir: templateDir,
		Title:       title,
		URL:         url,
		BaseURL:     baseURL,
		Format:      repoFormat,
		Force:       force,
		Quiet:       quiet,
		IgnoreList:  ignoreList,
		ExcludeArch: excludeArch,
		Version:     version,
	}

	// Instantiate generator and execute generation workflow
	generator := app.NewGenerator(config)
	if err := generator.Run(); err != nil {
		var partialErr *app.PartialFailureError
		var safetyErr *app.SafetyError

		// Translate internal errors into structured diagnostic exit codes
		switch {
		case errors.As(err, &safetyErr):
			_, _ = fmt.Fprintf(stderr, "Safety Violation: %v\n", safetyErr)
			return app.ExitUsageError
		case errors.As(err, &partialErr):
			_, _ = fmt.Fprintf(stderr, "Warning: %v\n", partialErr)
			return app.ExitPartialFailure
		default:
			errMsg := strings.ToLower(err.Error())
			switch {
			case strings.Contains(errMsg, "safety violation"):
				_, _ = fmt.Fprintf(stderr, "Configuration Error: %v\n", err)
				return app.ExitUsageError
			case strings.Contains(errMsg, "repomd") || strings.Contains(errMsg, "primary") || strings.Contains(errMsg, "sqlite") || strings.Contains(errMsg, "decompress") || strings.Contains(errMsg, "packages") || strings.Contains(errMsg, "debian"):
				_, _ = fmt.Fprintf(stderr, "Repository Metadata Error: %v\n", err)
				return app.ExitRepoMetadataError
			case strings.Contains(errMsg, "template"):
				_, _ = fmt.Fprintf(stderr, "Template Error: %v\n", err)
				return app.ExitTemplateError
			case strings.Contains(errMsg, "output dir") || strings.Contains(errMsg, "no space") || strings.Contains(errMsg, "permission"):
				_, _ = fmt.Fprintf(stderr, "I/O Error: %v\n", err)
				return app.ExitIOError
			default:
				_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
				return 1
			}
		}
	}

	return app.ExitSuccess
}

// main is the primary execution entry point.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
