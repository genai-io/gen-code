// MCP server selector: model, state, runtime, and keyboard handling.
package input

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/log"
	coremcp "github.com/genai-io/san/internal/mcp"
)

// ── State ───────────────────────────────────────────────────────────

// MCPState holds all MCP-related state for the TUI model.
type MCPState struct {
	Selector      MCPSelector
	EditingFile   string        // temp file being edited
	EditingServer string        // server name being edited
	EditingScope  coremcp.Scope // scope of the server being edited
}

// mcpEditorFinishedMsg is sent when the external MCP config editor closes.
type mcpEditorFinishedMsg struct {
	Err error
}

// ── Private types ───────────────────────────────────────────────────

// mcpSelectorLevel represents the navigation level in the MCP selector
type mcpSelectorLevel int

const (
	mcpLevelList   mcpSelectorLevel = iota // Server list view
	mcpLevelDetail                         // Server detail + actions view
)

// mcpAction represents an action available for a server in detail view.
type mcpAction struct {
	Label  string
	Action string // "edit", "connect", "disconnect", "reconnect", "remove"
}

// mcpServerItem represents an MCP server in the selector
type mcpServerItem struct {
	Name      string
	Type      string // stdio, http, sse
	Status    coremcp.ServerStatus
	ToolCount int
	Error     string
	Scope     string   // user, project, local
	URL       string   // for http/sse
	Command   string   // for stdio
	Args      []string // for stdio
}

// ── Model ───────────────────────────────────────────────────────────

// MCPSelector holds state for the MCP server selector
type MCPSelector struct {
	registry *coremcp.Registry

	active          bool
	servers         []mcpServerItem
	filteredServers []mcpServerItem
	nav             kit.ListNav
	width           int
	height          int
	connecting      bool   // True when a connection is in progress
	lastError       string // Last connection error to display

	// Two-level navigation
	level        mcpSelectorLevel
	parentIdx    int            // selected index when entering detail
	detailServer *mcpServerItem // server shown in detail view
	actions      []mcpAction    // context-sensitive action menu
	actionIdx    int            // selected action
}

// ── Message types ───────────────────────────────────────────────────

// mcpConnectMsg is sent when connecting to a server
type mcpConnectMsg struct {
	ServerName string
}

// mcpConnectResultMsg is sent when connection completes
type mcpConnectResultMsg struct {
	ServerName string
	Success    bool
	ToolCount  int
	Err        error
}

// mcpDisconnectMsg is sent when disconnecting from a server
type mcpDisconnectMsg struct {
	ServerName string
}

// mcpReconnectMsg is sent when reconnecting to a server
type mcpReconnectMsg struct {
	ServerName string
}

// mcpRemoveMsg is sent when removing a server
type mcpRemoveMsg struct {
	ServerName string
}

// mcpAddServerMsg is sent when the user presses ctrl+a to add a new server
type mcpAddServerMsg struct{}

// mcpEditServerMsg is sent when the user chooses to edit a server's config
type mcpEditServerMsg struct {
	ServerName string
	Scope      string
}

// ── Constructor ─────────────────────────────────────────────────────

// NewMCPSelector creates a new MCPSelector with the given MCP registry.
func NewMCPSelector(reg *coremcp.Registry) MCPSelector {
	return MCPSelector{
		registry: reg,
		active:   false,
		servers:  []mcpServerItem{},
		nav:      kit.ListNav{MaxVisible: 10},
	}
}

// ── Model methods ───────────────────────────────────────────────────

// EnterSelect enters MCP server selection mode
func (s *MCPSelector) EnterSelect(width, height int) error {
	if s.registry == nil {
		return fmt.Errorf("MCP is not initialized")
	}

	s.refreshServers()
	s.active = true
	s.width = width
	s.height = height
	s.connecting = false
	s.lastError = ""
	s.nav.Reset()
	s.nav.Total = len(s.filteredServers)
	s.level = mcpLevelList
	s.parentIdx = 0
	s.detailServer = nil
	s.actions = nil
	s.actionIdx = 0

	return nil
}

