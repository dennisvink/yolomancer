package repository

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

type packIndex struct {
	packPath string
	hashes   []OID
	offsets  []uint64
}

func (r *Repository) packedObject(ctx context.Context, oid OID) (Object, error) {
	if err := r.loadPackIndexes(ctx); err != nil {
		return Object{}, err
	}
	for _, index := range r.packs {
		position := sort.Search(len(index.hashes), func(i int) bool { return index.hashes[i] >= oid })
		if position < len(index.hashes) && index.hashes[position] == oid {
			object, err := r.objectAtPackOffset(ctx, index, index.offsets[position])
			if err != nil {
				return Object{}, err
			}
			object.OID = oid
			return object, nil
		}
	}
	return Object{}, fs.ErrNotExist
}

func (r *Repository) loadPackIndexes(ctx context.Context) error {
	r.mu.RLock()
	loaded := r.packsLoaded
	r.mu.RUnlock()
	if loaded {
		return nil
	}
	paths, err := r.objects.List(ctx, "objects/pack")
	if err != nil {
		return err
	}
	var indexes []packIndex
	for _, path := range paths {
		if !strings.HasSuffix(path, ".idx") {
			continue
		}
		data, err := r.objects.Read(ctx, path)
		if err != nil {
			return err
		}
		hashes, offsets, err := parsePackIndex(data)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		indexes = append(indexes, packIndex{packPath: strings.TrimSuffix(path, ".idx") + ".pack", hashes: hashes, offsets: offsets})
	}
	r.mu.Lock()
	if !r.packsLoaded {
		r.packs, r.packsLoaded = indexes, true
	}
	r.mu.Unlock()
	return nil
}

func parsePackIndex(data []byte) ([]OID, []uint64, error) {
	const fanoutBytes = 256 * 4
	if len(data) < 8+fanoutBytes || !bytes.Equal(data[:4], []byte{0xff, 't', 'O', 'c'}) {
		return nil, nil, errors.New("unsupported or truncated pack index")
	}
	if version := binary.BigEndian.Uint32(data[4:8]); version != 2 {
		return nil, nil, fmt.Errorf("unsupported pack index version %d", version)
	}
	position := 8
	count64 := uint64(binary.BigEndian.Uint32(data[position+fanoutBytes-4 : position+fanoutBytes]))
	position += fanoutBytes
	if count64 > uint64((len(data)-position)/20) {
		return nil, nil, errors.New("truncated pack index object ids")
	}
	count := int(count64)
	hashes := make([]OID, count)
	for i := range count {
		hashes[i] = OID(hex.EncodeToString(data[position+i*20 : position+(i+1)*20]))
	}
	position += count * 20
	if count > (len(data)-position)/4 {
		return nil, nil, errors.New("truncated pack index CRC table")
	}
	position += count * 4
	if count > (len(data)-position)/4 {
		return nil, nil, errors.New("truncated pack index offsets")
	}
	rawOffsets := make([]uint32, count)
	for i := range count {
		rawOffsets[i] = binary.BigEndian.Uint32(data[position+i*4 : position+(i+1)*4])
	}
	position += count * 4
	offsets := make([]uint64, count)
	for i, raw := range rawOffsets {
		if raw&0x80000000 == 0 {
			offsets[i] = uint64(raw)
			continue
		}
		large := uint64(raw & 0x7fffffff)
		remaining := len(data) - position
		maxInt := int(^uint(0) >> 1)
		if remaining < 8 || large > uint64(maxInt) || int(large) > (remaining-8)/8 {
			return nil, nil, errors.New("truncated pack index large offsets")
		}
		start := position + int(large)*8
		offsets[i] = binary.BigEndian.Uint64(data[start : start+8])
	}
	return hashes, offsets, nil
}

func (r *Repository) objectAtPackOffset(ctx context.Context, index packIndex, offset uint64) (Object, error) {
	key := fmt.Sprintf("%s:%d", index.packPath, offset)
	r.mu.RLock()
	if object, ok := r.offsetCache[key]; ok {
		r.mu.RUnlock()
		return cloneObject(object), nil
	}
	r.mu.RUnlock()
	pack, err := r.objects.Read(ctx, index.packPath)
	if err != nil {
		return Object{}, err
	}
	object, err := r.decodePackedObject(ctx, index, pack, offset)
	if err != nil {
		return Object{}, err
	}
	r.mu.Lock()
	r.offsetCache[key] = object
	r.mu.Unlock()
	return cloneObject(object), nil
}

