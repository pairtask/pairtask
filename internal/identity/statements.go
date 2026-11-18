package identity

import (
	"bytes"
	"crypto/ed25519"
	"time"

	"github.com/pairtask/pairtask/internal/signing"
)

// Kind names a statement's domain; the domain string is what is signed.
type Kind string

const (
	KindAnchor     Kind = "pairtask-id-anchor-v1"
	KindClaim      Kind = "pairtask-ws-claim-v1"
	KindAcceptance Kind = "pairtask-ws-accept-v1"
	KindRecord     Kind = "pairtask-id-record-v1"
	KindRevocation Kind = "pairtask-id-revoke-v1"
	KindNotice     Kind = "pairtask-id-notice-v1"
	KindInventory  Kind = "pairtask-id-inventory-v1"
	KindSuccession Kind = "pairtask-id-succession-v1"
	KindHandover   Kind = "pairtask-id-handover-v1"
	KindMigration  Kind = "pairtask-id-migrate-v1"
	KindResolution Kind = "pairtask-id-resolve-v1"
)

// Statement is any signed statement of wire-v0 §2.
type Statement interface {
	Kind() Kind
	// TBS is the signed bytes: the domain, then the fields.
	TBS() []byte
	// Signature is the Ed25519 signature over TBS, empty until signed.
	Signature() []byte
	// Marshal is bytes(TBS) ‖ bytes(Signature).
	Marshal() []byte
}

// sign returns sig over tbs; check verifies it under pub.
func sign(key ed25519.PrivateKey, tbs []byte) []byte { return ed25519.Sign(key, tbs) }

func check(pub ed25519.PublicKey, tbs, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, tbs, sig) {
		return ErrBadSignature
	}
	return nil
}

// Anchor is a lineage's genesis: its first person key, its first recovery
// key (none with --no-recovery) and when it was made. Lineage() is its
// digest, and the digest is the first recovery statement's predecessor.
type Anchor struct {
	Person   ed25519.PublicKey
	Recovery ed25519.PublicKey // nil: no recovery
	InitAt   time.Time
	Sig      []byte
}

func (a Anchor) Kind() Kind { return KindAnchor }

func (a Anchor) TBS() []byte {
	w := (&writer{}).domain(string(KindAnchor))
	w.key(a.Person)
	if a.Recovery == nil {
		w.u64(0)
	} else {
		w.u64(1)
		w.key(a.Recovery)
	}
	w.at(a.InitAt)
	return w.b
}

func (a Anchor) Signature() []byte { return a.Sig }
func (a Anchor) Marshal() []byte   { return sealed(a.TBS(), a.Sig) }
func (a Anchor) Lineage() Lineage  { return Lineage(DigestOf(a.TBS())) }

// Signed returns a with Sig set by the person key.
func (a Anchor) Signed(person ed25519.PrivateKey) Anchor {
	a.Sig = sign(person, a.TBS())
	return a
}

// Check verifies a under its own person key.
func (a Anchor) Check() error { return check(a.Person, a.TBS(), a.Sig) }

// ParseAnchor decodes a Marshal'd anchor; it checks shape, not the signature.
func ParseAnchor(b []byte) (Anchor, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Anchor{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindAnchor))
	a := Anchor{Person: r.key(), Sig: sig}
	switch r.u64() {
	case 0:
	case 1:
		a.Recovery = r.key()
	default:
		r.fail()
	}
	a.InitAt = r.at()
	return a, r.done()
}

// Claim: P says workspace W is the person's, in epoch e.
type Claim struct {
	Lineage   Lineage
	Epoch     uint64
	Workspace string // W's key ID
	At        time.Time
	Sig       []byte
}

func (c Claim) Kind() Kind { return KindClaim }

func (c Claim) TBS() []byte {
	w := (&writer{}).domain(string(KindClaim))
	w.lineage(c.Lineage)
	w.u64(c.Epoch)
	w.str(c.Workspace)
	w.at(c.At)
	return w.b
}

func (c Claim) Signature() []byte { return c.Sig }
func (c Claim) Marshal() []byte   { return sealed(c.TBS(), c.Sig) }

