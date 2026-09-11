package transport

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/bucketgit/bgit/repository"
	"github.com/bucketgit/bgit/store"
)

const zeroOID = "0000000000000000000000000000000000000000"

const DefaultMaxReceivePackSize int64 = 512 << 20

type ReceiveCommand struct {
	Old, New repository.OID
	Ref      string
	Delete   bool
	Create   bool
}
type ReceiveRequest struct {
	Commands     []ReceiveCommand
	Capabilities map[string]bool
	PushOptions  []string
	Pack         io.Reader
}

type ReceiveStore interface {
	store.Writer
	store.RefStore
}

func ServeReceivePack(ctx context.Context, repo *repository.Repository, target ReceiveStore, input io.Reader, output io.Writer) error {
	request, err := ReadReceivePackRequest(input)
	if err != nil {
		return err
	}
	if len(request.Commands) == 0 {
		return nil
	}
	received := map[repository.OID]repository.Object{}
	if needsPack(request.Commands) {
		received, err = ingestPack(ctx, repo, target, request.Pack)
	}
	if err != nil {
		if reportsStatus(request.Capabilities) {
			_ = writeReceiveReport(output, request, err, nil)
		}
		return err
	}
	commandErrors, applyErr := applyCommands(ctx, repo, target, request.Commands, request.Capabilities["atomic"], received)
	if reportsStatus(request.Capabilities) {
		if reportErr := writeReceiveReport(output, request, nil, commandErrors); reportErr != nil && applyErr == nil {
			return reportErr
		}
	}
	return applyErr
}

func ReadReceivePackRequest(input io.Reader) (ReceiveRequest, error) {
	reader := bufio.NewReader(input)
	request := ReceiveRequest{Capabilities: map[string]bool{}}
	for {
		packet, err := ReadPacket(reader)
		if err != nil {
			return request, err
		}
		if packet.Kind == FlushPacket {
			if request.Capabilities["push-options"] {
				request.PushOptions, err = readPushOptions(reader)
				if err != nil {
					return request, err
				}
			}
			request.Pack = reader
			return request, nil
		}
		if packet.Kind != DataPacket {
			continue
		}
		command, capabilities, err := parseReceiveCommand(string(packet.Data), len(request.Commands) == 0)
		if err != nil {
			return request, err
		}
		request.Commands = append(request.Commands, command)
		for capability := range capabilities {
			request.Capabilities[capability] = true
		}
	}
}

func parseReceiveCommand(line string, first bool) (ReceiveCommand, map[string]bool, error) {
	line = strings.TrimRight(line, "\n")
	capabilities := map[string]bool{}
	if first {
		if command, text, ok := strings.Cut(line, "\x00"); ok {
			line = command
			for _, capability := range strings.Fields(text) {
				capabilities[capability] = true
				if name, _, ok := strings.Cut(capability, "="); ok {
					capabilities[name] = true
				}
			}
		}
	}
	fields := strings.Fields(line)
	if len(fields) != 3 || !strings.HasPrefix(fields[2], "refs/") {
		return ReceiveCommand{}, nil, fmt.Errorf("invalid receive-pack command %q", line)
	}
	oldZero, newZero := fields[0] == zeroOID, fields[1] == zeroOID
	var oldOID, newOID repository.OID
	var err error
	if !oldZero {
		oldOID, err = repository.ParseOID(fields[0])
		if err != nil {
			return ReceiveCommand{}, nil, err
		}
	}
	if !newZero {
		newOID, err = repository.ParseOID(fields[1])
		if err != nil {
			return ReceiveCommand{}, nil, err
		}
	}
	return ReceiveCommand{Old: oldOID, New: newOID, Ref: fields[2], Create: oldZero && !newZero, Delete: !oldZero && newZero}, capabilities, nil
}

