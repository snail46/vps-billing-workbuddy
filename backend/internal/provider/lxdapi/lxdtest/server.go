// Package lxdtest provides a LXD-shaped HTTP server for tests that exercise
// the LXD adapter without a hypervisor. It speaks the API's envelope, async
// operations and error codes — enough for the contract suite and for the
// provision chain's integration tests.
package lxdtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// FakeLXD is a tiny LXD-shaped server: the endpoints the adapter touches, in
// the envelope LXD answers with.
type FakeLXD struct {
	mu        sync.Mutex
	instances map[string]*fakeInstance
}

type fakeInstance struct {
	name      string
	status    string
	createdAt string
	devices   map[string]map[string]any
}

// NewServer starts a fake LXD and returns it with the base URL to point an
// adapter at. The server requires the bearer token "test-token".
func NewServer(t *testing.T) (fake *FakeLXD, endpoint string) {
	t.Helper()
	fake = &FakeLXD{instances: map[string]*fakeInstance{}}
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	return fake, server.URL
}

// InstanceCount reports how many machines the fake currently holds, so an
// integration test can assert the provider was used exactly once.
func (f *FakeLXD) InstanceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.instances)
}

func (f *FakeLXD) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, w) {
			return
		}
		writeSync(w, map[string]any{"env": map[string]any{"server_version": "5.21"}})
	})
	mux.HandleFunc("/1.0/images", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, w) {
			return
		}
		writeSync(w, []map[string]any{
			{"fingerprint": "fp-debian-12", "architecture": "x86_64",
				"properties": map[string]any{"os": "Debian", "release": "12",
					"description": "Debian 12 (bookworm)"}},
			{"fingerprint": "fp-ubuntu-2404", "architecture": "x86_64",
				"properties": map[string]any{"os": "Ubuntu", "release": "24.04",
					"description": "Ubuntu 24.04 LTS"}},
		})
	})
	mux.HandleFunc("/1.0/instances", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, w) {
			return
		}
		f.create(w, r)
	})
	mux.HandleFunc("/1.0/instances/", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, w) {
			return
		}
		f.instance(r, w)
	})
	mux.HandleFunc("/1.0/operations/", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, w) {
			return
		}
		// The wait endpoint: the fake settles every operation immediately.
		writeSync(w, map[string]any{"status": "Success"})
	})
	return mux
}

// authorized checks the bearer token; it needs no server state, hence the
// free function.
func authorized(r *http.Request, w http.ResponseWriter) bool {
	if r.Header.Get("Authorization") != "Bearer test-token" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":"unauthorized","error_code":401}`))
		return false
	}
	return true
}

func writeSync(w http.ResponseWriter, metadata any) {
	w.Header().Set("Content-Type", "application/json")
	encoded, _ := json.Marshal(map[string]any{
		"type": "sync", "status": "Success", "status_code": 200, "metadata": metadata,
	})
	_, _ = w.Write(encoded)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	encoded, _ := json.Marshal(map[string]any{
		"type": "error", "error": message, "error_code": status,
	})
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

func writeAsync(w http.ResponseWriter, operationID string) {
	w.Header().Set("Content-Type", "application/json")
	encoded, _ := json.Marshal(map[string]any{
		"type": "async", "status": "Operation created", "status_code": 100,
		"operation": "/1.0/operations/" + operationID,
	})
	_, _ = w.Write(encoded)
}

func (f *FakeLXD) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.instances[body.Name]; exists {
		writeError(w, http.StatusConflict, "Instance already exists")
		return
	}
	f.instances[body.Name] = &fakeInstance{
		name:      body.Name,
		status:    "Running", // the fake provisions instantly, like the mock
		createdAt: time.Now().UTC().Format(time.RFC3339Nano),
		devices:   map[string]map[string]any{},
	}
	writeAsync(w, "op-create-"+body.Name)
}

// instance serves the /1.0/instances/<name>[/state] routes.
func (f *FakeLXD) instance(r *http.Request, w http.ResponseWriter) {
	name := r.URL.Path[len("/1.0/instances/"):]
	var action string
	for _, suffix := range []string{"/state"} {
		if len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix {
			name = name[:len(name)-len(suffix)]
			action = suffix
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	inst, exists := f.instances[name]
	if !exists {
		writeError(w, http.StatusNotFound, "Instance not found")
		return
	}

	switch action {
	case "/state":
		if r.Method == http.MethodPut {
			var body struct {
				Action string `json:"action"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body.Action {
			case "start":
				inst.status = "Running"
			case "stop":
				inst.status = "Stopped"
			case "restart":
				inst.status = "Running"
			}
			writeAsync(w, "op-state-"+name)
			return
		}
		writeSync(w, map[string]any{
			"memory": map[string]any{"usage": 512 << 20, "total": 1024 << 20},
			"disk":   map[string]any{"root": map[string]any{"usage": 4 << 30, "total": 20 << 30}},
			"network": map[string]any{"eth0": map[string]any{
				"addresses": []map[string]any{
					{"family": "inet", "address": "10.10.10.10"},
					{"family": "inet6", "address": "fd42::10"},
				},
				"counters": map[string]any{"bytes_received": 1024, "bytes_sent": 2048},
			}},
		})
	default:
		if r.Method == http.MethodDelete {
			delete(f.instances, name)
			writeAsync(w, "op-delete-"+name)
			return
		}
		if r.Method == http.MethodPatch {
			var body struct {
				Devices map[string]map[string]any `json:"devices"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			inst.devices = body.Devices
			writeSync(w, nil)
			return
		}
		writeSync(w, map[string]any{
			"status":     inst.status,
			"created_at": inst.createdAt,
			"devices":    inst.devices,
		})
	}
}
