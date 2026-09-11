// A local, synthetic intended-state API for a terminal smoke test. This server
// has no credentials, Kubernetes client, or write endpoints.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

func main() {
	typ := flag.String("type", "apps/v1/Deployment", "exact API version/kind")
	name := flag.String("name", "team-a/api", "namespace/name of an existing resource")
	flag.Parse()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("CUB_SERVER=http://%s\n", ln.Addr())
	row := map[string]any{"Resource": map[string]any{
		"ResourceID": "example-resource", "SpaceID": "example-space", "UnitID": "example-unit", "TargetID": "example-target",
		"ResourceType": *typ, "ResourceName": *name,
	}}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "read-only fixture", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/resource" {
			_ = json.NewEncoder(w).Encode([]any{row})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})}
	log.Fatal(server.Serve(ln))
}
