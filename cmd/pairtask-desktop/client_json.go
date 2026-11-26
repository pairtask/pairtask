package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// A client's JSON config is edited by splicing text: only the bytes of
// pairtask's own entry are written, inserted or removed, and every other
// byte of the file stays as it was. The result is decoded again and must
// equal the original with just that entry changed.

// jsonDoc is a JSON config's text.
type jsonDoc []byte

// span is one object member as written: where its key starts, and where
// its value starts and ends.
type span struct {
	key           string
	keyAt         int
	valAt, valEnd int
}

// object is an object's members and where its braces are.
type object struct {
	open, close int
	members     []span
}

var errNotObject = errors.New("not a JSON object")

func (d jsonDoc) skipSpace(i int) int {
	for i < len(d) && strings.IndexByte(" \t\r\n", d[i]) >= 0 {
		i++
	}
	return i
}

// stringEnd is the index after the string starting at i.
func (d jsonDoc) stringEnd(i int) int {
	for i++; i < len(d); i++ {
		switch d[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(d)
}

// valueEnd is the index after the value starting at i.
func (d jsonDoc) valueEnd(i int) int {
	switch d[i] {
	case '"':
		return d.stringEnd(i)
	case '{', '[':
		return d.nestedEnd(i)
	}
	for i < len(d) && strings.IndexByte(",}] \t\r\n", d[i]) < 0 {
		i++
	}
	return i
}

// nestedEnd is the index after the object or array starting at i.
func (d jsonDoc) nestedEnd(i int) int {
	depth := 0
	for ; i < len(d); i++ {
		switch d[i] {
		case '"':
			i = d.stringEnd(i) - 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(d)
}

// objectAt reads the object whose '{' is at open. The document is valid
// JSON (checked first), so the scan needs no error handling of its own.
func (d jsonDoc) objectAt(open int) (object, error) {
	if open >= len(d) || d[open] != '{' {
		return object{}, errNotObject
	}
	o := object{open: open}
	i := d.skipSpace(open + 1)
	for i < len(d) && d[i] != '}' {
		m, next := d.member(i)
		o.members = append(o.members, m)
		i = d.skipSpace(next)
		if i < len(d) && d[i] == ',' {
			i = d.skipSpace(i + 1)
		}
	}
	o.close = i
	return o, nil
}

// member reads the member whose key starts at i, and where it ends.
func (d jsonDoc) member(i int) (span, int) {
	keyEnd := d.stringEnd(i)
	var key string
	_ = json.Unmarshal(d[i:keyEnd], &key)
	valAt := d.skipSpace(d.skipSpace(keyEnd) + 1) // past the ':'
	valEnd := d.valueEnd(valAt)
	return span{key: key, keyAt: i, valAt: valAt, valEnd: valEnd}, valEnd
}

func (o object) find(key string) (span, bool) {
	for _, m := range o.members {
		if m.key == key {
			return m, true
		}
	}
	return span{}, false
}

// indent is the leading whitespace of the line holding position i.
func (d jsonDoc) indent(i int) string {
	start := bytes.LastIndexByte(d[:i], '\n') + 1
	end := start
	for end < len(d) && (d[end] == ' ' || d[end] == '\t') {
		end++
	}
	return string(d[start:end])
}

// startsLine: position i is the first thing on its line.
func (d jsonDoc) startsLine(i int) bool {
	start := bytes.LastIndexByte(d[:i], '\n') + 1
	return len(bytes.TrimSpace(d[start:i])) == 0 && bytes.IndexByte(d, '\n') >= 0
}

// layout is how new text is laid out: one member per line at an indent,
// or compact on one line.
type layout struct {
	pretty bool
	unit   string
}

// layoutOf follows the file: compact when its members share a line,
// else its own indentation step.
func (d jsonDoc) layoutOf(top object) layout {
	if len(top.members) == 0 {
		return layout{pretty: bytes.IndexByte(d, '\n') >= 0, unit: "  "}
	}
	first := top.members[0].keyAt
	if !d.startsLine(first) {
		return layout{}
	}
	unit := d.indent(first)
	if unit == "" {
		unit = "  "
	}
	return layout{pretty: true, unit: unit}
}

// splice is d with [from, to) replaced by text.
func (d jsonDoc) splice(from, to int, text string) jsonDoc {
	out := make(jsonDoc, 0, len(d)+len(text))
	out = append(out, d[:from]...)
	out = append(out, text...)
	return append(out, d[to:]...)
}

// json is the entry's value, laid out at indent, one step deeper.
func (e serverEntry) json(l layout, indent string) string {
	cmd, _ := json.Marshal(e.Command)
	if !l.pretty {
		return `{"command":` + string(cmd) + `,"args":["mcp"]}`
	}
	in := indent + l.unit
	return "{\n" + in + `"command": ` + string(cmd) + ",\n" +
		in + `"args": [` + "\n" + in + l.unit + `"mcp"` + "\n" + in + "]\n" + indent + "}"
}

// withMember is d with key: value added to o as its last member, where
// inner is the indent of o's members.
func (d jsonDoc) withMember(o object, key, value string, l layout, inner string) jsonDoc {
	quoted, _ := json.Marshal(key)
	sep := ": "
	if !l.pretty {
		sep = ":"
	}
	member := string(quoted) + sep + value
	if len(o.members) > 0 {
		last := o.members[len(o.members)-1]
		if !l.pretty {
			return d.splice(last.valEnd, last.valEnd, ","+member)
		}
		return d.splice(last.valEnd, last.valEnd, ",\n"+inner+member)
	}
	if !l.pretty {
		return d.splice(o.open+1, o.close, member)
	}
	return d.splice(o.open+1, o.close, "\n"+inner+member+"\n"+d.indent(o.open))
}

// memberIndent is the indent of o's members: its first member's, or one
// step past the line holding its brace.
func (d jsonDoc) memberIndent(o object, l layout) string {
	if len(o.members) > 0 && d.startsLine(o.members[0].keyAt) {
		return d.indent(o.members[0].keyAt)
	}
	return d.indent(o.open) + l.unit
}

// withoutMember is d less member m of o, with the comma that joined it.
func (d jsonDoc) withoutMember(o object, m span) jsonDoc {
	i := indexOf(o.members, m.key)
	switch {
	case len(o.members) == 1:
		return d.splice(o.open+1, o.close, "")
	case i > 0:
		return d.splice(o.members[i-1].valEnd, m.valEnd, "")
	}
	return d.splice(m.keyAt, o.members[1].keyAt, "")
}

func indexOf(members []span, key string) int {
	for i, m := range members {
		if m.key == key {
			return i
		}
	}
	return -1
}

// top is the document's top-level object.
func (d jsonDoc) top() (object, error) {
	if !json.Valid(d) {
		return object{}, errors.New("not valid JSON")
	}
	return d.objectAt(d.skipSpace(0))
}

// servers is the servers object at key, if the document has one.
func (d jsonDoc) servers(top object, key string) (object, bool, error) {
	m, ok := top.find(key)
	if !ok {
		return object{}, false, nil
	}
	o, err := d.objectAt(m.valAt)
	if err != nil {
		return object{}, false, fmt.Errorf("%q: %w", key, err)
	}
	return o, true, nil
}

// connected is the document with pairtask's entry set to e.
func (d jsonDoc) connected(key string, e serverEntry) (jsonDoc, error) {
	top, err := d.top()
	if err != nil {
		return nil, err
	}
	l := d.layoutOf(top)
	servers, ok, err := d.servers(top, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		inner := d.memberIndent(top, l)
		value := d.newServers(e, l, inner)
		return d.withMember(top, key, value, l, inner), nil
	}
	inner := d.memberIndent(servers, l)
	if ours, found := servers.find(serverName); found {
		return d.splice(ours.valAt, ours.valEnd, e.json(l, inner)), nil
	}
	return d.withMember(servers, serverName, e.json(l, inner), l, inner), nil
}

// newServers is a servers object holding just pairtask's entry, for a
// member at indent.
func (d jsonDoc) newServers(e serverEntry, l layout, indent string) string {
	quoted, _ := json.Marshal(serverName)
	if !l.pretty {
		return "{" + string(quoted) + ":" + e.json(l, "") + "}"
	}
	in := indent + l.unit
	return "{\n" + in + string(quoted) + ": " + e.json(l, in) + "\n" + indent + "}"
}

// disconnected is the document less pairtask's entry.
func (d jsonDoc) disconnected(key string) (jsonDoc, error) {
	top, err := d.top()
	if err != nil {
		return nil, err
	}
	servers, ok, err := d.servers(top, key)
	if err != nil || !ok {
		return d, err
	}
	ours, found := servers.find(serverName)
	if !found {
		return d, nil
	}
	return d.withoutMember(servers, ours), nil
}

// sameExcept: after decodes to before with only key's pairtask entry
// different, so nothing else in the file changed meaning.
func sameExcept(before, after jsonDoc, key string) error {
	strip := func(d jsonDoc) (map[string]any, error) {
		var v map[string]any
		if len(bytes.TrimSpace(d)) == 0 {
			return map[string]any{}, nil
		}
		if err := json.Unmarshal(d, &v); err != nil {
			return nil, err
		}
		if s, ok := v[key].(map[string]any); ok {
			delete(s, serverName)
			if len(s) == 0 {
				delete(v, key)
			}
		}
		return v, nil
	}
	a, err := strip(before)
	if err != nil {
		return err
	}
	b, err := strip(after)
	if err != nil {
		return fmt.Errorf("the edited config isn't valid JSON: %w", err)
	}
	if !reflect.DeepEqual(a, b) {
		return errors.New("the edit would change more than the pairtask entry")
	}
	return nil
}
