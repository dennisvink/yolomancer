package transport

import (
	"bufio"
	"bytes"
	"testing"
)

func TestPacketRoundTripAndSpecialPackets(t *testing.T) {
	var buffer bytes.Buffer
	if err := WriteString(&buffer, "want abc\n"); err != nil {
		t.Fatal(err)
	}
	if err := WriteFlush(&buffer); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(&buffer)
	packet, err := ReadPacket(reader)
	if err != nil || packet.Kind != DataPacket || string(packet.Data) != "want abc\n" {
		t.Fatalf("packet = %#v, %v", packet, err)
	}
	packet, err = ReadPacket(reader)
	if err != nil || packet.Kind != FlushPacket {
		t.Fatalf("flush = %#v, %v", packet, err)
	}
}

func TestAdvertisedRefs(t *testing.T) {
	var buffer bytes.Buffer
	err := WriteAdvertisedRefs(&buffer, UploadPackService, map[string]string{"refs/heads/main": "0123456789abcdef0123456789abcdef01234567"}, UploadPackCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	packet, err := ReadPacket(bufio.NewReader(&buffer))
	if err != nil || !bytes.Contains(packet.Data, []byte("side-band-64k")) {
		t.Fatalf("advertisement = %q, %v", packet.Data, err)
	}
}
