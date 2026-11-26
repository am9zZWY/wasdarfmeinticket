// Command wasdarfmeinticket checks which tickets cover a DB connection.
//
//	wasdarfmeinticket serve [-addr :8080]
//	wasdarfmeinticket check -tickets tickets.json [-format markdown|json] [-quiet] <share link>
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/am9zZWY/wasdarfmeinticket/internal/api"
	"github.com/am9zZWY/wasdarfmeinticket/internal/cache"
	"github.com/am9zZWY/wasdarfmeinticket/internal/check"
	"github.com/am9zZWY/wasdarfmeinticket/internal/dbapi"
	"github.com/am9zZWY/wasdarfmeinticket/internal/journey"
	"github.com/am9zZWY/wasdarfmeinticket/internal/progress"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff/efa"
	"github.com/am9zZWY/wasdarfmeinticket/internal/tariff/hafas"
	"github.com/am9zZWY/wasdarfmeinticket/internal/timetable/transitous"
)

const usage = `usage:
  wasdarfmeinticket serve [-addr :8080]
  wasdarfmeinticket check -tickets tickets.json [-format markdown|json] [-quiet] <share or search link>
  wasdarfmeinticket check -tickets tickets.json -from <stop> -to <stop> -departure 2026-11-23T10:45
  wasdarfmeinticket check -tickets tickets.json -recon <file with bahn.de's lookup answer>
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	store := cache.Open(cachePath())
	db := dbapi.New()
	db.Cache = store
	timetable := transitous.New()
	timetable.Cache = store
	planners := efa.New(journey.Berlin)
	planners.Cache = store
	fares := hafas.New(journey.Berlin)
	fares.Cache = store
	svc := &api.Service{DB: db, Tariff: tariff.Sources{planners, fares}, Timetable: timetable, Stops: timetable, Cache: store}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx, svc, os.Args[2:])
	case "check":
		err = runCheck(ctx, svc, os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// cacheEnv names the environment variable for the cache file: a path, or
// "off" for a cache that lives only as long as the process.
const cacheEnv = "WASDARFMEINTICKET_CACHE"

// cachePath returns where answers are kept between runs: by default in the
// user's cache directory.
func cachePath() string {
	switch path := os.Getenv(cacheEnv); {
	case path == "off":
		return ""
	case path != "":
		return path
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "wasdarfmeinticket", "cache.json")
}

func serve(ctx context.Context, svc *api.Service, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := flags.String("addr", ":8080", "listen address")
	flags.Parse(args)

	svc.Log = func(msg string) { log.Println(msg) }
	srv := &http.Server{Addr: *addr, Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	fmt.Fprintln(os.Stderr, "listening on", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runCheck(ctx context.Context, svc *api.Service, args []string) error {
	flags := flag.NewFlagSet("check", flag.ExitOnError)
	ticketsFile := flags.String("tickets", "", "JSON file with an array of tickets")
	format := flags.String("format", "markdown", "output format: markdown or json")
	quiet := flags.Bool("quiet", false, "do not print progress to standard error")
	from := flags.String("from", "", "start stop, instead of a link")
	to := flags.String("to", "", "destination stop, instead of a link")
	departure := flags.String("departure", "", "departure time like 2026-11-23T10:45, instead of a link")
	reconFile := flags.String("recon", "", "file with the pasted answer of bahn.de's share-link lookup, instead of a link (- for standard input)")
	flags.Parse(args)
	if *format != "markdown" && *format != "json" {
		return fmt.Errorf("unknown format %q, use markdown or json", *format)
	}
	if *ticketsFile == "" || (flags.NArg() == 0 && *from == "" && *reconFile == "") {
		return errors.New("check needs -tickets <file> and a link, or -from, -to and -departure, or -recon <file>")
	}
	var recon []byte
	if *reconFile != "" {
		var err error
		if recon, err = readInput(*reconFile); err != nil {
			return err
		}
	}
	raw, err := os.ReadFile(*ticketsFile)
	if err != nil {
		return err
	}
	var tickets []check.Ticket
	if err := json.Unmarshal(raw, &tickets); err != nil {
		return fmt.Errorf("%s: %w", *ticketsFile, err)
	}
	if !*quiet {
		start := time.Now()
		ctx = progress.With(ctx, func(msg string) {
			fmt.Fprintf(os.Stderr, "[%5.1fs] %s\n", time.Since(start).Seconds(), msg)
		})
	}
	resp, err := svc.Check(ctx, api.Request{Link: strings.Join(flags.Args(), " "), From: *from, To: *to, Departure: *departure, Recon: string(recon), Tickets: tickets})
	if err != nil {
		return err
	}
	if *format == "markdown" {
		_, err := fmt.Print(resp.Markdown())
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
}

// readInput reads a file, or standard input for "-".
func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