func readPushOptions(reader *bufio.Reader) ([]string, error) {
	var options []string
	for {
		packet, err := ReadPacket(reader)
		if err != nil {
			return nil, err
		}
		if packet.Kind == FlushPacket {
			return options, nil
		}
		if packet.Kind != DataPacket {
			return nil, errors.New("invalid push-options packet")
		}
		options = append(options, strings.TrimRight(string(packet.Data), "\n"))
	}
}

func applyCommands(ctx context.Context, repo *repository.Repository, target ReceiveStore, commands []ReceiveCommand, atomic bool, received map[repository.OID]repository.Object) (map[string]error, error) {
	refs, err := target.ListRefs(ctx)
	if err != nil {
		return nil, err
	}
	commandErrors := map[string]error{}
	var firstErr error
	for _, command := range commands {
		if err := validateCommand(ctx, repo, refs, command, received); err != nil {
			commandErrors[command.Ref] = err
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if atomic && firstErr != nil {
		for _, command := range commands {
			if commandErrors[command.Ref] == nil {
				commandErrors[command.Ref] = errors.New("atomic push failed")
			}
		}
		return commandErrors, firstErr
	}
	for _, command := range commands {
		if commandErrors[command.Ref] != nil {
			continue
		}
		oldValue, newValue := command.Old.String(), command.New.String()
		if command.Create {
			oldValue = ""
		}
		if command.Delete {
			newValue = ""
		}
		if err := target.CompareAndSwapRef(ctx, command.Ref, oldValue, newValue); err != nil {
			commandErrors[command.Ref] = err
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if command.Delete {
			delete(refs, command.Ref)
		} else {
			refs[command.Ref] = command.New.String()
		}
	}
	return commandErrors, firstErr
}

func validateCommand(ctx context.Context, repo *repository.Repository, refs map[string]string, command ReceiveCommand, received map[repository.OID]repository.Object) error {
	current, exists := refs[command.Ref]
	switch {
	case command.Create:
		if exists {
			return errors.New("ref already exists")
		}
	case command.Delete:
		if !exists {
			return errors.New("ref does not exist")
		}
		if current != command.Old.String() {
			return errors.New("stale ref")
		}
	case command.Old != "" && command.New != "":
		if !exists {
			return errors.New("ref does not exist")
		}
		if current != command.Old.String() {
			return errors.New("stale ref")
		}
		if strings.HasPrefix(command.Ref, "refs/heads/") {
			ancestor, err := isAncestor(ctx, repo, received, command.Old, command.New)
			if err != nil {
				return err
			}
			if !ancestor {
				return errors.New("non-fast-forward update")
			}
		}
	default:
		return nil
	}
	if command.Delete {
		return nil
	}
	if _, ok := received[command.New]; ok {
		return nil
	}
	if _, err := repo.Object(ctx, command.New); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("missing object %s", command.New)
		}
		return err
	}
	return nil
}

func isAncestor(ctx context.Context, repo *repository.Repository, received map[repository.OID]repository.Object, ancestor, descendant repository.OID) (bool, error) {
	stack, seen := []repository.OID{descendant}, map[repository.OID]struct{}{}
	for len(stack) > 0 {
		oid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if oid == ancestor {
			return true, nil
		}
		if _, ok := seen[oid]; ok {
			continue
		}
		seen[oid] = struct{}{}
		commit, err := commitFromReceived(ctx, repo, received, oid)
		if err != nil {
			return false, err
		}
		stack = append(stack, commit.Parents...)
	}
	return false, nil
}

func commitFromReceived(ctx context.Context, repo *repository.Repository, received map[repository.OID]repository.Object, oid repository.OID) (repository.Commit, error) {
	if object, ok := received[oid]; ok {
		if object.Type != repository.CommitObject {
			return repository.Commit{}, fmt.Errorf("object %s is %s, not commit", oid, object.Type)
		}
		return repository.ParseCommitData(oid, object.Data)
	}
	return repo.Commit(ctx, oid)
}

type packedRecord struct {
	offset     int
	typ        int
	data       []byte
	baseOffset int
	baseOID    repository.OID
	oid        repository.OID
}

func ingestPack(ctx context.Context, repo *repository.Repository, target store.Writer, input io.Reader) (map[repository.OID]repository.Object, error) {
	data, err := io.ReadAll(io.LimitReader(input, DefaultMaxReceivePackSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > DefaultMaxReceivePackSize {
		return nil, fmt.Errorf("receive pack exceeds %d bytes", DefaultMaxReceivePackSize)
	}
	objects, err := decodeReceivedPack(ctx, repo, data)
	if err != nil {
		return nil, err
	}
	result := make(map[repository.OID]repository.Object, len(objects))
	for _, object := range objects {
		if err := writeLooseObject(ctx, target, object); err != nil {
			return nil, err
		}
		result[object.OID] = object
	}
	return result, nil
}

func decodeReceivedPack(ctx context.Context, repo *repository.Repository, pack []byte) ([]repository.Object, error) {
	if len(pack) < 32 || !bytes.Equal(pack[:4], []byte("PACK")) {
		return nil, errors.New("invalid pack file")
	}
	if sum := sha1.Sum(pack[:len(pack)-20]); !bytes.Equal(sum[:], pack[len(pack)-20:]) {
		return nil, errors.New("pack checksum mismatch")
	}
	version := binaryUint32(pack[4:8])
	if version != 2 && version != 3 {
		return nil, fmt.Errorf("unsupported pack version %d", version)
	}
	count := uint64(binaryUint32(pack[8:12]))
	if count > uint64((len(pack)-32)/2) {
		return nil, errors.New("invalid pack object count")
	}
	position := 12
	records := make([]packedRecord, 0, int(count))
	byOffset := map[int]int{}
	byOID := map[repository.OID]int{}
	for range count {
		if position >= len(pack)-20 {
			return nil, errors.New("truncated pack object")
		}
		offset := position
		typ, header, err := parsePackedHeader(pack[position:])
		if err != nil {
			return nil, err
		}
		position += header
		record := packedRecord{offset: offset, typ: typ}
		switch typ {
		case 1, 2, 3, 4:
			record.data, header, err = inflateEntry(pack[position:], repository.DefaultMaxObjectSize)
			if err != nil {
				return nil, err
			}
			position += header
			record.oid = hashObject(packObjectType(typ), record.data)
			byOID[record.oid] = len(records)
		case 6:
			base, consumed, err := parseOffsetDelta(pack[position:], uint64(offset))
			if err != nil {
				return nil, err
			}
			position += consumed
			record.baseOffset = int(base)
			record.data, consumed, err = inflateEntry(pack[position:], repository.DefaultMaxObjectSize)
			if err != nil {
				return nil, err
			}
			position += consumed
		case 7:
			if len(pack)-position < 20 {
				return nil, errors.New("truncated ref delta")
			}
			record.baseOID = repository.OID(hex.EncodeToString(pack[position : position+20]))
			position += 20
			record.data, header, err = inflateEntry(pack[position:], repository.DefaultMaxObjectSize)
			if err != nil {
				return nil, err
			}
			position += header
		default:
			return nil, fmt.Errorf("unsupported pack object type %d", typ)
		}
		byOffset[offset] = len(records)
		records = append(records, record)
	}
	if position != len(pack)-20 {
		return nil, errors.New("pack object data does not end at checksum")
	}
	resolved := map[int]repository.Object{}
	resolving := map[int]bool{}
	var resolve func(int) (repository.Object, error)
	resolve = func(index int) (repository.Object, error) {
		if object, ok := resolved[index]; ok {
			return object, nil
		}
		if resolving[index] {
			return repository.Object{}, errors.New("cyclic pack delta")
		}
		resolving[index] = true
		defer delete(resolving, index)
		record := records[index]
		if record.typ >= 1 && record.typ <= 4 {
			object := repository.Object{OID: record.oid, Type: packObjectType(record.typ), Data: record.data}
			resolved[index] = object
			return object, nil
		}
		var base repository.Object
		if record.typ == 6 {
			baseIndex, ok := byOffset[record.baseOffset]
			if !ok {
				return repository.Object{}, errors.New("missing ofs-delta base")
			}
			var err error
			base, err = resolve(baseIndex)
			if err != nil {
				return repository.Object{}, err
			}
		} else if baseIndex, ok := byOID[record.baseOID]; ok {
			var err error
			base, err = resolve(baseIndex)
			if err != nil {
				return repository.Object{}, err
			}
		} else {
			var err error
			base, err = repo.Object(ctx, record.baseOID)
			if err != nil {
				return repository.Object{}, fmt.Errorf("missing ref-delta base %s", record.baseOID)
			}
		}
		data, err := applyPackDelta(base.Data, record.data)
		if err != nil {
			return repository.Object{}, err
		}
		object := repository.Object{Type: base.Type, Data: data}
		object.OID = hashObject(object.Type, data)
		resolved[index] = object
		byOID[object.OID] = index
		return object, nil
	}
	objects := make([]repository.Object, len(records))
	for index := range records {
		object, err := resolve(index)
		if err != nil {
			return nil, err
		}
		objects[index] = object
	}
	return objects, nil
}

func writeLooseObject(ctx context.Context, target store.Writer, object repository.Object) error {
	raw := append([]byte(fmt.Sprintf("%s %d\x00", object.Type, len(object.Data))), object.Data...)
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	oid := object.OID.String()
	return target.Write(ctx, "objects/"+oid[:2]+"/"+oid[2:], compressed.Bytes())
}

func writeReceiveReport(output io.Writer, request ReceiveRequest, unpackErr error, commandErrors map[string]error) error {
	var report strings.Builder
	if unpackErr != nil {
		_ = WriteString(&report, "unpack "+unpackErr.Error()+"\n")
	} else {
		_ = WriteString(&report, "unpack ok\n")
	}
	for _, command := range request.Commands {
		if err := commandErrors[command.Ref]; err != nil {
			_ = WriteString(&report, "ng "+command.Ref+" "+err.Error()+"\n")
		} else {
			_ = WriteString(&report, "ok "+command.Ref+"\n")
		}
	}
	_ = WriteFlush(&report)
	if !request.Capabilities["side-band-64k"] {
		_, err := io.WriteString(output, report.String())
		return err
	}
	if err := WriteSideband(output, 1, []byte(report.String()), MaxPayload); err != nil {
		return err
	}
	return WriteFlush(output)
}

func reportsStatus(c map[string]bool) bool { return c["report-status"] || c["report-status-v2"] }
func needsPack(commands []ReceiveCommand) bool {
	for _, command := range commands {
		if !command.Delete && command.New != "" {
			return true
		}
	}
	return false
}
func binaryUint32(data []byte) uint32 {
	return uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
}
func packObjectType(value int) repository.ObjectType {
	return map[int]repository.ObjectType{1: repository.CommitObject, 2: repository.TreeObject, 3: repository.BlobObject, 4: repository.TagObject}[value]
}
func hashObject(typ repository.ObjectType, data []byte) repository.OID {
	raw := append([]byte(fmt.Sprintf("%s %d\x00", typ, len(data))), data...)
	sum := sha1.Sum(raw)
	return repository.OID(hex.EncodeToString(sum[:]))
}

func parsePackedHeader(data []byte) (int, int, error) {
	if len(data) == 0 {
		return 0, 0, errors.New("truncated pack object header")
	}
	current := data[0]
	typ, pos := int((current>>4)&7), 1
	for current&0x80 != 0 {
		if pos >= len(data) {
			return 0, 0, errors.New("truncated pack object header")
		}
		current = data[pos]
		pos++
	}
	return typ, pos, nil
}
func inflateEntry(data []byte, maxSize int64) ([]byte, int, error) {
	source := bytes.NewReader(data)
	reader, err := zlib.NewReader(source)
	if err != nil {
		return nil, 0, err
	}
	var output bytes.Buffer
	if _, err = io.Copy(&output, io.LimitReader(reader, maxSize+1)); err != nil {
		_ = reader.Close()
		return nil, 0, err
	}
	if err = reader.Close(); err != nil {
		return nil, 0, err
	}
	if int64(output.Len()) > maxSize {
		return nil, 0, fmt.Errorf("packed object exceeds %d bytes", maxSize)
	}
	return output.Bytes(), len(data) - source.Len(), nil
}
func parseOffsetDelta(data []byte, current uint64) (uint64, int, error) {
	if len(data) == 0 {
		return 0, 0, errors.New("truncated ofs delta")
	}
	value := data[0]
	offset, pos := uint64(value&0x7f), 1
	for value&0x80 != 0 {
		if pos >= len(data) {
			return 0, 0, errors.New("truncated ofs delta")
		}
		value = data[pos]
		pos++
		offset = ((offset + 1) << 7) | uint64(value&0x7f)
	}
	if offset > current {
		return 0, 0, errors.New("invalid ofs delta base")
	}
	return current - offset, pos, nil
}
func applyPackDelta(base, delta []byte) ([]byte, error) {
	baseSize, n, err := deltaVarint(delta)
	if err != nil || baseSize != len(base) {
		return nil, errors.New("delta base size mismatch")
	}
	position := n
	resultSize, n, err := deltaVarint(delta[position:])
	if err != nil {
		return nil, err
	}
	position += n
	if resultSize < 0 || int64(resultSize) > repository.DefaultMaxObjectSize {
		return nil, fmt.Errorf("delta result exceeds %d bytes", repository.DefaultMaxObjectSize)
	}
	capacity := resultSize
	if capacity > len(base)+len(delta) {
		capacity = len(base) + len(delta)
	}
	if capacity > 1<<20 {
		capacity = 1 << 20
	}
	result := make([]byte, 0, capacity)
	for position < len(delta) {
		opcode := delta[position]
		position++
		if opcode&0x80 != 0 {
			offset, size := 0, 0
			for i := range 4 {
				if opcode&(1<<uint(i)) != 0 {
					if position >= len(delta) {
						return nil, errors.New("truncated delta copy")
					}
					offset |= int(delta[position]) << (8 * i)
					position++
				}
			}
			for i := range 3 {
				if opcode&(1<<uint(4+i)) != 0 {
					if position >= len(delta) {
						return nil, errors.New("truncated delta copy")
					}
					size |= int(delta[position]) << (8 * i)
					position++
				}
			}
			if size == 0 {
				size = 0x10000
			}
			if size > len(base) || offset > len(base)-size {
				return nil, errors.New("delta copy exceeds base")
			}
			if size > resultSize-len(result) {
				return nil, errors.New("delta copy exceeds declared result size")
			}
			result = append(result, base[offset:offset+size]...)
		} else {
			size := int(opcode)
			if size == 0 || size > len(delta)-position {
				return nil, errors.New("invalid delta insert")
			}
			if size > resultSize-len(result) {
				return nil, errors.New("delta insert exceeds declared result size")
			}
			result = append(result, delta[position:position+size]...)
			position += size
		}
	}
	if len(result) != resultSize {
		return nil, errors.New("delta result size mismatch")
	}
	return result, nil
}
func deltaVarint(data []byte) (int, int, error) {
	value, shift := uint64(0), uint(0)
	for index, current := range data {
		if shift >= 63 {
			return 0, 0, errors.New("delta varint overflow")
		}
		value |= uint64(current&0x7f) << shift
		if current&0x80 == 0 {
			if value > uint64(^uint(0)>>1) {
				return 0, 0, errors.New("delta size overflow")
			}
			return int(value), index + 1, nil
		}
		shift += 7
	}
	return 0, 0, errors.New("truncated delta varint")
}
