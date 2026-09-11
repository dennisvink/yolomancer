// Package transport implements Git smart-protocol transport primitives.
package transport

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const (
	UploadPackService  = "git-upload-pack"
	ReceivePackService = "git-receive-pack"
	MaxPayload         = 65516
)

type PacketKind uint8

const (
	DataPacket PacketKind = iota
	FlushPacket
	DelimiterPacket
	ResponseEndPacket
)

type Packet struct {
	Kind PacketKind
	Data []byte
}

func ReadPacket(reader *bufio.Reader) (Packet, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return Packet{}, err
	}
	switch string(header[:]) {
	case "0000":
		return Packet{Kind: FlushPacket}, nil
	case "0001":
		return Packet{Kind: DelimiterPacket}, nil
	case "0002":
		return Packet{Kind: ResponseEndPacket}, nil
	}
	length, err := strconv.ParseUint(string(header[:]), 16, 16)
	if err != nil || length < 4 {
		return Packet{}, fmt.Errorf("invalid pkt-line length %q", header)
	}
	payload := make([]byte, int(length)-4)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return Packet{}, err
	}
	return Packet{Kind: DataPacket, Data: payload}, nil
}

func WritePacket(writer io.Writer, data []byte) error {
	if len(data) > MaxPayload {
		return errors.New("pkt-line payload too large")
	}
	header := fmt.Sprintf("%04x", len(data)+4)
	if _, err := io.WriteString(writer, header); err != nil {
		return err
	}
	_, err := writer.Write(data)
	return err
}

func WriteString(writer io.Writer, data string) error { return WritePacket(writer, []byte(data)) }
func WriteFlush(writer io.Writer) error               { _, err := io.WriteString(writer, "0000"); return err }
func WriteDelimiter(writer io.Writer) error           { _, err := io.WriteString(writer, "0001"); return err }
func WriteResponseEnd(writer io.Writer) error         { _, err := io.WriteString(writer, "0002"); return err }

func WriteSideband(writer io.Writer, channel byte, data []byte, maxPayload int) error {
	if channel < 1 || channel > 3 {
		return fmt.Errorf("invalid sideband channel %d", channel)
	}
	if maxPayload <= 1 || maxPayload > MaxPayload {
		maxPayload = MaxPayload
	}
	for len(data) > 0 {
		size := maxPayload - 1
		if len(data) < size {
			size = len(data)
		}
		payload := make([]byte, size+1)
		payload[0] = channel
		copy(payload[1:], data[:size])
		if err := WritePacket(writer, payload); err != nil {
			return err
		}
		data = data[size:]
	}
	return nil
}

type Capabilities []string

func (c Capabilities) String() string { return strings.Join(c, " ") }

func UploadPackCapabilities() Capabilities {
	return Capabilities{"multi_ack", "multi_ack_detailed", "no-done", "thin-pack", "side-band", "side-band-64k", "ofs-delta", "no-progress", "include-tag", "allow-tip-sha1-in-want", "allow-reachable-sha1-in-want", "symref=HEAD:refs/heads/main", "object-format=sha1", "agent=bgit"}
}

func ReceivePackCapabilities() Capabilities {
	return Capabilities{"report-status", "report-status-v2", "delete-refs", "side-band-64k", "quiet", "atomic", "ofs-delta", "push-options", "object-format=sha1", "agent=bgit"}
}

func WriteAdvertisedRefs(writer io.Writer, service string, refs map[string]string, capabilities Capabilities) error {
	if service != UploadPackService && service != ReceivePackService {
		return fmt.Errorf("unsupported service %q", service)
	}
	names := make([]string, 0, len(refs))
	for name, oid := range refs {
		if (name == "HEAD" || strings.HasPrefix(name, "refs/")) && validSHA1(oid) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if index := sort.SearchStrings(names, "HEAD"); index < len(names) && names[index] == "HEAD" {
		names = append([]string{"HEAD"}, append(names[:index], names[index+1:]...)...)
	}
	if len(names) == 0 {
		return WriteFlush(writer)
	}
	for index, name := range names {
		line := strings.TrimSpace(refs[name]) + " " + name
		if index == 0 {
			line += "\x00" + capabilities.String()
		}
		if err := WriteString(writer, line+"\n"); err != nil {
			return err
		}
	}
	return WriteFlush(writer)
}

func validSHA1(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}
