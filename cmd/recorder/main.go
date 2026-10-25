// Recorder captures local host metrics and logs once, then uploads them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tetoraorg/hokaku"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stderr); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, output io.Writer) error {
	flags := flag.NewFlagSet("recorder", flag.ContinueOnError)
	flags.SetOutput(output)
	var config capture.Config
	var id string
	flags.StringVar(&config.Endpoint, "endpoint", getenv("KOSAKANA_ENDPOINT"), "central server origin URL (or KOSAKANA_ENDPOINT)")
	flags.StringVar(&config.Environment, "environment", "", "environment name (required)")
	flags.StringVar(&config.Host, "host", "", "host name (required)")
	flags.StringVar(&config.OutputDir, "output-dir", "", "local capture directory (required)")
	flags.StringVar(&config.AccessLog, "access-log", "", "NGINX JSON log path; empty disables collection")
	flags.StringVar(&config.SlowLog, "slow-log", "", "MySQL slow log path; empty disables collection")
	flags.DurationVar(&config.Duration, "duration", 60*time.Second, "recording duration")
	flags.DurationVar(&config.Interval, "interval", time.Second, "host sampling interval")
	flags.StringVar(&id, "capture-id", "", "shared capture ID; empty generates an ID")
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: recorder [flags] [-- rotation-command args...]")
		fmt.Fprintln(output, "Set KOSAKANA_ACCESS_KEY for authentication. fgprof profiles recorder itself, not another application.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	config.Token = getenv("KOSAKANA_ACCESS_KEY")
	command := flags.Args()
	if len(command) == 0 {
		if config.AccessLog != "" || config.SlowLog != "" {
			return errors.New("log collection requires a rotation command after --")
		}
		config.Rotate = func(context.Context) error { return nil }
	} else {
		config.Rotate = capture.CommandRotator(command[0], command[1:]...)
	}
	done := make(chan capture.Result, 1)
	config.OnComplete = func(result capture.Result) { done <- result }
	recorder, err := capture.New(config)
	if err != nil {
		return err
	}
	defer recorder.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		id, err = recorder.Start()
	} else {
		id, err = recorder.StartWithID(id)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "capture started: %s (fgprof target: recorder process)\n", id)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-done:
		if result.Err != nil {
			return fmt.Errorf("capture %s failed (files: %s): %w", result.ID, result.Directory, result.Err)
		}
		fmt.Fprintf(output, "capture uploaded: %s (files: %s)\n", result.ID, result.Directory)
		return nil
	}
}