func (c Claim) Signed(person ed25519.PrivateKey) Claim {
	c.Sig = sign(person, c.TBS())
	return c
}

func (c Claim) Check(person ed25519.PublicKey) error { return check(person, c.TBS(), c.Sig) }

func ParseClaim(b []byte) (Claim, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Claim{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindClaim))
	c := Claim{Lineage: r.lineage(), Epoch: r.counter(), Workspace: r.str(), At: r.at(), Sig: sig}
	return c, r.done()
}

// Acceptance: W says it belongs to person P, for the same claim.
type Acceptance struct {
	Lineage   Lineage
	Epoch     uint64
	Person    string // P's key ID
	Workspace string // W's key ID
	At        time.Time
	Sig       []byte
}

func (a Acceptance) Kind() Kind { return KindAcceptance }

func (a Acceptance) TBS() []byte {
	w := (&writer{}).domain(string(KindAcceptance))
	w.lineage(a.Lineage)
	w.u64(a.Epoch)
	w.str(a.Person)
	w.str(a.Workspace)
	w.at(a.At)
	return w.b
}

func (a Acceptance) Signature() []byte { return a.Sig }
func (a Acceptance) Marshal() []byte   { return sealed(a.TBS(), a.Sig) }

func (a Acceptance) Signed(workspace ed25519.PrivateKey) Acceptance {
	a.Sig = sign(workspace, a.TBS())
	return a
}

func (a Acceptance) Check(workspace ed25519.PublicKey) error { return check(workspace, a.TBS(), a.Sig) }

func ParseAcceptance(b []byte) (Acceptance, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Acceptance{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindAcceptance))
	a := Acceptance{Lineage: r.lineage(), Epoch: r.counter(), Person: r.str(), Workspace: r.str(), At: r.at(), Sig: sig}
	return a, r.done()
}

// Binding is a claim with its acceptance; Consistent says they are about
// the same lineage, epoch, person, workspace and moment.
type Binding struct {
	Claim      Claim
	Acceptance Acceptance
}

func (b Binding) Consistent() error {
	c, a := b.Claim, b.Acceptance
	if c.Lineage != a.Lineage || c.Epoch != a.Epoch || c.Workspace != a.Workspace || !c.At.Equal(a.At) {
		return ErrMalformed
	}
	return nil
}

// Record is the person's signed public record: the one signed form per
// (epoch, version), and what spaces and contacts hold.
type Record struct {
	Lineage  Lineage
	Epoch    uint64
	Version  uint64
	Person   string // P's key ID
	Recovery string // R's key ID; "" with no recovery
	Counter  uint64 // R's counter; 0 with no recovery
	Bindings []Binding
	Revoked  []string // device key IDs, sorted, unique
	At       time.Time
	Sig      []byte
}

func (rec Record) Kind() Kind { return KindRecord }

func (rec Record) TBS() []byte {
	w := (&writer{}).domain(string(KindRecord))
	w.lineage(rec.Lineage)
	w.u64(rec.Epoch)
	w.u64(rec.Version)
	w.str(rec.Person)
	w.str(rec.Recovery)
	w.u64(rec.Counter)
	w.count(len(rec.Bindings))
	for _, b := range rec.Bindings {
		w.bytes(b.Claim.Marshal())
		w.bytes(b.Acceptance.Marshal())
	}
	w.ids(rec.Revoked)
	w.at(rec.At)
	return w.b
}

func (rec Record) Signature() []byte { return rec.Sig }
func (rec Record) Marshal() []byte   { return sealed(rec.TBS(), rec.Sig) }

func (rec Record) Signed(person ed25519.PrivateKey) Record {
	rec.Sig = sign(person, rec.TBS())
	return rec
}

func (rec Record) Check(person ed25519.PublicKey) error { return check(person, rec.TBS(), rec.Sig) }

func ParseRecord(b []byte) (Record, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Record{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindRecord))
	rec := Record{Lineage: r.lineage(), Epoch: r.counter(), Version: r.counter(), Person: r.str(), Recovery: r.str(), Counter: r.counter(), Sig: sig}
	rec.Bindings = readBindings(&r)
	rec.Revoked = r.ids(MaxRevocations)
	rec.At = r.at()
	return rec, r.done()
}

