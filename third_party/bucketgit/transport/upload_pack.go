package transport

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bucketgit/bgit/repository"
)

type UploadPackRequest struct {
	Wants      []repository.OID
	Haves      []repository.OID
	Done       bool
	Sideband   bool
	Sideband64 bool
	responded  bool
}

func ServeUploadPack(ctx context.Context, repo *repository.Repository, input io.Reader, output io.Writer) error {
	request, err := ReadUploadPackRequest(input, output)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(request.Wants) == 0 {
		return nil
	}
	if !request.responded {
		if err := WriteString(output, "NAK\n"); err != nil {
			return err
		}
	}
	oids, err := repo.ReachableObjects(ctx, request.Wants, request.Haves)
	if err != nil {
		return err
	}
	pack, err := EncodePack(ctx, repo, oids)
	if err != nil {
		return err
	}
	if request.Sideband || request.Sideband64 {
		maxPayload := 1000
		if request.Sideband64 {
			maxPayload = MaxPayload
		}
		if err := WriteSideband(output, 1, pack, maxPayload); err != nil {
			return err
		}
		return WriteFlush(output)
	}
	_, err = output.Write(pack)
	return err
}

func ReadUploadPackRequest(input io.Reader, responses io.Writer) (UploadPackRequest, error) {
	reader := bufio.NewReader(input)
	var request UploadPackRequest
	for {
		packet, err := ReadPacket(reader)
		if err != nil {
			if errors.Is(err, io.EOF) && len(request.Wants) > 0 {
				return request, nil
			}
			return request, err
		}
		if packet.Kind != DataPacket {
			if len(request.Wants) > 0 && request.Done {
				return request, nil
			}
			if len(request.Wants) > 0 && responses != nil {
				if err := WriteString(responses, "NAK\n"); err != nil {
					return request, err
				}
				request.responded = true
			}
			continue
		}
		fields := strings.Fields(strings.TrimSpace(string(packet.Data)))
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "want":
			if len(fields) < 2 {
				continue
			}
			oid, err := repository.ParseOID(fields[1])
			if err != nil {
				continue
			}
			request.Wants = append(request.Wants, oid)
			for _, capability := range fields[2:] {
				if capability == "side-band" {
					request.Sideband = true
				}
				if capability == "side-band-64k" {
					request.Sideband64 = true
				}
			}
		case "have":
			if len(fields) == 2 {
				if oid, err := repository.ParseOID(fields[1]); err == nil {
					request.Haves = append(request.Haves, oid)
				}
			}
		case "done":
			request.Done = true
			return request, nil
		}
	}
}

func EncodePack(ctx context.Context, repo *repository.Repository, oids []repository.OID) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString("PACK")
	_ = binary.Write(&buffer, binary.BigEndian, uint32(2))
	if uint64(len(oids)) > uint64(^uint32(0)) {
		return nil, errors.New("too many pack objects")
	}
	_ = binary.Write(&buffer, binary.BigEndian, uint32(len(oids)))
	for _, oid := range oids {
		object, err := repo.Object(ctx, oid)
		if err != nil {
			return nil, err
		}
		if err := writePackObject(&buffer, object); err != nil {
			return nil, fmt.Errorf("write pack object %s: %w", oid, err)
		}
	}
	sum := sha1.Sum(buffer.Bytes())
	buffer.Write(sum[:])
	return buffer.Bytes(), nil
}

func writePackObject(writer io.Writer, object repository.Object) error {
	types := map[repository.ObjectType]int{repository.CommitObject: 1, repository.TreeObject: 2, repository.BlobObject: 3, repository.TagObject: 4}
	typ := types[object.Type]
	if typ == 0 {
		return fmt.Errorf("unsupported object type %q", object.Type)
	}
	if err := writePackHeader(writer, typ, len(object.Data)); err != nil {
		return err
	}
	compressor := zlib.NewWriter(writer)
	if _, err := compressor.Write(object.Data); err != nil {
		_ = compressor.Close()
		return err
	}
	return compressor.Close()
}

func writePackHeader(writer io.Writer, typ, size int) error {
	if size < 0 {
		return errors.New("negative pack object size")
	}
	first := byte((typ << 4) | (size & 0x0f))
	size >>= 4
	if size > 0 {
		first |= 0x80
	}
	if _, err := writer.Write([]byte{first}); err != nil {
		return err
	}
	for size > 0 {
		current := byte(size & 0x7f)
		size >>= 7
		if size > 0 {
			current |= 0x80
		}
		if _, err := writer.Write([]byte{current}); err != nil {
			return err
		}
	}
	return nil
}
