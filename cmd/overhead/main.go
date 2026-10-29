// overhead is a reproducible synthetic HTTP experiment, not an ISUCON score predictor.
// Each measured server runs in its own process so fgprof never samples the load generator.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/felixge/fgprof"
	"github.com/google/pprof/profile"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/tetoraorg/hokaku"
)

var mode = flag.String("mode", "", "child mode; empty runs experiment")
var seconds = flag.Int("seconds", 10, "seconds of measured HTTP load per run")
var repeats = flag.Int("repeats", 3, "repetitions; rotates mode order")
var workers = flag.Int("workers", 16, "concurrent HTTP clients")
var parked = flag.Int("parked", 0, "extra idle application goroutines (fgprof sensitivity)")
var out = flag.String("out", "overhead-results", "output directory")
var mixed = flag.Bool("mixed", false, "add 2ms simulated I/O to each request")

// workload is kept separate so profile presence can be checked.
//
//go:noinline
func workload() [32]byte {
	var hash [32]byte
	for i := 0; i < 2000; i++ {
		hash = sha256.Sum256(hash[:])
	}
	return hash
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if *mode != "" {
		return serve()
	}
	if *seconds < 2 || *seconds > 100 || *repeats < 1 || *workers < 1 {
		return fmt.Errorf("invalid experiment parameters")
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		return err
	}
	modes := []string{"none", "fgprof", "host", "process"}
	for rep := 0; rep < *repeats; rep++ {
		for j := 0; j < len(modes); j++ {
			m := modes[(j+rep)%len(modes)]
			dir := filepath.Join(*out, fmt.Sprintf("%d-%s", rep, m))
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			cmd := exec.Command(os.Args[0], "-mode", m, "-seconds", fmt.Sprint(*seconds), "-parked", fmt.Sprint(*parked), "-out", dir, fmt.Sprintf("-mixed=%t", *mixed))
			cmd.Stderr = os.Stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return err
			}
			if err = cmd.Start(); err != nil {
				return err
			}
			var ready struct{ URL string }
			if err = json.NewDecoder(stdout).Decode(&ready); err != nil {
				cmd.Process.Kill()
				cmd.Wait()
				return err
			}
			client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *workers}}
			request := func(path string) error {
				r, e := client.Get(ready.URL + path)
				if e != nil {
					return e
				}
				defer r.Body.Close()
				_, e = io.Copy(io.Discard, r.Body)
				if r.StatusCode != 200 {
					return fmt.Errorf("HTTP %d", r.StatusCode)
				}
				return e
			}
			for i := 0; i < 100; i++ {
				if err = request("/work"); err != nil {
					return err
				}
			}
			if err = request("/start"); err != nil {
				return err
			}
			started := time.Now()
			deadline := started.Add(time.Duration(*seconds) * time.Second)
			var wg sync.WaitGroup
			var mu sync.Mutex
			var latencies []float64
			failures := 0
			for i := 0; i < *workers; i++ {
				wg.Go(func() {
					var local []float64
					failed := 0
					for time.Now().Before(deadline) {
						t := time.Now()
						if e := request("/work"); e != nil {
							failed++
						} else {
							local = append(local, float64(time.Since(t))/1e6)
						}
					}
					mu.Lock()
					latencies = append(latencies, local...)
					failures += failed
					mu.Unlock()
				})
			}
			wg.Wait()
			elapsed := time.Since(started).Seconds()
			if err = request("/stop"); err != nil {
				return err
			}
			if err = cmd.Wait(); err != nil {
				return err
			}
			client.CloseIdleConnections()
			sort.Float64s(latencies)
			if len(latencies) == 0 {
				return fmt.Errorf("no successful requests")
			}
			var child map[string]any
			data, err := os.ReadFile(filepath.Join(dir, "server.json"))
			if err != nil {
				return err
			}
			if err = json.Unmarshal(data, &child); err != nil {
				return err
			}
			child["mode"] = m
			child["repeat"] = rep
			child["requests"] = len(latencies)
			child["errors"] = failures
			child["rps"] = float64(len(latencies)) / elapsed
			child["p50_ms"] = latencies[len(latencies)/2]
			child["p95_ms"] = latencies[int(float64(len(latencies)-1)*.95)]
			child["p99_ms"] = latencies[int(float64(len(latencies)-1)*.99)]
			child["workers"] = *workers
			child["parked"] = *parked
			child["mixed"] = *mixed
			child["go"] = runtime.Version()
			if err = json.NewEncoder(os.Stdout).Encode(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func serve() error {
	for i := 0; i < *parked; i++ {
		go func() { select {} }()
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	url := "http://" + listener.Addr().String()
	mux := http.NewServeMux()
	done := make(chan struct{})
	var recorder *hokaku.Recorder
	var stop func() error
	var profileFile *os.File
	completed := make(chan hokaku.Result, 1)
	var before resourceUsage
	var started time.Time
	mux.HandleFunc("/work", func(w http.ResponseWriter, r *http.Request) {
		hash := workload()
		if *mixed {
			time.Sleep(2 * time.Millisecond)
		}
		w.Write(hash[:])
	})
	mux.HandleFunc("PUT /api/captures/", func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); w.WriteHeader(204) })
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		var e error
		if *mode == "fgprof" {
			profileFile, e = os.Create(filepath.Join(*out, "fgprof.pprof"))
			if e == nil {
				stop = fgprof.Start(profileFile, fgprof.FormatPprof)
			}
		}
		if *mode == "host" || *mode == "process" {
			recorder, e = hokaku.New(hokaku.Config{Endpoint: url, Token: "local-experiment", Environment: "overhead", Host: "app", OutputDir: *out, Rotate: func(context.Context) error { return nil }, Duration: time.Duration(*seconds+1) * time.Second, DisableProcessMetrics: *mode == "host", OnComplete: func(r hokaku.Result) { completed <- r }})
			if e == nil {
				_, e = recorder.StartWithID("capture")
			}
		}
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		before = resources()
		started = time.Now()
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		after := resources()
		wall := time.Since(started).Seconds()
		result := map[string]any{"server_cpu_seconds": after.CPU - before.CPU, "server_cpu_percent": 100 * (after.CPU - before.CPU) / wall, "max_rss_kib": after.RSS}
		if self, e := process.NewProcess(int32(os.Getpid())); e == nil {
			if memory, e := self.MemoryInfo(); e == nil {
				result["server_rss_kib"] = memory.RSS / 1024
			}
		}
		ppath := filepath.Join(*out, "fgprof.pprof")
		if recorder != nil {
			capture := <-completed
			if capture.Err != nil {
				http.Error(w, capture.Err.Error(), 500)
				return
			}
			recorder.Close()
			ppath = filepath.Join(capture.Directory, "fgprof.pprof")
			b, _ := os.ReadFile(filepath.Join(capture.Directory, "host.jsonl"))
			result["host_jsonl_bytes"] = len(b)
		}
		if stop != nil {
			if e := stop(); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			profileFile.Close()
		}
		if *mode != "none" {
			b, e := os.ReadFile(ppath)
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			p, e := profile.ParseData(b)
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			var scan, work, total int64
			for _, s := range p.Sample {
				value := s.Value[len(s.Value)-1]
				total += value
				hasScan, hasWork := false, false
				for _, l := range s.Location {
					for _, line := range l.Line {
						if line.Function != nil {
							n := line.Function.Name
							if n == "github.com/tetoraorg/hokaku/internal/process.scan" {
								hasScan = true
							}
							if n == "main.workload" {
								hasWork = true
							}
						}
					}
				}
				if hasScan {
					scan += value
				}
				if hasWork {
					work += value
				}
			}
			result["profile_scan_ns"] = scan
			result["profile_work_ns"] = work
			result["profile_total_ns"] = total
			result["profile_bytes"] = len(b)
		}
		f, e := os.Create(filepath.Join(*out, "server.json"))
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		json.NewEncoder(f).Encode(result)
		f.Close()
		close(done)
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	json.NewEncoder(os.Stdout).Encode(map[string]string{"URL": url})
	<-done
	return server.Shutdown(context.Background())
}
