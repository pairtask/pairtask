package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

// pairtask space (Pairbook v1): spaces whose members are people. The
// commands that sign (create, invite, join, approve, sync, leave, unbind)
// run where the person key lives and sign there; the broker only checks,
// keeps and sends the signed records. members and status sign nothing.

const operatorPairbookUsage = `usage: pairtask space COMMAND [--space ID] [--socket PATH] [--json] [--timeout 30s]
  create NAME --pairbook URL     a new space, owned by you, everyone connects with everyone
  invite [--for 168h]            a one-time invite link (shown once)
  join LINK [--name NAME]        ask to join; you're in once the owner approves
  requests                       the requests waiting for the owner
  approve PERSON                 admit a join (lineage or request prefix), and apply members' requests
  sync                           apply members' requests and renew the space's map
  members                        the people in the space and their workspaces
  status                         each space: pending, active, expired or refused, and until when
  leave                          leave the space
  unbind WORKSPACE               take one of your workspaces out of the space
--space may be left out when exactly one space is pinned.
v0 spaces: pairtask space v0 COMMAND (list, publish, revoke and delete also work without v0).`

// pairbookArgs are a pairbook command's words and flags.
type pairbookArgs struct {
	words     []string
	space     string
	directory string
	name      string
	valid     time.Duration
}

// pairbookCommand is one command's run.
type pairbookCommand func(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error)

var pairbookCommands = map[string]pairbookCommand{
	"create":   pairbookCreate,
	"invite":   pairbookInvite,
	"join":     pairbookJoin,
	"requests": pairbookRequests,
	"approve":  pairbookApprove,
	"sync":     pairbookSync,
	"members":  pairbookMembers,
	"status":   pairbookStatus,
	"leave":    pairbookLeave,
	"unbind":   pairbookUnbind,
}

// runOperatorPairbook runs one pairbook command.
func runOperatorPairbook(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || pairbookCommands[args[0]] == nil {
		fmt.Fprintln(stderr, operatorPairbookUsage)
		return 2
	}
	s := &pairbookSession{}
	a, code := parsePairbook(args, &s.call, stderr)
	if code != -1 {
		return code
	}
	out, err := pairbookCommands[args[0]](ctx, s, a)
	if err != nil {
		fmt.Fprintln(stderr, "space "+args[0]+":", err)
		return 1
	}
	return writePairbook(out, s.call.asJSON, stdout, stderr)
}

func parsePairbook(args []string, call *operatorCall, stderr io.Writer) (pairbookArgs, int) {
	var a pairbookArgs
	flags := flag.NewFlagSet("space", flag.ContinueOnError)
	call.register(flags)
	flags.StringVar(&a.space, "space", "", "space ID; may be left out when exactly one is pinned")
	flags.StringVar(&a.directory, "pairbook", "", "create: the Pairbook directory, https://HOST[:PORT]")
	flags.StringVar(&a.name, "name", "", "join: your name in the space")
	flags.DurationVar(&a.valid, "for", 7*24*time.Hour, "invite: how long the link is valid")
	words, rest := leadingWords(args[1:])
	code := parseFlags(flags, rest, operatorPairbookUsage, stderr, func(more []string) error {
		a.words = append(words, more...)
		return call.validate()
	})
	return a, code
}

func writePairbook(out any, asJSON bool, stdout, stderr io.Writer) int {
	if asJSON {
		return encodeJSON(stdout, stderr, out)
	}
	if text, ok := out.(fmt.Stringer); ok {
		fmt.Fprintln(stdout, text.String())
		return 0
	}
	return encodeJSON(stdout, stderr, out)
}

// pairbookSession is one command's broker calls and, once a command needs
// to sign, the person on this host.
type pairbookSession struct {
	call operatorCall
	me   *spaceowner.Me
}

