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

// offset is a position in a jsonDoc.
type offset int

// jsonKey is an object member's name.
type jsonKey string

// span is one object member as written: where its key starts, and where
// its value starts and ends.
type span struct {
	key           jsonKey
	keyAt         offset
	valAt, valEnd offset
}

// object is an object's members and where its braces are.
type object struct {
	open, close offset
	members     []span
}

// newMember is a member to add: its name and its value as text.
type newMember struct {
	key   jsonKey
	value string
}

var errNotObject = errors.New("not a JSON object")

func (d jsonDoc) at(i offset) byte { return d[i] }

func (d jsonDoc) end() offset { return offset(len(d)) }

func (d jsonDoc) skipSpace(i offset) offset {
	for i < d.end() && strings.IndexByte(" \t\r\n", d.at(i)) >= 0 {
		i++
	}
	return i
}

// stringEnd is the offset after the string starting at i.
func (d jsonDoc) stringEnd(i offset) offset {
	for i++; i < d.end(); i++ {
		switch d.at(i) {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return d.end()
}

// valueEnd is the offset after the value starting at i.
func (d jsonDoc) valueEnd(i offset) offset {
	switch d.at(i) {
	case '"':
		return d.stringEnd(i)
	case '{', '[':
		return d.nestedEnd(i)
	}
	for i < d.end() && strings.IndexByte(",}] \t\r\n", d.at(i)) < 0 {
		i++
	}
	return i
}

// nestedEnd is the offset after the object or array starting at i.
func (d jsonDoc) nestedEnd(i offset) offset {
	depth := 0
	for ; i < d.end(); i++ {
		switch d.at(i) {
		case '"':
			i = d.stringEnd(i) - 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
		if depth == 0 {
			return i + 1
		}
	}
	return d.end()
}

// objectAt reads the object whose '{' is at open. The document is valid
// JSON (checked first), so the scan needs no error handling of its own.
func (d jsonDoc) objectAt(open offset) (object, error) {
	if open >= d.end() || d.at(open) != '{' {
		return object{}, errNotObject
	}
	o := object{open: open}
	i := d.skipSpace(open + 1)
	for i < d.end() && d.at(i) != '}' {
		m := d.member(i)
		o.members = append(o.members, m)
		i = d.afterComma(m.valEnd)
	}
	o.close = i
	return o, nil
}

// afterComma is where the next member starts after a value ending at i.
func (d jsonDoc) afterComma(i offset) offset {
	i = d.skipSpace(i)
	if i < d.end() && d.at(i) == ',' {
		i = d.skipSpace(i + 1)
	}
	return i
}

// member reads the member whose key starts at i.
func (d jsonDoc) member(i offset) span {
	keyEnd := d.stringEnd(i)
	var key string
	_ = json.Unmarshal(d[i:keyEnd], &key)
	valAt := d.skipSpace(d.skipSpace(keyEnd) + 1) // past the ':'
	return span{key: jsonKey(key), keyAt: i, valAt: valAt, valEnd: d.valueEnd(valAt)}
}

func (o object) find(key jsonKey) (span, bool) {
	for _, m := range o.members {
		if m.key == key {
			return m, true
		}
	}
	return span{}, false
}

// lineStart is where the line holding i starts.
func (d jsonDoc) lineStart(i offset) offset {
	return offset(bytes.LastIndexByte(d[:i], '\n') + 1)
}

// indent is the leading whitespace of the line holding i.
func (d jsonDoc) indent(i offset) string {
	start := d.lineStart(i)
	end := start
	for end < d.end() && (d.at(end) == ' ' || d.at(end) == '\t') {
		end++
	}
	return string(d[start:end])
}

// startsLine: i is the first thing on its line, in a file of many lines.
func (d jsonDoc) startsLine(i offset) bool {
	start := d.lineStart(i)
	return len(bytes.TrimSpace(d[start:i])) == 0 && bytes.IndexByte(d, '\n') >= 0
}

// layout is how new text is laid out: one member per line at an indent,
// or compact on one line.
type layout struct {
	pretty bool
	unit   string
}

// layoutOf follows the file: compact when its members share a line, else
// its own indentation step.
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
func (d jsonDoc) splice(from, to offset, text string) jsonDoc {
	var out jsonDoc
	out = append(out, d[:from]...)
	out = append(out, text...)
	return append(out, d[to:]...)
}

// json is the entry's value, laid out at indent, one step deeper.
func (e serverEntry) json(l layout, indent string) string {
	if !l.pretty {
		out, _ := json.Marshal(e)
		return string(out)
	}
	out, _ := json.MarshalIndent(e, indent, l.unit)
	return string(out)
}

// text is the member as written in layout l.
func (m newMember) text(l layout) string {
	quoted, _ := json.Marshal(string(m.key))
	if !l.pretty {
		return string(quoted) + ":" + m.value
	}
	return string(quoted) + ": " + m.value
}

// placement is where a new member goes: in o, laid out as l, at inner.
type placement struct {
	o     object
	l     layout
	inner string
}

// withMember is d with m added as the last member of p's object.
func (d jsonDoc) withMember(p placement, m newMember) jsonDoc {
	member := m.text(p.l)
	if len(p.o.members) == 0 {
		return d.intoEmpty(p, member)
	}
	last := p.o.members[len(p.o.members)-1]
	if !p.l.pretty {
		return d.splice(last.valEnd, last.valEnd, ","+member)
	}
	return d.splice(last.valEnd, last.valEnd, ",\n"+p.inner+member)
}

// intoEmpty is d with member as the only member of p's empty object.
func (d jsonDoc) intoEmpty(p placement, member string) jsonDoc {
	if !p.l.pretty {
		return d.splice(p.o.open+1, p.o.close, member)
	}
	return d.splice(p.o.open+1, p.o.close, "\n"+p.inner+member+"\n"+d.indent(p.o.open))
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
	i := o.index(m.key)
	switch {
	case len(o.members) == 1:
		return d.splice(o.open+1, o.close, "")
	case i > 0:
		return d.splice(o.members[i-1].valEnd, m.valEnd, "")
	}
	return d.splice(m.keyAt, o.members[1].keyAt, "")
}

func (o object) index(key jsonKey) int {
	for i, m := range o.members {
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
func (d jsonDoc) servers(top object, key jsonKey) (object, bool, error) {
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
func (d jsonDoc) connected(key jsonKey, e serverEntry) (jsonDoc, error) {
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
		return d.withMember(placement{top, l, inner}, newMember{key, newServers(e, l, inner)}), nil
	}
	inner := d.memberIndent(servers, l)
	if ours, found := servers.find(serverName); found {
		return d.splice(ours.valAt, ours.valEnd, e.json(l, inner)), nil
	}
	return d.withMember(placement{servers, l, inner}, newMember{serverName, e.json(l, inner)}), nil
}

// newServers is a servers object holding just pairtask's entry, for a
// member at indent.
func newServers(e serverEntry, l layout, indent string) string {
	if !l.pretty {
		return "{" + newMember{serverName, e.json(l, "")}.text(l) + "}"
	}
	in := indent + l.unit
	return "{\n" + in + newMember{serverName, e.json(l, in)}.text(l) + "\n" + indent + "}"
}

// disconnected is the document less pairtask's entry.
func (d jsonDoc) disconnected(key jsonKey) (jsonDoc, error) {
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

// decodedWithout is d decoded, less key's pairtask entry.
func (d jsonDoc) decodedWithout(key jsonKey) (map[string]any, error) {
	v := map[string]any{}
	if len(bytes.TrimSpace(d)) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(d, &v); err != nil {
		return nil, err
	}
	if s, ok := v[string(key)].(map[string]any); ok {
		delete(s, serverName)
		if len(s) == 0 {
			delete(v, string(key))
		}
	}
	return v, nil
}

// sameExcept: after decodes to before with only key's pairtask entry
// different, so nothing else in the file changed meaning.
func sameExcept(before, after jsonDoc, key jsonKey) error {
	a, err := before.decodedWithout(key)
	if err != nil {
		return err
	}
	b, err := after.decodedWithout(key)
	if err != nil {
		return fmt.Errorf("the edited config isn't valid JSON: %w", err)
	}
	if !reflect.DeepEqual(a, b) {
		return errors.New("the edit would change more than the pairtask entry")
	}
	return nil
}
