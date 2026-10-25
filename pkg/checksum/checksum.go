package checksum

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"strings"
)

// scanLinesAnyNL splits lines by \r\n, \r, or \n, matching Java BufferedReader.readLine().
func scanLinesAnyNL(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			return i + 1, data[0:i], nil
		}
		if data[i] == '\r' {
			if i+1 < len(data) {
				if data[i+1] == '\n' {
					return i + 2, data[0:i], nil
				}
				return i + 1, data[0:i], nil
			}
			if atEOF {
				return i + 1, data[0:i], nil
			}
			// Request more data to see if \n follows \r
			return 0, nil, nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// Calculate calculates the Flyway-compatible CRC32 checksum for the provided reader.
// It is line-ending and encoding independent:
// 1. Reads line by line (ending a line on \n, \r, or \r\n)
// 2. Removes UTF-8 BOM from the first line if present
// 3. Updates CRC32 (IEEE) with the UTF-8 bytes of each line
// 4. Returns the signed 32-bit integer cast as int64 (matching Flyway's (int) crc32.getValue())
func Calculate(r io.Reader) (int64, error) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	scanner.Split(scanLinesAnyNL)

	crc := crc32.NewIEEE()
	firstLine := true
	hasLines := false

	for scanner.Scan() {
		hasLines = true
		line := scanner.Text()
		if firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			firstLine = false
		}
		crc.Write([]byte(line))
	}

	if err := scanner.Err(); err != nil {
		return 0, err
	}

	if !hasLines {
		return 0, nil
	}

	val := int32(crc.Sum32())
	return int64(val), nil
}

// CalculateString calculates the checksum for a string.
func CalculateString(s string) (int64, error) {
	return Calculate(strings.NewReader(s))
}

// CalculatePreservingBOM calculates CRC32 checksum line by line without stripping any BOM.
// Used when the input stream or string has already had its single BOM stripped (e.g. by decodeContent).
func CalculatePreservingBOM(r io.Reader) (int64, error) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	scanner.Split(scanLinesAnyNL)

	crc := crc32.NewIEEE()
	hasLines := false

	for scanner.Scan() {
		hasLines = true
		line := scanner.Text()
		crc.Write([]byte(line))
	}

	if err := scanner.Err(); err != nil {
		return 0, err
	}

	if !hasLines {
		return 0, nil
	}

	val := int32(crc.Sum32())
	return int64(val), nil
}

// CalculateStringPreservingBOM calculates CRC32 checksum for a string without stripping any BOM.
func CalculateStringPreservingBOM(s string) (int64, error) {
	return CalculatePreservingBOM(strings.NewReader(s))
}

// CalculateBytes calculates the checksum for byte slice.
func CalculateBytes(b []byte) (int64, error) {
	return Calculate(bytes.NewReader(b))
}

// CombineChecksums combines multiple checksums matching Flyway's multi-resource checksum algorithm.
func CombineChecksums(checksums []int64) int64 {
	if len(checksums) == 0 {
		return 0
	}
	if len(checksums) == 1 {
		return checksums[0]
	}
	crc := crc32.NewIEEE()
	b := make([]byte, 4)
	for _, c := range checksums {
		binary.BigEndian.PutUint32(b, uint32(int32(c)))
		crc.Write(b)
	}
	return int64(int32(crc.Sum32()))
}