func (r *Repository) decodePackedObject(ctx context.Context, index packIndex, pack []byte, offset uint64) (Object, error) {
	if len(pack) < 12 || !bytes.Equal(pack[:4], []byte("PACK")) || offset > uint64(len(pack)) {
		return Object{}, errors.New("invalid pack file or object offset")
	}
	position := int(offset)
	typ, headerLength, err := parsePackObjectHeader(pack[position:])
	if err != nil {
		return Object{}, err
	}
	body := position + headerLength
	switch typ {
	case 1, 2, 3, 4:
		data, err := inflatePackedData(pack[body:], r.maxObjectSize)
		return Object{Type: packType(typ), Data: data}, err
	case 6:
		baseOffset, consumed, err := parseOFSDeltaBase(pack[body:], uint64(position))
		if err != nil {
			return Object{}, err
		}
		delta, err := inflatePackedData(pack[body+consumed:], r.maxObjectSize)
		if err != nil {
			return Object{}, err
		}
		base, err := r.objectAtPackOffset(ctx, index, baseOffset)
		if err != nil {
			return Object{}, err
		}
		data, err := applyDeltaWithLimit(base.Data, delta, r.maxObjectSize)
		return Object{Type: base.Type, Data: data}, err
	case 7:
		if len(pack)-body < 20 {
			return Object{}, errors.New("truncated ref delta")
		}
		baseOID := OID(hex.EncodeToString(pack[body : body+20]))
		delta, err := inflatePackedData(pack[body+20:], r.maxObjectSize)
		if err != nil {
			return Object{}, err
		}
		base, err := r.Object(ctx, baseOID)
		if err != nil {
			return Object{}, err
		}
		data, err := applyDeltaWithLimit(base.Data, delta, r.maxObjectSize)
		return Object{Type: base.Type, Data: data}, err
	default:
		return Object{}, fmt.Errorf("unsupported pack object type %d", typ)
	}
}

func parsePackObjectHeader(data []byte) (int, int, error) {
	if len(data) == 0 {
		return 0, 0, errors.New("truncated pack object header")
	}
	byteValue := data[0]
	typ, position := int((byteValue>>4)&7), 1
	for byteValue&0x80 != 0 {
		if position >= len(data) {
			return 0, 0, errors.New("truncated pack object header")
		}
		byteValue = data[position]
		position++
	}
	return typ, position, nil
}

func inflatePackedData(data []byte, maxSize int64) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return readBounded(reader, maxSize, "packed object")
}

func packType(value int) ObjectType {
	return map[int]ObjectType{1: CommitObject, 2: TreeObject, 3: BlobObject, 4: TagObject}[value]
}

func parseOFSDeltaBase(data []byte, current uint64) (uint64, int, error) {
	if len(data) == 0 {
		return 0, 0, errors.New("truncated ofs delta")
	}
	currentByte := data[0]
	offset, position := uint64(currentByte&0x7f), 1
	for currentByte&0x80 != 0 {
		if position >= len(data) {
			return 0, 0, errors.New("truncated ofs delta")
		}
		currentByte = data[position]
		position++
		offset = ((offset + 1) << 7) | uint64(currentByte&0x7f)
	}
	if offset > current {
		return 0, 0, errors.New("invalid ofs delta base")
	}
	return current - offset, position, nil
}

func applyDelta(base, delta []byte) ([]byte, error) {
	return applyDeltaWithLimit(base, delta, DefaultMaxObjectSize)
}

func applyDeltaWithLimit(base, delta []byte, maxSize int64) ([]byte, error) {
	baseSize, consumed, err := readDeltaVarint(delta)
	if err != nil {
		return nil, err
	}
	if baseSize != len(base) {
		return nil, errors.New("delta base size mismatch")
	}
	position := consumed
	resultSize, consumed, err := readDeltaVarint(delta[position:])
	if err != nil {
		return nil, err
	}
	position += consumed
	if resultSize < 0 || int64(resultSize) > maxSize {
		return nil, fmt.Errorf("delta result exceeds %d bytes", maxSize)
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
						return nil, errors.New("truncated delta copy offset")
					}
					offset |= int(delta[position]) << (8 * i)
					position++
				}
			}
			for i := range 3 {
				if opcode&(1<<uint(4+i)) != 0 {
					if position >= len(delta) {
						return nil, errors.New("truncated delta copy size")
					}
					size |= int(delta[position]) << (8 * i)
					position++
				}
			}
			if size == 0 {
				size = 0x10000
			}
			if offset < 0 || size < 0 || offset > len(base)-size {
				return nil, errors.New("delta copy exceeds base object")
			}
			if size > resultSize-len(result) {
				return nil, errors.New("delta copy exceeds declared result size")
			}
			result = append(result, base[offset:offset+size]...)
			continue
		}
		if opcode == 0 || int(opcode) > len(delta)-position {
			return nil, errors.New("invalid delta insert")
		}
		if int(opcode) > resultSize-len(result) {
			return nil, errors.New("delta insert exceeds declared result size")
		}
		result = append(result, delta[position:position+int(opcode)]...)
		position += int(opcode)
	}
	if len(result) != resultSize {
		return nil, errors.New("delta result size mismatch")
	}
	return result, nil
}

func readDeltaVarint(data []byte) (int, int, error) {
	value, shift := uint64(0), uint(0)
	for i, current := range data {
		if shift >= 63 {
			return 0, 0, errors.New("delta varint overflow")
		}
		value |= uint64(current&0x7f) << shift
		if current&0x80 == 0 {
			if value > uint64(^uint(0)>>1) {
				return 0, 0, errors.New("delta size overflows int")
			}
			return int(value), i + 1, nil
		}
		shift += 7
	}
	return 0, 0, errors.New("truncated delta varint")
}
