package apps

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const maxLogLine = 16 << 10

// demuxLines reads Docker's multiplexed log stream (8-byte frame headers)
// and calls fn for every complete line with its stream name ("stdout" or
// "stderr"). It stops when fn returns false or the stream ends.
func demuxLines(r io.Reader, fn func(stream, line string) bool) error {
	var header [8]byte
	pending := map[byte]*bytes.Buffer{}
	buf := make([]byte, 32<<10)
	flush := func(kind byte, final bool) bool {
		b := pending[kind]
		if b == nil {
			return true
		}
		for {
			data := b.Bytes()
			i := bytes.IndexByte(data, '\n')
			if i < 0 {
				if len(data) > maxLogLine || (final && len(data) > 0) {
					line := string(data)
					b.Reset()
					return fn(streamName(kind), cleanLine(line))
				}
				return true
			}
			line := string(data[:i])
			b.Next(i + 1)
			if !fn(streamName(kind), cleanLine(line)) {
				return false
			}
		}
	}
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			for kind := range pending {
				if !flush(kind, true) {
					return nil
				}
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		kind := header[0]
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if kind > 3 {
			return errors.New("log akışı çözümlenemedi")
		}
		b := pending[kind]
		if b == nil {
			b = &bytes.Buffer{}
			pending[kind] = b
		}
		for size > 0 {
			n := int64(len(buf))
			if size < n {
				n = size
			}
			read, err := io.ReadFull(r, buf[:n])
			b.Write(buf[:read])
			size -= int64(read)
			if err != nil {
				flush(kind, true)
				return nil
			}
			if !flush(kind, false) {
				return nil
			}
		}
	}
}

func streamName(kind byte) string {
	if kind == 2 {
		return "stderr"
	}
	return "stdout"
}

func cleanLine(s string) string {
	s = strings.TrimRight(s, "\r")
	if len(s) > maxLogLine {
		s = s[:maxLogLine]
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return s
}

// splitTimestamp separates the RFC 3339 timestamp Docker puts in front of a
// log line when timestamps are requested.
func splitTimestamp(line string) (ts, rest string) {
	i := strings.IndexByte(line, ' ')
	if i < 20 || i > 35 || line[4] != '-' || line[10] != 'T' {
		return "", line
	}
	return line[:i], line[i+1:]
}