func readBindings(r *reader) []Binding {
	n := r.count(MaxBindings)
	out := make([]Binding, 0, n)
	var previous []byte
	for i := 0; i < n; i++ {
		raw := r.bytes()
		claim, cerr := ParseClaim(raw)
		acceptance, aerr := ParseAcceptance(r.bytes())
		if cerr != nil || aerr != nil || (i > 0 && bytes.Compare(previous, raw) >= 0) {
			r.fail()
			return nil
		}
		previous = raw
		out = append(out, Binding{claim, acceptance})
	}
	return out
}

// Revocation retires one device for good.
type Revocation struct {
	Lineage Lineage
	Epoch   uint64
	Device  string // D's key ID
	At      time.Time
	Sig     []byte
}

func (v Revocation) Kind() Kind { return KindRevocation }

func (v Revocation) TBS() []byte {
	w := (&writer{}).domain(string(KindRevocation))
	w.lineage(v.Lineage)
	w.u64(v.Epoch)
	w.str(v.Device)
	w.at(v.At)
	return w.b
}

func (v Revocation) Signature() []byte { return v.Sig }
func (v Revocation) Marshal() []byte   { return sealed(v.TBS(), v.Sig) }

func (v Revocation) Signed(person ed25519.PrivateKey) Revocation {
	v.Sig = sign(person, v.TBS())
	return v
}

func (v Revocation) Check(person ed25519.PublicKey) error { return check(person, v.TBS(), v.Sig) }

func ParseRevocation(b []byte) (Revocation, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Revocation{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindRevocation))
	v := Revocation{Lineage: r.lineage(), Epoch: r.counter(), Device: r.str(), At: r.at(), Sig: sig}
	return v, r.done()
}

// Notice announces a newly linked device by key ID only: no timestamp, no
// label, so it discloses nothing but the key.
type Notice struct {
	Lineage Lineage
	Epoch   uint64
	Device  string
	Sig     []byte
}

func (n Notice) Kind() Kind { return KindNotice }

func (n Notice) TBS() []byte {
	w := (&writer{}).domain(string(KindNotice))
	w.lineage(n.Lineage)
	w.u64(n.Epoch)
	w.str(n.Device)
	return w.b
}

func (n Notice) Signature() []byte { return n.Sig }
func (n Notice) Marshal() []byte   { return sealed(n.TBS(), n.Sig) }

func (n Notice) Signed(person ed25519.PrivateKey) Notice {
	n.Sig = sign(person, n.TBS())
	return n
}

func (n Notice) Check(person ed25519.PublicKey) error { return check(person, n.TBS(), n.Sig) }

func ParseNotice(b []byte) (Notice, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Notice{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindNotice))
	n := Notice{Lineage: r.lineage(), Epoch: r.counter(), Device: r.str(), Sig: sig}
	return n, r.done()
}

// Device is one entry of the private inventory.
type Device struct {
	ID       string
	Label    string
	LinkedAt time.Time
}

// Inventory is the person's device list, signed and private: it travels
// only between the person's own machines and is never published.
type Inventory struct {
	Lineage Lineage
	Epoch   uint64
	Version uint64 // the record version it accompanies
	Devices []Device
	At      time.Time
	Sig     []byte
}

func (inv Inventory) Kind() Kind { return KindInventory }

func (inv Inventory) TBS() []byte {
	w := (&writer{}).domain(string(KindInventory))
	w.lineage(inv.Lineage)
	w.u64(inv.Epoch)
	w.u64(inv.Version)
	w.count(len(inv.Devices))
	for _, d := range inv.Devices {
		w.str(d.ID)
		w.str(d.Label)
		w.at(d.LinkedAt)
	}
	w.at(inv.At)
	return w.b
}

func (inv Inventory) Signature() []byte { return inv.Sig }
func (inv Inventory) Marshal() []byte   { return sealed(inv.TBS(), inv.Sig) }

func (inv Inventory) Signed(person ed25519.PrivateKey) Inventory {
	inv.Sig = sign(person, inv.TBS())
	return inv
}

