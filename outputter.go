package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"
)

var outputterFlags = cli.MutuallyExclusiveFlags{
	Required: false,
	Flags: [][]cli.Flag{
		{
			&cli.StringFlag{
				Name:        "output",
				DefaultText: "STDOUT",
				Usage:       "The destination to which to write the modified configuration file.",
				Destination: &destinations.outputter.output,
				Aliases:     []string{"o"},
				TakesFile:   true,
				Action: func(ctx context.Context, command *cli.Command, s string) error {
					return command.Set("output", filepath.Clean(s))
				},
				OnlyOnce: true,
			},
		},
		{
			&cli.BoolFlag{
				Name:        "in-place-rewrite",
				Usage:       "Weather to directly replace the resulting changes in the current file. Cannot be set in conjunction with output.",
				Destination: &destinations.outputter.inPlace,
				Aliases:     []string{"i"},
				OnlyOnce:    true,
			},
		},
	},
}

func output(originalPath string, content bytes.Buffer) error {
	switch {
	case destinations.outputter.inPlace:
		stat, err := os.Stat(originalPath)
		if err != nil {
			return fmt.Errorf("failed to get config file stat %s: %w", originalPath, err)
		}
		return writeFileAtomically(originalPath, content.Bytes(), stat.Mode().Perm())
	case destinations.outputter.output != "":
		return writeFileAtomically(destinations.outputter.output, content.Bytes(), 0600)
	default:
		_, err := io.Copy(os.Stdout, &content)
		return err
	}
}

// writeFileAtomically atomically writes the content at the given path by writing it in
// a temporary file first, then renaming it to the destination.
func writeFileAtomically(destination string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(destination)

	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(content); err != nil {
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}

	// TODO: Make this windows friendly
	//
	// On windows rename fails if file already exists; live with it for now since
	// this utility is not used on windows.

	return os.Rename(tmpName, destination)
}
