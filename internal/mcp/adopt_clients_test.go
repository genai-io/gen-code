package mcp

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/genai-io/san/internal/mcp/transport"
)

// liveTransport stands in for a running MCP server: alive until Close.
type liveTransport struct {
	mu     sync.Mutex
	closed bool
}

func (t *liveTransport) Start(context.Context) error { return nil }
func (t *liveTransport) Send(context.Context, *transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	return &transport.JSONRPCResponse{}, nil
}
func (t *liveTransport) SendNotification(context.Context, *transport.JSONRPCNotification) error {
	return nil
}
func (t *liveTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return nil
}
func (t *liveTransport) IsAlive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.closed
}
func (t *liveTransport) SetNotificationHandler(transport.NotificationHandler) {}

func (t *liveTransport) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// connectedClient builds a Client already in the connected state, backed by tr.
func connectedClient(t *testing.T, cfg ServerConfig, tr transport.Transport) *Client {
	t.Helper()
	c := NewClient(cfg)
	c.TransportFactory = func() (transport.Transport, error) { return tr, nil }
	c.mu.Lock()
	c.transport = tr
	c.connected = true
	c.mu.Unlock()
	return c
}

func registryWith(configs map[string]ServerConfig, clients map[string]*Client) *Registry {
	r := newEmptyRegistry()
	maps.Copy(r.configs, configs)
	for name, c := range clients {
		r.clients[name] = c
		r.getOrCreateConnectionState(name).retainWithoutLeases = true
	}
	return r
}

func registryWithRetainedClients(configs map[string]ServerConfig, clients map[string]*Client) *Registry {
	return registryWith(configs, clients)
}

// A user-level MCP server is configured in both the old project and the new
// one. Replacing the registry on a cwd change used to drop its connection, so
// every mcp__* tool disappeared from the agent for the rest of the session with
// nothing to reconnect it — AutoConnect only runs at startup.
func TestAdoptLiveClientsKeepsUnchangedServersConnected(t *testing.T) {
	cfg := ServerConfig{Name: "docs", Type: "stdio", Command: "docs-server", Args: []string{"--stdio"}}
	tr := &liveTransport{}
	old := registryWith(
		map[string]ServerConfig{"docs": cfg},
		map[string]*Client{"docs": connectedClient(t, cfg, tr)},
	)

	// Same server still configured after the cwd change.
	fresh := registryWith(map[string]ServerConfig{"docs": cfg}, nil)
	fresh.adoptLiveClients(old)

	if _, ok := fresh.clients["docs"]; !ok {
		t.Fatal("the live connection was not carried across; mcp__docs__* tools would vanish")
	}
	if tr.isClosed() {
		t.Error("an unchanged server's transport was closed")
	}
	if len(old.clients) != 0 {
		t.Error("the outgoing registry still owns the client; both would think they own it")
	}
}

// A project-scoped server that the new project does not configure must be torn
// down — it was previously dropped still running, leaking the subprocess.
func TestAdoptLiveClientsDisconnectsServersTheNewProjectDropped(t *testing.T) {
	cfg := ServerConfig{Name: "old-proj", Type: "stdio", Command: "old-server"}
	tr := &liveTransport{}
	old := registryWith(
		map[string]ServerConfig{"old-proj": cfg},
		map[string]*Client{"old-proj": connectedClient(t, cfg, tr)},
	)

	fresh := registryWith(map[string]ServerConfig{}, nil) // new project configures nothing
	fresh.adoptLiveClients(old)

	if _, ok := fresh.clients["old-proj"]; ok {
		t.Error("a server the new project does not configure was adopted")
	}
	waitFor(t, "the dropped server's transport to close", tr.isClosed)
}

// Same name, different command: the config changed, so the old process is not
// the server the new project asked for.
func TestAdoptLiveClientsRejectsAChangedConfig(t *testing.T) {
	oldCfg := ServerConfig{Name: "docs", Type: "stdio", Command: "docs-server", Args: []string{"--v1"}}
	newCfg := ServerConfig{Name: "docs", Type: "stdio", Command: "docs-server", Args: []string{"--v2"}}
	tr := &liveTransport{}
	old := registryWith(
		map[string]ServerConfig{"docs": oldCfg},
		map[string]*Client{"docs": connectedClient(t, oldCfg, tr)},
	)

	fresh := registryWith(map[string]ServerConfig{"docs": newCfg}, nil)
	fresh.adoptLiveClients(old)

	if _, ok := fresh.clients["docs"]; ok {
		t.Error("adopted a connection whose configuration no longer matches")
	}
	waitFor(t, "the stale server's transport to close", tr.isClosed)
}

func TestTransferredConnectionNotifiesIncomingRegistryOnly(t *testing.T) {
	cfg := ServerConfig{Name: "docs", Type: "stdio", Command: "docs-server"}
	tr := &liveTransport{}
	client := connectedClient(t, cfg, tr)
	old := registryWithRetainedClients(map[string]ServerConfig{"docs": cfg}, map[string]*Client{"docs": client})
	fresh := registryWithRetainedClients(map[string]ServerConfig{"docs": cfg}, nil)

	var oldCalls, freshCalls int
	old.SetOnToolsChanged(func() { oldCalls++ })
	fresh.SetOnToolsChanged(func() { freshCalls++ })
	client.SetOnToolsChanged(old.notifyToolsChanged)
	fresh.transferRetainedConnectionsFrom(old)

	client.mu.RLock()
	callback := client.onToolsChanged
	client.mu.RUnlock()
	callback()
	if oldCalls != 0 || freshCalls != 1 {
		t.Fatalf("tools-changed callbacks = old:%d fresh:%d, want old:0 fresh:1", oldCalls, freshCalls)
	}
}

func TestRegistryReplacementLeavesLeaseOwnedConnectionInOutgoingRegistry(t *testing.T) {
	cfg := ServerConfig{Name: "docs", Type: "stdio", Command: "docs-server"}
	tr := &liveTransport{}
	client := connectedClient(t, cfg, tr)
	old := registryWithRetainedClients(map[string]ServerConfig{"docs": cfg}, map[string]*Client{"docs": client})
	old.getOrCreateConnectionState("docs").disconnectAfterFinalLease = true
	s := old.connectionStates["docs"]
	if s != nil {
		s.retainWithoutLeases = false
	}
	old.getOrCreateConnectionState("docs").leaseCountsByEpoch = map[uint64]int{0: 1}

	fresh := registryWithRetainedClients(map[string]ServerConfig{"docs": cfg}, nil)
	fresh.transferRetainedConnectionsFrom(old)

	if _, ok := fresh.clients["docs"]; ok {
		t.Fatal("new registry adopted a temporary connection still owned by an active Agent")
	}
	if got := old.clients["docs"]; got != client {
		t.Fatal("old registry lost the temporary connection before its lease cleanup")
	}
	if tr.isClosed() {
		t.Fatal("reload closed a temporary connection still used by an active Agent")
	}
}

// waitFor polls cond, since the teardown of servers that did not survive runs
// detached — Disconnect can block for seconds and Initialize is on the UI
// goroutine.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %s", what)
}
