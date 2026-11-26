package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// A client's JSON config keeps its members in order: only the one server
// entry pairtask owns changes, everything else is written back as it was
// (re-indented with two spaces, as these clients write it themselves).

// member is one name and its value, as written.
type member struct {
	Key   string
	Value json.RawMessage
}

// object is a JSON object's members in their order.
type object []member

// errNotObject: the config, or its servers entry, isn't a JSON object.
var errNotObject = errors.New("not a JSON object")

// parseObject reads a JSON object keeping its members' order.
func parseObject(data []byte) (object, error) {
	if !json.Valid(data) {
		return nil, errors.New("not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errNotObject
	}
	var out object
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		out = append(out, member{Key: tok.(string), Value: value})
	}
	return out, nil
}

// get is the value at key, if there is one.
func (o object) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// set replaces the value at key in place, or adds it at the end.
func (o object) set(key string, value json.RawMessage) object {
	for i, m := range o {
		if m.Key == key {
			o[i].Value = value
			return o
		}
	}
	return append(o, member{Key: key, Value: value})
}

// without is o less key.
func (o object) without(key string) object {
	out := object{}
	for _, m := range o {
		if m.Key != key {
			out = append(out, m)
		}
	}
	return out
}

// encode writes o with two-space indentation, nested values re-indented.
func (o object) encode(prefix string) ([]byte, error) {
	if len(o) == 0 {
		return []byte("{}"), nil
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, m := range o {
		key, err := json.Marshal(m.Key)
		if err != nil {
			return nil, err
		}
		var value bytes.Buffer
		if err := json.Indent(&value, m.Value, prefix+"  ", "  "); err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s  %s: %s", prefix, key, value.Bytes())
		if i < len(o)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(prefix + "}")
	return b.Bytes(), nil
}

// jsonServers edits the servers object at serversKey in a client's JSON
// config: edit gets the servers in order and returns them changed.
func jsonServers(data []byte, serversKey string, edit func(object) object) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	top, err := parseObject(data)
	if err != nil {
		return nil, err
	}
	servers := object{}
	if raw, ok := top.get(serversKey); ok {
		if servers, err = parseObject(raw); err != nil {
			return nil, fmt.Errorf("%q: %w", serversKey, err)
		}
	}
	encoded, err := edit(servers).encode("")
	if err != nil {
		return nil, err
	}
	out, err := top.set(serversKey, encoded).encode("")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