// AutoReconnect returns a batch command to reconnect servers in error state.
// Disconnected servers are left as-is since the user intentionally disconnected them.
func (s *MCPSelector) AutoReconnect() tea.Cmd {
	var cmds []tea.Cmd
	for _, srv := range s.servers {
		if srv.Status == coremcp.StatusError {
			s.registry.SetConnecting(srv.Name, true)
			cmds = append(cmds, mcpStartConnect(s.registry, srv.Name))
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// refreshServers refreshes the server list from registry
func (s *MCPSelector) refreshServers() {
	servers := s.registry.List()
	s.servers = make([]mcpServerItem, 0, len(servers))

	for _, srv := range servers {
		item := mcpServerItem{
			Name:    srv.Config.Name,
			Type:    string(srv.Config.GetType()),
			Status:  srv.Status,
			Error:   srv.Error,
			Scope:   string(srv.Config.Scope),
			URL:     srv.Config.URL,
			Command: srv.Config.Command,
			Args:    srv.Config.Args,
		}
		if srv.Status == coremcp.StatusConnected {
			item.ToolCount = len(srv.Tools)
		}
		s.servers = append(s.servers, item)
	}
	s.updateFilter()
}

// IsActive returns whether the selector is active
func (s *MCPSelector) IsActive() bool {
	return s.active
}

// Cancel cancels the selector
func (s *MCPSelector) Cancel() {
	s.active = false
	s.servers = []mcpServerItem{}
	s.filteredServers = nil
	s.nav.Reset()
	s.nav.Total = 0
	s.connecting = false
	s.level = mcpLevelList
	s.detailServer = nil
	s.actions = nil
	s.actionIdx = 0
}

// updateFilter filters servers based on search query (fuzzy match)
func (s *MCPSelector) updateFilter() {
	if s.nav.Search == "" {
		s.filteredServers = s.servers
	} else {
		query := strings.ToLower(s.nav.Search)
		s.filteredServers = make([]mcpServerItem, 0)
		for _, srv := range s.servers {
			if kit.FuzzyMatch(strings.ToLower(srv.Name), query) ||
				kit.FuzzyMatch(strings.ToLower(srv.Type), query) {
				s.filteredServers = append(s.filteredServers, srv)
			}
		}
	}
	s.nav.ResetCursor()
	s.nav.Total = len(s.filteredServers)
}

// MoveUp moves the selection up (level-aware)
func (s *MCPSelector) MoveUp() {
	if s.level == mcpLevelDetail {
		if s.actionIdx > 0 {
			s.actionIdx--
		}
		return
	}
	s.nav.MoveUp()
}

// MoveDown moves the selection down (level-aware)
func (s *MCPSelector) MoveDown() {
	if s.level == mcpLevelDetail {
		if s.actionIdx < len(s.actions)-1 {
			s.actionIdx++
		}
		return
	}
	s.nav.MoveDown()
}

// enterDetail enters the detail view for the selected server
func (s *MCPSelector) enterDetail() {
	if len(s.filteredServers) == 0 || s.nav.Selected >= len(s.filteredServers) {
		return
	}
	s.parentIdx = s.nav.Selected
	srv := s.filteredServers[s.nav.Selected]
	s.detailServer = &srv
	s.actions = s.buildActions(srv)
	s.actionIdx = 0
	s.level = mcpLevelDetail
}

// goBack returns to the list view from detail view
func (s *MCPSelector) goBack() bool {
	if s.level == mcpLevelDetail {
		s.level = mcpLevelList
		s.nav.Selected = s.parentIdx
		s.detailServer = nil
		s.actions = nil
		s.actionIdx = 0
		s.lastError = ""
		return true
	}
	return false
}

// buildActions returns context-sensitive actions for a server
func (s *MCPSelector) buildActions(srv mcpServerItem) []mcpAction {
	edit := mcpAction{Label: "Edit", Action: "edit"}
	switch srv.Status {
	case coremcp.StatusConnected:
		return []mcpAction{
			edit,
			{Label: "Disable", Action: "disconnect"},
			{Label: "Reconnect", Action: "reconnect"},
			{Label: "Remove", Action: "remove"},
		}
	case coremcp.StatusConnecting:
		return []mcpAction{
			edit,
			{Label: "Disable", Action: "disconnect"},
			{Label: "Remove", Action: "remove"},
		}
	default: // Error or Disconnected
		return []mcpAction{
			edit,
			{Label: "Connect", Action: "connect"},
			{Label: "Remove", Action: "remove"},
		}
	}
}

// executeAction executes the currently selected action in detail view
func (s *MCPSelector) executeAction() tea.Cmd {
	if s.detailServer == nil || s.actionIdx >= len(s.actions) || s.connecting {
		return nil
	}

	action := s.actions[s.actionIdx]
	name := s.detailServer.Name

	switch action.Action {
	case "edit":
		scope := s.detailServer.Scope
		s.Cancel()
		return func() tea.Msg {
			return mcpEditServerMsg{ServerName: name, Scope: scope}
		}
	case "connect":
		s.connecting = true
		return func() tea.Msg { return mcpConnectMsg{ServerName: name} }
	case "disconnect":
		return func() tea.Msg { return mcpDisconnectMsg{ServerName: name} }
	case "reconnect":
		s.connecting = true
		return func() tea.Msg { return mcpReconnectMsg{ServerName: name} }
	case "remove":
		return func() tea.Msg { return mcpRemoveMsg{ServerName: name} }
	}
	return nil
}

// HandleConnectResult handles the result of a connection attempt
func (s *MCPSelector) HandleConnectResult(msg mcpConnectResultMsg) {
	s.connecting = false
	if msg.Success {
		s.lastError = ""
	} else if msg.Err != nil {
		s.lastError = fmt.Sprintf("Failed to connect: %v", msg.Err)
	}
	s.refreshAndUpdateView()
}

// HandleDisconnect handles a disconnect (disable) request.
// Marks the server as disabled so it won't auto-connect on restart.
func (s *MCPSelector) HandleDisconnect(name string) {
	if s.registry != nil {
		s.registry.Disconnect(name)
		s.registry.SetDisabled(name, true)
	}
	s.refreshAndUpdateView()
}

// HandleReconnect handles a reconnect request.
func (s *MCPSelector) HandleReconnect(name string) {
	if s.registry != nil {
		s.registry.Disconnect(name)
	}
	s.refreshAndUpdateView()
}

// HandleRemove handles a remove request
func (s *MCPSelector) HandleRemove(name string) {
	if s.registry != nil {
		s.registry.SetDisabled(name, false)
		if err := s.registry.RemoveServer(name); err != nil {
			log.Logger().Warn("failed to remove MCP server", zap.String("server", name), zap.Error(err))
		}
	}
	s.refreshServers()
	s.goBack()
	s.clampSelectedIdx()
}

// refreshAndUpdateView refreshes servers and updates the detail view if active
func (s *MCPSelector) refreshAndUpdateView() {
	s.refreshServers()
	if s.level == mcpLevelDetail && s.detailServer != nil {
		s.refreshDetailView()
	}
}

// clampSelectedIdx ensures selectedIdx is within valid bounds
func (s *MCPSelector) clampSelectedIdx() {
	if s.nav.Selected >= len(s.filteredServers) && len(s.filteredServers) > 0 {
		s.nav.Selected = len(s.filteredServers) - 1
	}
}

// refreshDetailView updates the detail server and actions after a state change
func (s *MCPSelector) refreshDetailView() {
	if s.detailServer == nil {
		return
	}
	name := s.detailServer.Name
	for _, srv := range s.filteredServers {
		if srv.Name == name {
			s.detailServer = &srv
			s.actions = s.buildActions(srv)
			s.clampActionIdx()
			return
		}
	}
	// Server no longer in list (removed or filtered out) - go back
	s.goBack()
}

// clampActionIdx ensures actionIdx is within valid bounds
func (s *MCPSelector) clampActionIdx() {
	if s.actionIdx >= len(s.actions) {
		s.actionIdx = len(s.actions) - 1
	}
	if s.actionIdx < 0 {
		s.actionIdx = 0
	}
}

// AutoConnect returns a batch of commands to connect all configured MCP servers,
// skipping servers that the user has explicitly disabled.
func (s *MCPSelector) AutoConnect() tea.Cmd {
	if s.registry == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, srv := range s.registry.List() {
		name := srv.Config.Name
		if !s.registry.IsDisabled(name) {
			s.registry.SetConnecting(name, true)
			cmds = append(cmds, mcpStartConnect(s.registry, name))
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// mcpStartConnect returns a tea.Cmd that connects to an MCP server.
func mcpStartConnect(reg *coremcp.Registry, name string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if reg == nil {
			return mcpConnectResultMsg{
				ServerName: name,
				Success:    false,
				Err:        fmt.Errorf("MCP not initialized"),
			}
		}

		if err := reg.Connect(ctx, name); err != nil {
			return mcpConnectResultMsg{
				ServerName: name,
				Success:    false,
				Err:        err,
			}
		}

		toolCount := 0
		if client, ok := reg.GetClient(name); ok {
			toolCount = len(client.GetCachedTools())
		}

		return mcpConnectResultMsg{
			ServerName: name,
			Success:    true,
			ToolCount:  toolCount,
		}
	}
}

// ── Runtime interface ───────────────────────────────────────────────

// UpdateMCP routes MCP server management messages.
func UpdateMCP(deps OverlayDeps, state *MCPState, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case mcpConnectMsg:
		if state.Selector.registry != nil {
			state.Selector.registry.SetDisabled(msg.ServerName, false)
			state.Selector.registry.SetConnecting(msg.ServerName, true)
		}
		return mcpStartConnect(state.Selector.registry, msg.ServerName), true

	case mcpConnectResultMsg:
		if state.Selector.registry != nil {
			state.Selector.registry.SetConnecting(msg.ServerName, false)
			if !msg.Success && msg.Err != nil {
				state.Selector.registry.SetConnectError(msg.ServerName, msg.Err.Error())
			} else {
				state.Selector.registry.SetConnectError(msg.ServerName, "")
			}
		}
		state.Selector.HandleConnectResult(msg)
		if !state.Selector.IsActive() && !msg.Success {
			content := fmt.Sprintf("Failed to connect to '%s': %v", msg.ServerName, msg.Err)
			deps.Conv.Append(core.ChatMessage{Role: core.ChatNotice, Content: content})
			return tea.Batch(deps.CommitMessages()...), true
		}
		return nil, true

	case mcpDisconnectMsg:
		state.Selector.HandleDisconnect(msg.ServerName)
		return nil, true

	case mcpReconnectMsg:
		state.Selector.HandleReconnect(msg.ServerName)
		if state.Selector.registry != nil {
			state.Selector.registry.SetConnecting(msg.ServerName, true)
		}
		return mcpStartConnect(state.Selector.registry, msg.ServerName), true

	case mcpRemoveMsg:
		state.Selector.HandleRemove(msg.ServerName)
		return nil, true

	case mcpAddServerMsg:
		deps.State.Textarea.SetValue("/mcp add ")
		return nil, true

	case mcpEditServerMsg:
		info, err := coremcp.PrepareServerEdit(state.Selector.registry, msg.ServerName)
		if err != nil {
			deps.Conv.Append(core.ChatMessage{Role: core.ChatNotice, Content: fmt.Sprintf("Error: %v", err)})
			return tea.Batch(deps.CommitMessages()...), true
		}
		state.EditingFile = info.TempFile
		state.EditingServer = info.ServerName
		state.EditingScope = info.Scope
		return StartMCPEditor(info.TempFile), true

	case mcpEditorFinishedMsg:
		info := &coremcp.EditInfo{
			TempFile:   state.EditingFile,
			ServerName: state.EditingServer,
			Scope:      state.EditingScope,
		}
		state.EditingFile, state.EditingServer, state.EditingScope = "", "", ""

		if msg.Err != nil {
			os.Remove(info.TempFile)
			deps.Conv.Append(core.ChatMessage{Role: core.ChatNotice, Content: fmt.Sprintf("Editor error: %v", msg.Err)})
			return tea.Batch(deps.CommitMessages()...), true
		}

		if err := coremcp.ApplyServerEdit(state.Selector.registry, info); err != nil {
			deps.Conv.Append(core.ChatMessage{Role: core.ChatNotice, Content: fmt.Sprintf("Failed to apply edit: %v", err)})
			return tea.Batch(deps.CommitMessages()...), true
		}

		deps.Conv.Append(core.ChatMessage{Role: core.ChatNotice, Content: fmt.Sprintf("Updated MCP server '%s'", info.ServerName)})
		return tea.Batch(deps.CommitMessages()...), true
	}
	return nil, false
}

// StartMCPEditor launches the external editor for an MCP config file.
// Exported for use by command handlers in the parent app package.
func StartMCPEditor(filePath string) tea.Cmd {
	return kit.StartExternalEditor(filePath, func(err error) tea.Msg {
		return mcpEditorFinishedMsg{Err: err}
	})
}

// ── Keyboard handling ───────────────────────────────────────────────

// HandleKeypress handles a keypress and returns a command if needed
func (s *MCPSelector) HandleKeypress(key tea.KeyMsg) tea.Cmd {
	// Only allow escape while connecting
	if s.connecting {
		if key.String() == "esc" {
			s.Cancel()
			return func() tea.Msg { return kit.DismissedMsg{} }
		}
		return nil
	}

	// Detail view keypress handling
	if s.level == mcpLevelDetail {
		return s.handleDetailKeypress(key)
	}

	// List view keypress handling
	return s.handleListKeypress(key)
}

// handleDetailKeypress handles keypresses in the detail view
func (s *MCPSelector) handleDetailKeypress(key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "up", "ctrl+p":
		s.MoveUp()
		return nil
	case "down", "ctrl+n":
		s.MoveDown()
		return nil
	case "enter":
		return s.executeAction()
	case "esc", "left":
		s.goBack()
		return nil
	case "k":
		s.MoveUp()
		return nil
	case "j":
		s.MoveDown()
		return nil
	case "h":
		s.goBack()
		return nil
	}
	return nil
}

// handleListKeypress handles keypresses in the list view
func (s *MCPSelector) handleListKeypress(key tea.KeyMsg) tea.Cmd {
	// Selector-specific action keys. ctrl+n is intentionally NOT add-server
	// here: it is unified to "move down" across all selectors via ListNav, so
	// add lives on ctrl+a.
	switch key.String() {
	case "ctrl+a":
		s.Cancel()
		return func() tea.Msg { return mcpAddServerMsg{} }
	case "ctrl+d":
		if len(s.filteredServers) > 0 && s.nav.Selected < len(s.filteredServers) {
			name := s.filteredServers[s.nav.Selected].Name
			return func() tea.Msg { return mcpRemoveMsg{ServerName: name} }
		}
		return nil
	case "enter", "right":
		s.enterDetail()
		return nil
	}

	// vim 'l' opens detail when not searching (j/k are handled by ListNav).
	if s.nav.Search == "" && key.String() == "l" {
		s.enterDetail()
		return nil
	}

	// Shared core: up/down, ctrl+p/ctrl+n, j/k, esc-clears-search, backspace,
	// and type-to-search — identical to every other list selector.
	if searchChanged, consumed := s.nav.HandleKey(key); consumed {
		if searchChanged {
			s.updateFilter()
		}
		return nil
	}

	if key.String() == "esc" {
		s.Cancel()
		return func() tea.Msg { return kit.DismissedMsg{} }
	}
	return nil
}
