// Local worker messages use the same bounded, length-prefixed framing in both directions.
package worker

import (
	"encoding/binary"
	"errors"
	"fmt"
	protoapi "github.com/thomas/poml-node/api/proto"
	"google.golang.org/protobuf/proto"
	"io"
)

func writeFrame(writer io.Writer, envelope *protoapi.Envelope) error {
	payload, err := proto.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(payload) > 16<<20 {
		return errors.New("worker frame is too large")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err = writer.Write(payload)
	return err
}

func readFrame(reader io.Reader) (*protoapi.Envelope, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length > 16<<20 {
		return nil, fmt.Errorf("frame is too large: %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	var envelope protoapi.Envelope
	if err := proto.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	return &envelope, nil
}
