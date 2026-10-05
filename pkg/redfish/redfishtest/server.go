// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package redfishtest provides a synchronized, synthetic Redfish HTTP service.
// It implements only the protocol subset needed for client, power and media tests.
package redfishtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var systemIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

const (
	root          = "/redfish/v1"
	odataID       = "@odata.id"
	actionTarget  = "target"
	resourceID    = "Id"
	resourceName  = "Name"
	offPowerState = "Off"
)

// Config controls synthetic resources. Nil SystemIDs generates SystemCount IDs
// (default one); a non-nil empty slice creates an empty collection.
// All systems start Off unless PowerState is set. Media contains CD and DVD slots.
type Config struct {
	// SystemCount generates numeric IDs when SystemIDs is nil; zero means one.
	SystemCount int
	// SystemIDs overrides generated IDs, including a non-nil empty collection.
	// IDs must be unique, nonempty ASCII letters, digits, underscores or hyphens.
	SystemIDs []string
	// PowerState is the initial state of all systems; empty means Off.
	PowerState string
	// ResetDelay keeps resets in PoweringOn/PoweringOff before reaching their target.
	// Zero completes resets immediately.
	ResetDelay time.Duration
	// Username and Password optionally protect resources other than the root.
	Username string
	Password string
	// DisableMediaActions omits action targets, leaving writable media PATCH.
	DisableMediaActions bool
	// RequireETag rejects writes without an If-Match header.
	RequireETag bool
	// TLS starts an httptest TLS server with a synthetic certificate.
	TLS bool
}

// Fault overrides matching requests. Count=0 repeats forever; positive Count
// consumes that many requests. Status=0 adds delay without changing the response.
// A successful status can simulate an action that acknowledges without mutating.
type Fault struct {
	// Status overrides the response status; zero continues normal handling.
	Status int
	// Delay is a context-aware response delay.
	Delay time.Duration
	// Count is the number of requests affected; zero repeats indefinitely.
	Count int
	// MessageIDs populate Redfish ExtendedInfo MessageId fields.
	MessageIDs []string
	// Message populates synthetic error messages.
	Message string
}

// Reset records a system reset request.
type Reset struct {
	// SystemID identifies the target system.
	SystemID string
	// ResetType records the requested standard reset action.
	ResetType string
}

// Media is a synthetic media snapshot.
type Media struct {
	// Image is the mounted synthetic image URI, or empty after ejection.
	Image string
	// Inserted reports whether media is attached.
	Inserted bool
	// WriteProtected reflects the requested insertion mode.
	WriteProtected bool
}

// State is a detached snapshot of all mutable state.
type State struct {
	// Power and Boot map system IDs to power state and boot target.
	Power map[string]string
	Boot  map[string]string
	// BootEnabled maps system IDs to the boot override mode.
	BootEnabled map[string]string
	// Media maps slot IDs to detached media snapshots.
	Media map[string]Media
	// Resets records successfully processed resets in order.
	Resets []Reset
	// SessionTimeout is the service-wide timeout in seconds.
	SessionTimeout int64
	// Requests counts authenticated requests by "METHOD /path".
	Requests map[string]int
}

// Server embeds an httptest server. State and fault injection are synchronized.
type Server struct {
	*httptest.Server
	mu      sync.Mutex
	config  Config
	ids     []string
	state   State
	faults  map[string]Fault
	etag    int
	pending map[string]transition
}

type transition struct {
	target string
	at     time.Time
}

// New starts a server and registers its closure with tb.Cleanup.
func New(tb testing.TB, cfg Config) *Server {
	tb.Helper()
	server := Start(cfg)
	tb.Cleanup(server.Close)
	return server
}

// Start starts a server for examples or non-test callers, who must call Close.
// It panics for invalid or duplicate SystemIDs or a negative SystemCount.
func Start(cfg Config) *Server {
	if cfg.SystemCount < 0 {
		panic("redfishtest: SystemCount must not be negative")
	}
	ids := slices.Clone(cfg.SystemIDs)
	if cfg.SystemIDs == nil {
		count := cfg.SystemCount
		if count == 0 {
			count = 1
		}
		for i := range count {
			ids = append(ids, strconv.Itoa(i+1))
		}
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !systemIDPattern.MatchString(id) || seen[id] {
			panic("redfishtest: SystemIDs must be unique nonempty ASCII letters, digits, underscores or hyphens")
		}
		seen[id] = true
	}
	powerState := cfg.PowerState
	if powerState == "" {
		powerState = offPowerState
	}
	server := &Server{
		config: cfg, ids: ids, faults: make(map[string]Fault), etag: 1,
		pending: make(map[string]transition),
		state: State{
			Power: make(map[string]string), Boot: make(map[string]string), BootEnabled: make(map[string]string),
			Media: map[string]Media{"CD1": {}, "DVD1": {}}, Requests: make(map[string]int),
			SessionTimeout: 60,
		},
	}
	for _, id := range ids {
		server.state.Power[id] = powerState
		server.state.BootEnabled[id] = "Disabled"
	}
	if cfg.TLS {
		server.Server = httptest.NewTLSServer(http.HandlerFunc(server.serveHTTP))
	} else {
		server.Server = httptest.NewServer(http.HandlerFunc(server.serveHTTP))
	}
	return server
}

