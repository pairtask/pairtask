package chat

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Finding colleagues (#284). /pair open --discoverable announces the
// invitation by this workspace's name on the local network while it is open;
// /pair find lists the named invitations the host helper hears and the online
// tailnet devices, and picking one fills `/pair redeem HOST:PORT ` for the
// code. Nothing listed is verified: the pairing exchange decides who is there.

const (
	pairFind               = protocol.ContactOpFind
	kindContactFind        = "contact find"
	kindContactFindRefresh = "contact find refresh"
	panelFind              = "find"
	// The panel re-reads the broker's snapshot every refreshEvery while it
	// is open, at most refreshesMax times, inside the broker's FindWindow
	// so a re-read never starts a new one: the helper reports within a
	// lease or two, and the operator should not have to ask again.
	refreshEvery = 5 * time.Second
	refreshesMax = 22
	// discoverableWord is open's one option.
	discoverableWord = "--discoverable"
	// tailnetPort is the published port a tailnet device is tried on; the
	// operator edits it on the line when the host uses another.
	tailnetPort = 7445
)

// discoveryView is the helper's latest snapshot as the broker served it.
type discoveryView struct {
	snapshot *protocol.ContactFind
	// endpoints are the picker's items in order: LAN invitations, then
	// tailnet devices; labels looks each one up.
	endpoints []string
	labels    map[string]string
	refreshes int // re-reads since the last /pair find
}

// discoveryRefreshMsg is the panel's timer.
type discoveryRefreshMsg struct{}

// openCommand is /pair open [--discoverable]: a private invitation, or one
// announced by name while open, refused here on a broker without it.
func (m *Model) openCommand(fields pairWords) tea.Cmd {
	if len(fields) == 2 && fields[1] != discoverableWord {
		m.status = "use /pair open, or /pair open --discoverable to announce the invitation on this network"
		return nil
	}
	discoverable := len(fields) == 2
	if discoverable && !m.session.HasCapability(protocol.CapabilityPairingDiscoverable) {
		m.status = "this broker can't announce an invitation; update it (pairtask upgrade), then /pair open --discoverable"
		return nil
	}
	m.status = "pairing open…"
	return m.request(kindPairing, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpOpen,
		PairingDiscoverable: discoverable}, protocol.PairingOpOpen)
}

// findCommand is /pair find: it reads the helper's snapshot; nothing is
// dialled until the operator redeems with a code.
func (m *Model) pairFindCommand(pairWords) tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityContactFind) {
		m.status = "this broker can't find colleagues; update it (pairtask upgrade), then /pair find"
		return nil
	}
	m.status = "looking on this network and the tailnet…"
	return m.request(kindContactFind, protocol.Message{Type: protocol.TypeOperatorContact, MessageID: uuid.NewString(), ContactOp: pairFind})
}

// findResult shows the snapshot and offers its endpoints for redeem.
func (m *Model) discoveryResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactFind == nil {
		m.status = "pairing find refused: " + brokerReason(resp)
		return nil
	}
	m.discovery = m.discoveryViewOf(*resp.ContactFind)
	m.panel = &panel{kind: panelFind}
	switch {
	case !m.helperAlive(): // the panel keeps re-reading: a helper started now shows up
		m.status = "discovery helper is off or not installed: pairtask pairing on --discovery on the host"
	case len(m.discovery.endpoints) == 0:
		m.status = "looking on this network and the tailnet; the helper reports within seconds"
	default:
		m.status = "pick one to fill /pair redeem; then type the code you were given"
		m.offerRedeemEndpoints()
	}
	return m.scheduleDiscoveryRefresh()
}

// scheduleDiscoveryRefresh re-reads the snapshot while the panel is open.
func (m *Model) scheduleDiscoveryRefresh() tea.Cmd {
	if m.discovery.refreshes >= refreshesMax {
		return nil
	}
	m.discovery.refreshes++
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return discoveryRefreshMsg{} })
}

