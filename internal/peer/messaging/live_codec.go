package messaging

import "fmt"

// The live frames of version 2: Live after the first round, then the
// field-less signals that keep a link and start its rounds.

var liveShape = shape{FrameLive, 1}

// EncodeSignal is one of the field-less live frames: Round, RoundAck, Ping,
// Pong or Pending.
func EncodeSignal(kind byte) ([]byte, error) { return encode(kind) }

// DecodeSignal checks a field-less live frame of the wanted kind.
func DecodeSignal(frame []byte, kind byte) error {
	_, err := decode(frame, shape{kind, 0})
	return err
}

// EncodeLive says whether this side stays connected after the first round.
func EncodeLive(on bool) ([]byte, error) {
	flag := byte(0)
	if on {
		flag = 1
	}
	return encode(FrameLive, []byte{flag})
}

func DecodeLive(frame []byte) (bool, error) {
	f, err := decode(frame, liveShape)
	if err != nil {
		return false, err
	}
	if len(f[0]) != 1 || f[0][0] > 1 {
		return false, fmt.Errorf("%w: live flag", ErrProtocol)
	}
	return f[0][0] == 1, nil
}