// SetFault replaces the fault for method and path. Pass Fault{} to clear it.
func (s *Server) SetFault(method, path string, fault Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fault.MessageIDs = slices.Clone(fault.MessageIDs)
	s.faults[method+" "+path] = fault
}

// Snapshot returns independent copies, safe to inspect while requests run.
func (s *Server) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	state := s.state
	state.Power = maps.Clone(state.Power)
	state.Boot = maps.Clone(state.Boot)
	state.BootEnabled = maps.Clone(state.BootEnabled)
	state.Media = maps.Clone(state.Media)
	state.Requests = maps.Clone(state.Requests)
	state.Resets = slices.Clone(state.Resets)
	return state
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s.intercept(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	if r.Method == http.MethodGet {
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, s.etag))
	}
	s.route(w, r)
}

func (s *Server) advance() {
	for id, pending := range s.pending {
		if !time.Now().Before(pending.at) {
			s.state.Power[id] = pending.target
			delete(s.pending, id)
			s.etag++
		}
	}
}

func (s *Server) intercept(w http.ResponseWriter, r *http.Request) bool {
	// The service root is discoverable without authentication, as in DSP0266.
	isRoot := strings.TrimSuffix(r.URL.Path, "/") == root
	if !isRoot && (s.config.Username != "" || s.config.Password != "") {
		user, pass, ok := r.BasicAuth()
		if !ok || user != s.config.Username || pass != s.config.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="redfishtest"`)
			writeError(w, http.StatusUnauthorized, nil, "authentication required")
			return true
		}
	}
	key := r.Method + " " + r.URL.Path
	s.mu.Lock()
	s.state.Requests[key]++
	fault := s.faults[key]
	if fault.Count > 0 {
		fault.Count--
		if fault.Count == 0 {
			delete(s.faults, key)
		} else {
			s.faults[key] = fault
		}
	}
	s.mu.Unlock()
	if fault.Delay > 0 {
		timer := time.NewTimer(fault.Delay)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return true
		case <-timer.C:
		}
	}
	if fault.Status != 0 {
		writeError(w, fault.Status, fault.MessageIDs, fault.Message)
		return true
	}
	return false
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if r.Method == http.MethodPatch || r.Method == http.MethodPost {
		match := r.Header.Get("If-Match")
		if (s.config.RequireETag && match == "") || (match != "" && match != fmt.Sprintf(`"%d"`, s.etag)) {
			writeError(w, http.StatusPreconditionFailed, []string{"Base.1.0.PreconditionFailed"}, "ETag mismatch")
			return
		}
	}
	switch {
	case path == root && r.Method == http.MethodGet:
		writeJSON(w, map[string]any{
			odataID: root + "/", resourceID: "RootService", resourceName: "Synthetic Redfish",
			"RedfishVersion": "1.17.0", "Systems": link(root + "/Systems"),
			"Managers": link(root + "/Managers"), "SessionService": link(root + "/SessionService"),
		})
	case path == root+"/Systems" && r.Method == http.MethodGet:
		writeCollection(w, path, s.ids)
	case strings.HasPrefix(path, root+"/Systems/"):
		s.system(w, r, path)
	case path == root+"/Managers" && r.Method == http.MethodGet:
		writeCollection(w, path, []string{"1"})
	case path == root+"/Managers/1" && r.Method == http.MethodGet:
		writeJSON(w, map[string]any{
			odataID: path, resourceID: "1", resourceName: "Synthetic manager",
			"VirtualMedia": link(path + "/VirtualMedia"),
		})
	case path == root+"/Managers/1/VirtualMedia" && r.Method == http.MethodGet:
		writeCollection(w, path, []string{"CD1", "DVD1"})
	case strings.HasPrefix(path, root+"/Managers/1/VirtualMedia/"):
		s.media(w, r, path)
	case path == root+"/SessionService":
		s.session(w, r, path)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) system(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.TrimPrefix(path, root+"/Systems/"), "/")
	id := parts[0]
	state, exists := s.state.Power[id]
	if !exists {
		http.NotFound(w, r)
		return
	}
	resource := root + "/Systems/" + id
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		writeJSON(w, map[string]any{
			odataID: resource, resourceID: id, resourceName: "Synthetic system",
			"Manufacturer": "Example manufacturer", "Model": "Example model", "PowerState": state,
			"Boot": map[string]string{
				"BootSourceOverrideEnabled": s.state.BootEnabled[id], "BootSourceOverrideTarget": s.state.Boot[id],
			},
			"Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{
				actionTarget:                        resource + "/Actions/ComputerSystem.Reset",
				"ResetType@Redfish.AllowableValues": []string{"On", "ForceOff", "GracefulShutdown", "ForceRestart"},
			}},
		})
	case len(parts) == 1 && r.Method == http.MethodPatch:
		var payload struct{ Boot map[string]string }
		if !decode(w, r, &payload) {
			return
		}
		if target, ok := payload.Boot["BootSourceOverrideTarget"]; ok {
			s.state.Boot[id] = target
		}
		if enabled, ok := payload.Boot["BootSourceOverrideEnabled"]; ok {
			s.state.BootEnabled[id] = enabled
		}
		s.etag++
		w.WriteHeader(http.StatusNoContent)
	case path == resource+"/Actions/ComputerSystem.Reset" && r.Method == http.MethodPost:
		var payload struct{ ResetType string }
		if !decode(w, r, &payload) {
			return
		}
		switch payload.ResetType {
		case "On", "ForceRestart":
			s.state.Power[id] = "On"
		case "ForceOff", "GracefulShutdown":
			s.state.Power[id] = offPowerState
		default:
			writeError(w, http.StatusBadRequest, []string{"Base.1.0.PropertyValueNotInList"}, "unsupported reset")
			return
		}
		s.state.Resets = append(s.state.Resets, Reset{id, payload.ResetType})
		if s.config.ResetDelay > 0 {
			target := s.state.Power[id]
			s.state.Power[id] = "PoweringOn"
			if target == offPowerState {
				s.state.Power[id] = "PoweringOff"
			}
			s.pending[id] = transition{target: target, at: time.Now().Add(s.config.ResetDelay)}
		}
		s.etag++
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) media(w http.ResponseWriter, r *http.Request, path string) {
	base := root + "/Managers/1/VirtualMedia/"
	parts := strings.Split(strings.TrimPrefix(path, base), "/")
	id := parts[0]
	media, exists := s.state.Media[id]
	if !exists {
		http.NotFound(w, r)
		return
	}
	resource := base + id
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		payload := map[string]any{
			odataID: resource, resourceID: id, resourceName: "Synthetic media",
			"MediaTypes": []string{"CD", "DVD"}, "Image": media.Image,
			"Inserted": media.Inserted, "WriteProtected": media.WriteProtected,
		}
		if !s.config.DisableMediaActions {
			payload["Actions"] = map[string]any{
				"#VirtualMedia.InsertMedia": map[string]string{actionTarget: resource + "/Actions/VirtualMedia.InsertMedia"},
				"#VirtualMedia.EjectMedia":  map[string]string{actionTarget: resource + "/Actions/VirtualMedia.EjectMedia"},
			}
		}
		writeJSON(w, payload)
	case len(parts) == 1 && r.Method == http.MethodPatch:
		var payload struct {
			Image          json.RawMessage
			Inserted       *bool
			WriteProtected *bool
		}
		if !decode(w, r, &payload) {
			return
		}
		if len(payload.Image) > 0 {
			if err := json.Unmarshal(payload.Image, &media.Image); err != nil {
				writeError(w, http.StatusBadRequest, nil, "invalid image")
				return
			}
			if string(payload.Image) == "null" {
				media.Image = ""
			}
		}
		if payload.Inserted != nil {
			media.Inserted = *payload.Inserted
		}
		if payload.WriteProtected != nil {
			media.WriteProtected = *payload.WriteProtected
		}
		s.state.Media[id] = media
		s.etag++
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && !s.config.DisableMediaActions &&
		path == resource+"/Actions/VirtualMedia.InsertMedia":
		var payload Media
		if !decode(w, r, &payload) {
			return
		}
		s.state.Media[id] = payload
		s.etag++
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && !s.config.DisableMediaActions &&
		path == resource+"/Actions/VirtualMedia.EjectMedia":
		s.state.Media[id] = Media{}
		s.etag++
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) session(w http.ResponseWriter, r *http.Request, path string) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			odataID: path, resourceID: "SessionService", resourceName: "Synthetic session service",
			"SessionTimeout": s.state.SessionTimeout, "Sessions": link(path + "/Sessions"),
		})
	case http.MethodPatch:
		var payload struct{ SessionTimeout *int64 }
		if !decode(w, r, &payload) {
			return
		}
		if payload.SessionTimeout != nil {
			s.state.SessionTimeout = *payload.SessionTimeout
		}
		s.etag++
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func link(path string) map[string]string {
	return map[string]string{odataID: path}
}

func writeCollection(w http.ResponseWriter, path string, ids []string) {
	members := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		members = append(members, link(path+"/"+id))
	}
	writeJSON(w, map[string]any{odataID: path, resourceName: "Collection", "Members": members, "Members@odata.count": len(members)})
}

func decode(w http.ResponseWriter, r *http.Request, payload any) bool {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(payload); err != nil {
		writeError(w, http.StatusBadRequest, nil, "invalid JSON")
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, nil, "invalid JSON")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, ids []string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusNoContent {
		return
	}
	infos := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		infos = append(infos, map[string]string{"MessageId": id, "Message": message})
	}
	writeJSON(w, map[string]any{"error": map[string]any{
		"code": "Base.1.0.GeneralError", "message": message, "@Message.ExtendedInfo": infos,
	}})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}
