package verify

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const signedChunked = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"

var chunkHeader = regexp.MustCompile(`^([0-9a-fA-F]{1,16});chunk-signature=([0-9a-f]{64})\r\n$`)
var decodedLength = regexp.MustCompile(`^[0-9]{1,19}$`)

func chunkEncodings(r *http.Request) (int, string) {
	var other []string
	count := 0
	for _, value := range r.Header.Values("Content-Encoding") {
		for _, item := range strings.Split(value, ",") {
			encoding := strings.TrimSpace(item)
			if strings.EqualFold(encoding, "aws-chunked") {
				count++
			} else if encoding != "" {
				other = append(other, encoding)
			}
		}
	}
	return count, strings.Join(other, ", ")
}

// decodeChunks consumes the caller's bounded body and publishes it only when the
// seed, every data chunk, the final empty chunk and EOF have been checked.
func decodeChunks(r *http.Request, creds aws.Credentials, region, seed, encoding string, date time.Time) (string, error) {
	values := r.Header.Values("X-Amz-Decoded-Content-Length")
	if len(values) != 1 || !decodedLength.MatchString(values[0]) || r.Body == nil {
		return "", signatureError()
	}
	length, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return "", signatureError()
	}
	defer r.Body.Close()
	initial, _ := hex.DecodeString(seed) // Verified by readAuth and the seed signature check.
	// EventStream's signer uses the same chain string-to-sign with the hash of
	// its headers in the fifth field. S3 signed chunks use the empty hash there.
	signer := v4.NewStreamSigner(creds, "s3", region, initial)
	wire := bufio.NewReader(r.Body)
	var body bytes.Buffer
	for {
		line, err := wire.ReadSlice('\n')
		if err != nil {
			return "", chunkReadError(err)
		}
		fields := chunkHeader.FindSubmatch(line)
		if fields == nil {
			return "", signatureError()
		}
		size, err := strconv.ParseInt(string(fields[1]), 16, 64)
		if err != nil || size > length-int64(body.Len()) {
			return "", signatureError()
		}
		start := body.Len()
		if _, err := io.CopyN(&body, wire, size); err != nil {
			return "", chunkReadError(err)
		}
		var end [2]byte
		if _, err := io.ReadFull(wire, end[:]); err != nil {
			return "", chunkReadError(err)
		}
		if end != [2]byte{'\r', '\n'} {
			return "", signatureError()
		}
		supplied, _ := hex.DecodeString(string(fields[2]))
		expected, err := signer.GetSignature(r.Context(), nil, body.Bytes()[start:], date)
		if err != nil || !hmac.Equal(expected, supplied) {
			return "", signatureError()
		}
		if size == 0 {
			break
		}
	}
	if _, err := wire.ReadByte(); err != io.EOF {
		return "", chunkReadError(err)
	}
	if int64(body.Len()) != length {
		return "", signatureError()
	}
	sum := sha256.Sum256(body.Bytes())
	r.ContentLength = length
	r.GetBody = nil
	r.Header.Del("Content-Length")
	r.Header.Del("X-Amz-Decoded-Content-Length")
	r.Header.Del("Content-Encoding")
	if encoding != "" {
		r.Header.Set("Content-Encoding", encoding)
	}
	r.TransferEncoding = nil
	r.Trailer = nil
	r.Header.Del("Trailer")
	if length == 0 {
		r.Body = http.NoBody
	} else {
		r.Body = io.NopCloser(bytes.NewReader(body.Bytes()))
	}
	return hex.EncodeToString(sum[:]), nil
}

func chunkReadError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &Error{http.StatusRequestEntityTooLarge, "EntityTooLarge", "The chunked request body exceeds the maximum allowed size."}
	}
	return signatureError()
}
