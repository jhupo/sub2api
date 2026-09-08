package service

import (
	"bufio"
	"io"
	"time"
)

type openAISSEScanResult struct {
	line string
	err  error
	done bool
}

// The reader alone owns scanning and its buffer. Heartbeats run on the caller
// goroutine, so HTTP headers, event writes and billing never race each other.
type openAISSEKeepaliveScanner struct {
	body      io.ReadCloser
	results   chan openAISSEScanResult
	stop      chan struct{}
	done      chan struct{}
	ticker    *time.Ticker
	heartbeat func()
	line      string
	err       error
	completed bool
	closed    bool
	syncScan  *openAISSEJSONDocumentScanner
	syncBuf   *sseScannerBuf64K
}

func newOpenAISSEKeepaliveScanner(body io.ReadCloser, maxLineSize int, interval time.Duration, heartbeat func()) *openAISSEKeepaliveScanner {
	s := &openAISSEKeepaliveScanner{
		body: body, results: make(chan openAISSEScanResult, 16),
		stop: make(chan struct{}), done: make(chan struct{}), heartbeat: heartbeat,
	}
	if interval <= 0 {
		s.syncBuf = getSSEScannerBuf64K()
		scanner := bufio.NewScanner(body)
		scanner.Buffer(s.syncBuf[:0], maxLineSize)
		s.syncScan = newOpenAISSEJSONDocumentScanner(scanner)
		return s
	}
	s.ticker = time.NewTicker(interval)
	go func() {
		defer close(s.done)
		buffer := getSSEScannerBuf64K()
		defer putSSEScannerBuf64K(buffer)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(buffer[:0], maxLineSize)
		documents := newOpenAISSEJSONDocumentScanner(scanner)
		for documents.Scan() {
			select {
			case s.results <- openAISSEScanResult{line: documents.Text()}:
			case <-s.stop:
				return
			}
		}
		select {
		case s.results <- openAISSEScanResult{err: documents.Err(), done: true}:
		case <-s.stop:
		}
	}()
	return s
}

func (s *openAISSEKeepaliveScanner) Scan() bool {
	if s.completed || s.closed {
		return false
	}
	if s.syncScan != nil {
		if !s.syncScan.Scan() {
			s.completed, s.err = true, s.syncScan.Err()
			return false
		}
		s.line = s.syncScan.Text()
		return true
	}
	var ticks <-chan time.Time
	if s.ticker != nil {
		ticks = s.ticker.C
	}
	for {
		select {
		case result := <-s.results:
			s.line, s.err = result.line, result.err
			s.completed = result.done
			return !result.done
		case <-ticks:
			if s.heartbeat != nil {
				s.heartbeat()
			}
		}
	}
}

func (s *openAISSEKeepaliveScanner) Text() string { return s.line }
func (s *openAISSEKeepaliveScanner) Err() error   { return s.err }
func (s *openAISSEKeepaliveScanner) Close() {
	if s.closed {
		return
	}
	s.closed = true
	if s.syncScan != nil {
		_ = s.body.Close()
		putSSEScannerBuf64K(s.syncBuf)
		return
	}
	if s.ticker != nil {
		s.ticker.Stop()
	}
	close(s.stop)
	_ = s.body.Close()
	<-s.done
}
