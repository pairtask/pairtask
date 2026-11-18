package host

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// Codex's remote plugin catalog made the startup plugin/list frame larger than
// the old 32 MiB limit (#408).
const overOldLimit = 40 << 20

// filler is an endless run of one byte, so an oversized frame is streamed
// rather than held in memory.
type filler byte

func (f filler) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(f)
	}
	return len(p), nil
}

// frameStream is before, then one frame whose params string is n bytes,
// then after, each on its own line.
func frameStream(before string, n int64, after string) io.Reader {
	return io.MultiReader(
		strings.NewReader(before+"\n"+`{"method":"plugin/list","params":"`),
		io.LimitReader(filler('x'), n),
		strings.NewReader("\"}\n"+after+"\n"))
}

func TestReadFramesHandlesAFrameOverTheOldLimit(t *testing.T) {
	var methods []string
	size := 0
	err := ReadFrames(frameStream(`{"method":"before"}`, overOldLimit, `{"method":"after"}`), func(f Frame) error {
		methods = append(methods, f.Method)
		if f.Method == "plugin/list" {
			size = len(f.Params)
		}
		return nil
	})
	if err != io.EOF {
		t.Fatalf("stream ended with %v, want io.EOF", err)
	}
	if strings.Join(methods, ",") != "before,plugin/list,after" {
		t.Fatalf("handled %v", methods)
	}
	if size != overOldLimit+2 {
		t.Fatalf("plugin/list params are %d bytes, want %d", size, overOldLimit+2)
	}
}

func TestReadFramesNamesAFrameOverTheLimit(t *testing.T) {
	if raceDetector {
		t.Skip("a frame over the limit fills a 128 MiB buffer; the non-race job covers it")
	}
	var methods []string
	err := ReadFrames(frameStream(`{"method":"before"}`, maxHostFrameBytes, `{"method":"after"}`), func(f Frame) error {
		methods = append(methods, f.Method)
		return nil
	})
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v, want ErrFrameTooLarge", err)
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("an oversized frame reads as a clean end of stream")
	}
	if !strings.Contains(err.Error(), "128 MiB") {
		t.Fatalf("error doesn't name the limit: %v", err)
	}
	if strings.Join(methods, ",") != "before" {
		t.Fatalf("handled %v, want only the frame before the oversized one", methods)
	}
}

func TestReadFramesEndOfStreamIsStillPlainEOF(t *testing.T) {
	for name, input := range map[string]string{"empty": "", "after a frame": `{"method":"one"}` + "\n"} {
		if err := ReadFrames(strings.NewReader(input), func(Frame) error { return nil }); err != io.EOF {
			t.Fatalf("%s: got %v, want io.EOF itself", name, err)
		}
	}
}

func TestReadFramesPassesOtherReadErrorsThrough(t *testing.T) {
	cause := errors.New("pipe closed")
	err := ReadFrames(io.MultiReader(bytes.NewReader([]byte(`{"method":"one"}`+"\n")), failingReader{cause}),
		func(Frame) error { return nil })
	if !errors.Is(err, cause) || errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v, want the reader's own error", err)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