func (inv Inventory) Check(person ed25519.PublicKey) error { return check(person, inv.TBS(), inv.Sig) }

func ParseInventory(b []byte) (Inventory, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Inventory{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindInventory))
	inv := Inventory{Lineage: r.lineage(), Epoch: r.counter(), Version: r.counter(), Sig: sig}
	n := r.count(MaxDevices)
	for i := 0; i < n; i++ {
		d := Device{ID: r.str(), Label: r.label(), LinkedAt: r.at()}
		if i > 0 && inv.Devices[i-1].ID >= d.ID {
			r.fail()
		}
		inv.Devices = append(inv.Devices, d)
	}
	inv.At = r.at()
	return inv, r.done()
}

// Succession opens the next epoch with a new person key, signed by the
// recovery key active at its predecessor.
type Succession struct {
	Lineage    Lineage
	Seq        uint64
	Prev       Digest // the predecessor recovery statement, or the anchor for seq 1
	Epoch      uint64 // predecessor epoch; the new epoch is Epoch+1
	Person     string // predecessor P's key ID
	NextPerson ed25519.PublicKey
	Recovery   string // R's key ID, the active R at the predecessor
	Counter    uint64 // R's active counter at the predecessor
	At         time.Time
	Sig        []byte
}

func (s Succession) Kind() Kind { return KindSuccession }

func (s Succession) TBS() []byte {
	w := (&writer{}).domain(string(KindSuccession))
	w.lineage(s.Lineage)
	w.u64(s.Seq)
	w.digest(s.Prev)
	w.u64(s.Epoch)
	w.str(s.Person)
	w.u64(s.Epoch + 1)
	w.key(s.NextPerson)
	w.str(s.Recovery)
	w.u64(s.Counter)
	w.at(s.At)
	return w.b
}

func (s Succession) Signature() []byte { return s.Sig }
func (s Succession) Marshal() []byte   { return sealed(s.TBS(), s.Sig) }

func (s Succession) Signed(recovery ed25519.PrivateKey) Succession {
	s.Sig = sign(recovery, s.TBS())
	return s
}

func (s Succession) Check(recovery ed25519.PublicKey) error { return check(recovery, s.TBS(), s.Sig) }

func ParseSuccession(b []byte) (Succession, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Succession{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindSuccession))
	s := Succession{Lineage: r.lineage(), Seq: r.counter(), Prev: r.digest(), Epoch: r.counter(), Person: r.str(), Sig: sig}
	if r.counter() != s.Epoch+1 {
		r.fail()
	}
	s.NextPerson, s.Recovery, s.Counter, s.At = r.key(), r.str(), r.counter(), r.at()
	return s, r.done()
}

// Handover replaces the recovery key, signed by the outgoing one.
type Handover struct {
	Lineage      Lineage
	Seq          uint64
	Prev         Digest
	Recovery     string // predecessor R's key ID
	Counter      uint64 // predecessor counter; the new counter is Counter+1
	NextRecovery ed25519.PublicKey
	Epoch        uint64 // the epoch it is issued in
	At           time.Time
	Sig          []byte
}

func (h Handover) Kind() Kind { return KindHandover }

func (h Handover) TBS() []byte {
	w := (&writer{}).domain(string(KindHandover))
	w.lineage(h.Lineage)
	w.u64(h.Seq)
	w.digest(h.Prev)
	w.str(h.Recovery)
	w.u64(h.Counter)
	w.u64(h.Counter + 1)
	w.key(h.NextRecovery)
	w.u64(h.Epoch)
	w.at(h.At)
	return w.b
}

func (h Handover) Signature() []byte { return h.Sig }
func (h Handover) Marshal() []byte   { return sealed(h.TBS(), h.Sig) }

func (h Handover) Signed(recovery ed25519.PrivateKey) Handover {
	h.Sig = sign(recovery, h.TBS())
	return h
}

func (h Handover) Check(recovery ed25519.PublicKey) error { return check(recovery, h.TBS(), h.Sig) }