// refreshDiscovery is the timer: a quiet re-read while the find panel is
// still open, nothing once it closed.
func (m *Model) refreshDiscovery() tea.Cmd {
	if !m.findPanelOpen() || m.requested(kindContactFindRefresh, pairFind) {
		return nil
	}
	return m.request(kindContactFindRefresh, protocol.Message{Type: protocol.TypeOperatorContact, MessageID: uuid.NewString(), ContactOp: pairFind}, pairFind)
}

func (m *Model) findPanelOpen() bool {
	return m.panel != nil && m.panel.kind == panelFind
}

// discoveryRefreshed takes a re-read: new rows join the panel and, when the
// operator is still on the redeem line, the picker; a failure ends the
// refreshing and says so.
func (m *Model) discoveryRefreshed(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactFind == nil {
		m.status = "pairing find refused: " + brokerReason(resp)
		return nil
	}
	refreshes := m.discovery.refreshes
	m.discovery = m.discoveryViewOf(*resp.ContactFind)
	m.discovery.refreshes = refreshes
	if !m.findPanelOpen() {
		return nil
	}
	m.offerRefreshedEndpoints()
	return m.scheduleDiscoveryRefresh()
}

// offerRefreshedEndpoints puts new rows where the operator is: into the
// picker on the redeem line, or onto a fresh redeem line when no command
// line is open; a line they are typing on is left alone.
func (m *Model) offerRefreshedEndpoints() {
	if len(m.discovery.endpoints) == 0 {
		return
	}
	switch {
	case m.onRedeemLine():
		m.refreshCommandPicker()
	case m.compose.cmd == nil:
		m.status = "pick one to fill /pair redeem; then type the code you were given"
		m.offerRedeemEndpoints()
	}
}

// onRedeemLine: the command line waits for redeem's endpoint.
func (m *Model) onRedeemLine() bool {
	if m.compose.cmd == nil {
		return false
	}
	l, ok := splitArgLine(m.compose.cmd.input.Value())
	return ok && l.name == "pair" && len(l.words) == 1 && l.words[0] == protocol.PairingOpRedeem
}

// helperAlive: the broker heard from the helper within DiscoveryHelperStale.
func (m *Model) helperAlive() bool {
	s := m.discovery.snapshot
	return s != nil && s.HelperSeenAt != nil && time.Now().Sub(*s.HelperSeenAt) < protocol.DiscoveryHelperStale
}

// discoveryViewOf indexes the snapshot's rows by the endpoint each one fills.
func (m *Model) discoveryViewOf(s protocol.ContactFind) discoveryView {
	v := discoveryView{snapshot: &s, labels: map[string]string{}}
	for _, inv := range s.Invitations {
		endpoint := net.JoinHostPort(inv.Host, strconv.Itoa(inv.Port))
		v.add(endpoint, fmt.Sprintf("  %s  %s  %s", safe(inv.Name), safe(endpoint), mutedStyle.Render("open invitation on this network · seen "+when(inv.SeenAt))))
	}
	for _, d := range s.Devices {
		// The name first, then the address: the broker's DNS (Colima's,
		// on macOS) may not resolve MagicDNS, and the address always works.
		for _, host := range []string{d.Name, d.Address} {
			if host == "" {
				continue
			}
			endpoint := net.JoinHostPort(host, strconv.Itoa(tailnetPort))
			v.add(endpoint, fmt.Sprintf("  %s  %s  %s", safe(d.Name), safe(endpoint), mutedStyle.Render("tailnet device, unverified · edit the port if theirs differs")))
		}
	}
	return v
}

func (v *discoveryView) add(endpoint, label string) {
	if _, dup := v.labels[endpoint]; dup {
		return
	}
	v.endpoints = append(v.endpoints, endpoint)
	v.labels[endpoint] = label
}

// offerRedeemEndpoints opens `/pair redeem ` with the found endpoints to
// pick from; Enter fills one and waits for the code, it never sends.
func (m *Model) offerRedeemEndpoints() {
	if m.compose.cmd == nil {
		m.openCommandLine()
	}
	m.compose.cmd.set("pair " + protocol.PairingOpRedeem + " ") // the line's prompt is the slash
	m.refreshCommandPicker()
}

