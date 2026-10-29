package usageadjustment

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"
)

// WrapResponse rewrites before usage extraction, so billing and the client see
// the same counters. It leaves errors and unsupported binary formats untouched.
func WrapResponse(resp *http.Response, percent int, maxBytes int64, inputIncludesCache bool) {
	if resp == nil || resp.Body == nil || percent <= 0 || percent > 100 || resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.Header.Get("Content-Encoding") != "" {
		return
	}
	typ := strings.ToLower(resp.Header.Get("Content-Type"))
	stream := strings.Contains(typ, "text/event-stream")
	if !stream && !strings.Contains(typ, "json") {
		return
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	b := &bodyReader{source: resp.Body, rewriter: Rewriter{Percent: percent, InputIncludesCache: inputIncludesCache}, limit: maxBytes}
	if stream {
		b.stream = bufio.NewReaderSize(resp.Body, 32<<10)
	}
	resp.Body = b
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
	resp.Header.Del("ETag")
	resp.Header.Del("Content-MD5")
}

type bodyReader struct {
	source   io.ReadCloser
	rewriter Rewriter
	limit    int64
	stream   *bufio.Reader
	reader   io.Reader
	pending  []byte
	err      error
	oversize bool
}

func (b *bodyReader) Close() error { return b.source.Close() }

func (b *bodyReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.stream == nil {
		if b.reader == nil {
			data, err := io.ReadAll(io.LimitReader(b.source, b.limit+1))
			if int64(len(data)) > b.limit {
				// Preserve the caller's existing size-limit detection.
				b.reader = io.MultiReader(bytes.NewReader(data), b.source)
			} else {
				if err == nil {
					data = b.rewriter.Rewrite(data)
				}
				b.reader = bytes.NewReader(data)
				b.err = err
			}
		}
		n, err := b.reader.Read(p)
		if err == io.EOF && b.err != nil {
			err, b.err = b.err, nil
		}
		return n, err
	}
	for len(b.pending) == 0 && b.err == nil {
		b.readEvent()
	}
	if len(b.pending) > 0 {
		n := copy(p, b.pending)
		b.pending = b.pending[n:]
		return n, nil
	}
	return 0, b.err
}

func (b *bodyReader) readEvent() {
	var event []byte
	for {
		line, err := b.stream.ReadSlice('\n')
		event = append(event, line...)
		if int64(len(event)) > b.limit || b.oversize {
			b.pending = event
			b.oversize = err == bufio.ErrBufferFull || len(bytes.TrimSpace(line)) != 0
			if err != nil && err != bufio.ErrBufferFull {
				b.err = err
			}
			return
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil || len(bytes.TrimSpace(line)) == 0 {
			b.err = err
			b.pending = rewriteEvent(event, &b.rewriter)
			return
		}
	}
}

func rewriteEvent(event []byte, rewriter *Rewriter) []byte {
	lines := bytes.SplitAfter(event, []byte("\n"))
	var data [][]byte
	first := -1
	for i, line := range lines {
		if bytes.HasPrefix(line, []byte("data:")) {
			if first < 0 {
				first = i
			}
			value := bytes.TrimSuffix(bytes.TrimSuffix(line[5:], []byte("\n")), []byte("\r"))
			value = bytes.TrimPrefix(value, []byte(" "))
			data = append(data, value)
		}
	}
	if first < 0 {
		return event
	}
	joined := bytes.Join(data, []byte("\n"))
	adjusted := rewriter.Rewrite(joined)
	if bytes.Equal(joined, adjusted) {
		return event
	}
	var out bytes.Buffer
	for i, line := range lines {
		if !bytes.HasPrefix(line, []byte("data:")) {
			_, _ = out.Write(line)
			continue
		}
		if i == first {
			_, _ = out.WriteString("data: ")
			// A multi-line JSON event must remain valid SSE after rewriting.
			_, _ = out.Write(bytes.ReplaceAll(adjusted, []byte("\n"), []byte("\ndata: ")))
			if bytes.HasSuffix(line, []byte("\r\n")) {
				_, _ = out.WriteString("\r\n")
			} else if bytes.HasSuffix(line, []byte("\n")) {
				_ = out.WriteByte('\n')
			}
		}
	}
	return out.Bytes()
}