func ParseHandover(b []byte) (Handover, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Handover{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindHandover))
	h := Handover{Lineage: r.lineage(), Seq: r.counter(), Prev: r.digest(), Recovery: r.str(), Counter: r.counter(), Sig: sig}
	if r.counter() != h.Counter+1 {
		r.fail()
	}
	h.NextRecovery, h.Epoch, h.At = r.key(), r.counter(), r.at()
	return h, r.done()
}

// Migration: a legacy person key says it is succeeded by a lineage's P.
type Migration struct {
	OldPerson string // the legacy key's ID, which signs
	Lineage   Lineage
	Person    string // destination P's key ID
	Epoch     uint64
	At        time.Time
	Sig       []byte
}

func (m Migration) Kind() Kind { return KindMigration }

func (m Migration) TBS() []byte {
	w := (&writer{}).domain(string(KindMigration))
	w.str(m.OldPerson)
	w.lineage(m.Lineage)
	w.str(m.Person)
	w.u64(m.Epoch)
	w.at(m.At)
	return w.b
}

func (m Migration) Signature() []byte { return m.Sig }
func (m Migration) Marshal() []byte   { return sealed(m.TBS(), m.Sig) }

func (m Migration) Signed(old ed25519.PrivateKey) Migration {
	m.Sig = sign(old, m.TBS())
	return m
}

func (m Migration) Check(old ed25519.PublicKey) error { return check(old, m.TBS(), m.Sig) }

func ParseMigration(b []byte) (Migration, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Migration{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindMigration))
	m := Migration{OldPerson: r.str(), Lineage: r.lineage(), Person: r.str(), Epoch: r.counter(), At: r.at(), Sig: sig}
	return m, r.done()
}

// Resolution is the operator's local choice between conflicting branches;
// it is signed by the local workspace key and never sent.
type Resolution struct {
	Lineage   Lineage
	Chosen    Digest
	Rejected  []Digest // sorted
	BranchSet Digest   // over the sorted digests of every statement displayed
	At        time.Time
	Sig       []byte
}

func (res Resolution) Kind() Kind { return KindResolution }

func (res Resolution) TBS() []byte {
	w := (&writer{}).domain(string(KindResolution))
	w.lineage(res.Lineage)
	w.digest(res.Chosen)
	w.digests(res.Rejected)
	w.digest(res.BranchSet)
	w.at(res.At)
	return w.b
}

func (res Resolution) Signature() []byte { return res.Sig }
func (res Resolution) Marshal() []byte   { return sealed(res.TBS(), res.Sig) }

func (res Resolution) Signed(workspace ed25519.PrivateKey) Resolution {
	res.Sig = sign(workspace, res.TBS())
	return res
}

func (res Resolution) Check(workspace ed25519.PublicKey) error {
	return check(workspace, res.TBS(), res.Sig)
}

func ParseResolution(b []byte) (Resolution, error) {
	tbs, sig, err := envelope(b)
	if err != nil {
		return Resolution{}, err
	}
	r := reader{b: tbs}
	r.domain(string(KindResolution))
	res := Resolution{Lineage: r.lineage(), Chosen: r.digest(), Rejected: r.digests(MaxBindings), BranchSet: r.digest(), At: r.at(), Sig: sig}
	return res, r.done()
}

// Parse decodes any statement by its domain, checking shape only.
func Parse(b []byte) (Statement, error) {
	tbs, _, err := envelope(b)
	if err != nil {
		return nil, err
	}
	r := reader{b: tbs}
	switch Kind(r.str()) {
	case KindAnchor:
		return ParseAnchor(b)
	case KindClaim:
		return ParseClaim(b)
	case KindAcceptance:
		return ParseAcceptance(b)
	case KindRecord:
		return ParseRecord(b)
	case KindRevocation:
		return ParseRevocation(b)
	case KindNotice:
		return ParseNotice(b)
	case KindInventory:
		return ParseInventory(b)
	case KindSuccession:
		return ParseSuccession(b)
	case KindHandover:
		return ParseHandover(b)
	case KindMigration:
		return ParseMigration(b)
	case KindResolution:
		return ParseResolution(b)
	}
	return nil, ErrMalformed
}

// KeyID is signing's key ID, so person, device and workspace keys are named
// the same way everywhere.
func KeyID(pub ed25519.PublicKey) string { return signing.KeyID(pub) }
