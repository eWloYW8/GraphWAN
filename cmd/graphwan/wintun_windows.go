package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/eWloYW8/GraphWAN/internal/wintunsetup"
)

func prepareAgentPlatform(ctx context.Context) error {
	return installWintun(ctx, "")
}

func installWintun(ctx context.Context, archive string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	directory := filepath.Dir(executable)
	_, err = wintunsetup.Ensure(ctx, directory, runtime.GOARCH, archive)
	if err != nil {
		return fmt.Errorf("prepare Wintun beside %s: %w; use an elevated terminal with write access, or run 'graphwan agent install-wintun --archive PATH' with the official ZIP", executable, err)
	}
	return nil
}

func runInstallWintun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent install-wintun", flag.ContinueOnError)
	archive := fs.String("archive", "", "offline official Wintun ZIP (default: download via HTTPS)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if err := installWintun(ctx, *archive); err != nil {
		return err
	}
	fmt.Println("Wintun DLL is present beside graphwan.exe (existing files are preserved).")
	return nil
}
