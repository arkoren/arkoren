package stream

import "bytes"

// Event frames one SSE value, normalizing CRLF and bare CR to prevent field
// injection. It retains the final empty data line so EventSource preserves the
// payload's trailing newline rather than consuming it as framing.
func Event(value []byte) []byte {
	var buffer bytes.Buffer
	value = bytes.ReplaceAll(value, []byte("\r\n"), []byte("\n"))
	value = bytes.ReplaceAll(value, []byte("\r"), []byte("\n"))

	for line := range bytes.SplitSeq(value, []byte("\n")) {
		buffer.WriteString("data: ")
		buffer.Write(line)
		buffer.WriteByte('\n')
	}

	buffer.WriteByte('\n')

	return buffer.Bytes()
}