// person is this host's person, from the keys and their anchor.
func (s *pairbookSession) person() (spaceowner.Me, error) {
	if s.me != nil {
		return *s.me, nil
	}
	dir, err := personkey.ResolveDir()
	if err != nil {
		return spaceowner.Me{}, err
	}
	keys, err := personkey.Load(dir)
	if err != nil {
		return spaceowner.Me{}, fmt.Errorf("%w (pairtask identity init makes your keys)", err)
	}
	me, err := spaceowner.Load(keys)
	if err != nil {
		return spaceowner.Me{}, fmt.Errorf("%w (pairtask identity anchor makes your identity)", err)
	}
	s.me = &me
	return me, nil
}

// space sends one space request and answers its result, or the broker's
// refusal as an error.
func (s *pairbookSession) space(ctx context.Context, r protocol.SpaceRequest) (*protocol.SpaceResult, error) {
	resp, err := s.call.send(ctx, protocol.Message{Type: protocol.TypeOperatorSpace, MessageID: uuid.NewString(), SpaceRequest: &r})
	if err != nil {
		return nil, err
	}
	if resp.Type != protocol.TypeOperatorSpaceResult {
		return nil, errors.New(resp.Code + ": " + resp.Detail)
	}
	if resp.SpaceResult == nil {
		return &protocol.SpaceResult{}, nil
	}
	return resp.SpaceResult, nil
}

// current is the map the broker keeps for space and its mark.
func (s *pairbookSession) current(ctx context.Context, id string) (spaceowner.Current, error) {
	res, err := s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapCurrent, Space: id})
	if err != nil {
		return spaceowner.Current{}, err
	}
	var c spaceowner.Current
	if err := json.Unmarshal(res.Record, &c.Map); err != nil {
		return c, err
	}
	c.Mark = c.Map.SpaceMap.Mark()
	if res.Mark == nil || res.Mark.Digest != hex.EncodeToString(c.Mark.Digest[:]) {
		return c, errors.New("the broker's map and mark disagree")
	}
	return c, nil
}

// binding binds this broker's workspace to the person, now.
func (s *pairbookSession) binding(ctx context.Context, me spaceowner.Me, now time.Time) (pairbook.Binding, error) {
	epoch, err := me.Epoch()
	if err != nil {
		return pairbook.Binding{}, err
	}
	res, err := s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapAccept, Lineage: pairbook.LineageHex(me.Lineage), Epoch: epoch, Person: me.KeyID(), At: now})
	if err != nil {
		return pairbook.Binding{}, err
	}
	return pairbook.Binding{Claim: me.Claim(res.WorkspaceKey, epoch, now), Acceptance: res.Acceptance, WorkspaceKey: res.WorkspaceKey}, nil
}

// pairbookCreate makes a space owned by this person, with this workspace.
func pairbookCreate(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	if len(a.words) != 1 || a.directory == "" {
		return nil, errors.New("create needs NAME and --pairbook URL")
	}
	me, err := s.person()
	if err != nil {
		return nil, err
	}
	now := time.Now().Truncate(time.Millisecond)
	own, err := s.binding(ctx, me, now)
	if err != nil {
		return nil, err
	}
	id, err := newSpaceID()
	if err != nil {
		return nil, err
	}
	m, err := spaceowner.NewSpace(me, spaceowner.Genesis{Space: id, Name: a.words[0], Own: own}, now)
	if err != nil {
		return nil, err
	}
	return s.publish(ctx, a.directory, spacePut{put: space.MapPut{Map: m}})
}

// newSpaceID is a fresh space ID: 32 lowercase hex.
func newSpaceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// spacePut is a put to publish and the invites its joins consume.
type spacePut struct {
	put     space.MapPut
	invites []map[string]string
}

// publish sends a put; a stale answer means another map won.
func (s *pairbookSession) publish(ctx context.Context, directory string, p spacePut) (*protocol.SpaceResult, error) {
	if p.put.Applied == nil {
		p.put.Applied = []string{}
	}
	body, err := json.Marshal(map[string]any{"put": p.put, "invites": p.invites})
	if err != nil {
		return nil, err
	}
	return s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapPublish, Directory: directory, Record: body})
}
