// Command fakeapp imitates the Sonarr API used by dev/config/apps.yaml, so the
// version check and the update jobs can be exercised locally (make
// run-fakeapp). POST /api/v3/command installs --upgrade-to a few seconds later
// (nothing if empty: the job then times out). It also prints the webhook
// notifications it receives on POST /hook. Development only.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

func main() {
	listen := flag.String("listen", ":8989", "listen address")
	initial := flag.String("version", "4.0.0.700", "installed version to report")
	upgradeTo := flag.String("upgrade-to", "", "version installed by ApplicationUpdate (empty: none)")
	flag.Parse()
	apiKey := os.Getenv("SONARR_API_KEY")
	var mu sync.Mutex
	version := *initial

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		v := version
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"appName": "Sonarr", "version": v})
	})
	mux.HandleFunc("POST /api/v3/command", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		log.Printf("command received: %s", body)
		if *upgradeTo != "" {
			time.AfterFunc(5*time.Second, func() {
				mu.Lock()
				version = *upgradeTo
				mu.Unlock()
				log.Printf("now on version %s", *upgradeTo)
			})
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"ApplicationUpdate","status":"queued"}`))
	})
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"OK"}`))
	})
	mux.HandleFunc("POST /hook", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		log.Printf("notification received: %s", body)
		w.WriteHeader(http.StatusNoContent)
	})

	log.Printf("fake Sonarr %s listening on %s (upgrade to %q)", version, *listen, *upgradeTo)
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
