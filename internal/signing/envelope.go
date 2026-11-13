package signing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"slices"
	"time"
)

// Sender kinds: who a header says sent the message.
const (
	SenderAgent    = "agent"
	SenderOperator = "operator"
)

const messageDomain = "pairtask-msg-v1"

// Header is what a message signature covers: the message's identity,
// sender, audience, thread and text, but no broker-stamped routing (seq,
// origin, delivery state), which brokers set and recipients needn't trust.
// It travels inside the seal, so a broker that canonicalises a frame (a
// group's members, a payload's JSON spacing) can't break the signature;
// the recipient checks the frame against it instead (Seal.Matches).
type Header struct {
	MessageID  string
	SenderKind string // SenderAgent or SenderOperator
	SenderID   string // the agent's ID; "operator" for an operator
	Workspace  string // key ID of the sender's workspace
	Recipients []string
	InReplyTo  string
	CreatedAt  time.Time
	TextHash   [sha256.Size]byte
}

// TextHash is the hash a header carries for a message's text.
func TextHash(text string) [sha256.Size]byte { return sha256.Sum256([]byte(text)) }

// Seal is a signed header: the header, the signing key's ID and signature,
// the certificate for that key, and the public key of the root that issued
// it, which the recipient matches against the fingerprint it pinned.
type Seal struct {
	Header  Header
	KeyID   string
	Sig     []byte
	Chain   []Cert
	RootKey ed25519.PublicKey
}

// Sign seals h with key, certified by chain under root.
func Sign(key ed25519.PrivateKey, chain []Cert, root ed25519.PublicKey, h Header) Seal {
	h.Recipients = sorted(h.Recipients)
	return Seal{Header: h, KeyID: KeyID(key.Public().(ed25519.PublicKey)), Sig: ed25519.Sign(key, h.tbs()), Chain: chain, RootKey: root}
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

// tbs is what a sender signs.
func (h Header) tbs() []byte {
	w := writer{}
	w.str(messageDomain)
	h.write(&w)
	return w.b
}

func (h Header) write(w *writer) {
	w.str(h.MessageID)
	w.str(h.SenderKind)
	w.str(h.SenderID)
	w.str(h.Workspace)
	w.u64(uint64(len(h.Recipients)))
	for _, r := range h.Recipients {
		w.str(r)
	}
	w.str(h.InReplyTo)
	w.u64(uint64(h.CreatedAt.UnixMilli()))
	w.bytes(h.TextHash[:])
}

func readHeader(r *reader) Header {
	h := Header{MessageID: r.str(), SenderKind: r.str(), SenderID: r.str(), Workspace: r.str()}
	n := r.u64()
	if n > maxRecipients {
		r.fail()
		return h
	}
	for range n {
		h.Recipients = append(h.Recipients, r.str())
	}
	h.InReplyTo, h.CreatedAt = r.str(), time.UnixMilli(int64(r.u64()))
	if hash := r.bytes(); len(hash) == sha256.Size {
		copy(h.TextHash[:], hash)
	} else {
		r.fail()
	}
	return h
}

// Bounds on what a seal may carry.
const (
	maxRecipients = 1024
	maxChain      = 4
)

// Marshal encodes the seal.
func (s Seal) Marshal() []byte {
	w := writer{}
	s.Header.write(&w)
	w.str(s.KeyID)
	w.bytes(s.Sig)
	w.u64(uint64(len(s.Chain)))
	for _, c := range s.Chain {
		w.bytes(c.Marshal())
	}
	w.bytes(s.RootKey)
	return w.b
}

// ParseSeal decodes a Marshal'd seal; it checks shape, not trust.
func ParseSeal(b []byte) (Seal, error) {
	r := reader{b: b}
	s := Seal{Header: readHeader(&r), KeyID: r.str(), Sig: r.bytes()}
	chain, err := readChain(&r)
	if err != nil {
		return Seal{}, err
	}
	s.Chain, s.RootKey = chain, ed25519.PublicKey(r.bytes())
	if err := r.done(); err != nil {
		return Seal{}, err
	}
	if !s.sized() {
		return Seal{}, errMalformed
	}
	return s, nil
}

// readChain reads a chain of 1 to maxChain certificates.
func readChain(r *reader) ([]Cert, error) {
	n := r.u64()
	if r.err != nil || !chainLength(n) {
		return nil, errMalformed
	}
	chain := make([]Cert, 0, n)
	for range n {
		c, err := ParseCert(r.bytes())
		if err != nil {
			return nil, err
		}
		chain = append(chain, c)
	}
	return chain, nil
}

func chainLength(n uint64) bool { return n >= 1 && n <= maxChain }

// sized: the signature and root key have their Ed25519 sizes.
func (s Seal) sized() bool {
	return len(s.Sig) == ed25519.SignatureSize && len(s.RootKey) == ed25519.PublicKeySize
}
