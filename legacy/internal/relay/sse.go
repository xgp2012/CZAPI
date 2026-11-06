package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// streamScanner reads an SSE stream line by line, forwarding each line to the
// client immediately and capturing any usage block it sees.
//
// One instance per response; not safe for concurrent use (and not needed).
type streamScanner struct {
	usage   *Usage
	done    bool
	sawData bool
}

// sseReader wraps a body reader with SSE line handling.
type sseReader struct {
	br      *bufio.Reader
	scanner *streamScanner
}

func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{br: bufio.NewReaderSize(r, 32*1024), scanner: &streamScanner{}}
}

// next returns the next raw line WITHOUT its trailing newline (the newline is
// re-added by the writer so byte-for-byte fidelity is preserved).
func (s *sseReader) next() (string, error) {
	line, err := s.br.ReadString('\n')
	if len(line) == 0 && err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

// inspect parses a data line looking for usage and the [DONE] sentinel.
func (s *sseReader) inspect(line string) {
	const prefix = "data:"
	if !strings.HasPrefix(line, prefix) {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if payload == "" {
		return
	}
	s.scanner.sawData = true
	if payload == "[DONE]" {
		s.scanner.done = true
		return
	}
	// Only decode chunks that actually mention usage: decoding every chunk would
	// double the CPU cost of streaming for no benefit.
	if !bytes.Contains([]byte(payload), []byte(`"usage"`)) {
		return
	}
	var chunk struct {
		Usage *Usage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return
	}
	if chunk.Usage != nil && (chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
		s.scanner.usage = chunk.Usage
	}
}

// Usage returns the captured usage block (nil when the stream carried none).
func (s *sseReader) Usage() *Usage { return s.scanner.usage }

// Finished reports whether the stream ended with [DONE].
func (s *sseReader) Finished() bool { return s.scanner.done }

// SawData reports whether any data line was observed.
func (s *sseReader) SawData() bool { return s.scanner.sawData }

// flushWriter writes a line and flushes immediately so clients see tokens as
// they arrive (plan §6.7).
type flushWriter struct {
	w       io.Writer
	flusher http.Flusher
}

func newFlushWriter(w http.ResponseWriter) *flushWriter {
	fw := &flushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		fw.flusher = f
	}
	return fw
}

// writeLine writes a line plus a newline and flushes.
func (f *flushWriter) writeLine(line string) error {
	if _, err := io.WriteString(f.w, line+"\n"); err != nil {
		return err
	}
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return nil
}

// writeRaw writes bytes and flushes.
func (f *flushWriter) writeRaw(b []byte) error {
	if _, err := f.w.Write(b); err != nil {
		return err
	}
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return nil
}