// endpointChoices are redeem's first word after a find: the endpoints seen.
func (m *Model) endpointChoices(l argLine) []string {
	if len(l.words) != 1 {
		return nil
	}
	return withPrefix(m.discovery.endpoints, l.current)
}

// endpointLabel labels a found endpoint in the picker.
func (m *Model) endpointLabel(item string) (string, bool) {
	label, ok := m.discovery.labels[item]
	return label, ok
}

// findRows are the /pair find panel: the two groups, each row as the helper
// reported it, and what the listing does and doesn't mean.
func (m *Model) discoveryRows() []string {
	rows := []string{titleStyle.Render("Find colleagues") + mutedStyle.Render("  (Esc closes)")}
	s := m.discovery.snapshot
	if s == nil || !m.helperAlive() {
		return append(rows, "  discovery helper is off or not installed; on the host: pairtask pairing on --discovery",
			"  or redeem by hand: /pair redeem HOST:PORT CODE")
	}
	rows = append(rows, mutedStyle.Render("  helper reported "+when(*s.HelperSeenAt)+"; rows are announcements, not identities: the code and the key decide"), "",
		titleStyle.Render("  Open invitations on this network"))
	if len(s.Invitations) == 0 {
		rows = append(rows, mutedStyle.Render("    none heard; your colleague runs /pair open --discoverable on the same network"))
	}
	for _, inv := range s.Invitations {
		rows = append(rows, fmt.Sprintf("    %s  %s  seen %s", safe(inv.Name), safe(net.JoinHostPort(inv.Host, strconv.Itoa(inv.Port))), when(inv.SeenAt)))
	}
	rows = append(rows, "", titleStyle.Render("  Tailnet devices (unverified)"))
	if len(s.Devices) == 0 {
		rows = append(rows, mutedStyle.Render("    none online, or Tailscale isn't installed on the host"))
	}
	for _, d := range s.Devices {
		rows = append(rows, "    "+strings.TrimSpace(safe(d.Name)+"  "+safe(d.Address)))
	}
	return append(rows, "", mutedStyle.Render("  pick a row below to fill /pair redeem HOST:PORT, then type the code; a tailnet row uses port 7445 unless you change it"))
}

// invitationNote says how a discoverable invitation stands, for /pair status.
func invitationNote(inv *protocol.InvitationView) string {
	if !inv.Discoverable {
		return ""
	}
	switch inv.AnnounceState {
	case protocol.AnnounceAnnouncing:
		return fmt.Sprintf(" · announcing %q on this network", inv.PublicName)
	case protocol.AnnounceFailed:
		return fmt.Sprintf(" · announcing %q failed on the host; pairtask discovery status", inv.PublicName)
	case protocol.AnnounceHelperLost:
		return fmt.Sprintf(" · announcing %q requested, but the helper went quiet; pairtask discovery status", inv.PublicName)
	case protocol.AnnounceRequested:
		return fmt.Sprintf(" · announcing %q requested; the host confirms within seconds", inv.PublicName)
	}
	return fmt.Sprintf(" · discoverable as %q", inv.PublicName)
}

// identityNote warns when a redemption's peer differs from a contact this
// side already holds under the same name or address: a hint to check, never
// a judgement; names collide and addresses get reused.
func (m *Model) identityNote(j protocol.PairingJoinView) string {
	if j.Peer == "" || m.pairing.snapshot == nil {
		return ""
	}
	for _, c := range m.pairing.snapshot.Contacts {
		if c.IdentityKey == j.Peer {
			return ""
		}
		if matched := matchedBy(c, j); matched != "" {
			return " · identity differs from your contact " + safe(c.Name) + " (matched by " + matched + ")"
		}
	}
	return ""
}

// matchedBy: what a redemption shares with a contact, "address" or "name",
// or nothing.
func matchedBy(c protocol.PairingContactView, j protocol.PairingJoinView) string {
	switch {
	case c.Destination != "" && c.Destination == j.Destination:
		return "address"
	case c.Name != "" && c.Name == j.Name:
		return "name"
	}
	return ""
}
