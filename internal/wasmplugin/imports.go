package wasmplugin

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/tetratelabs/wazero/api"
)

type invocationKey struct{}
type invocation struct {
	read         Reader
	config       Config
	calls, bytes int
	readTime     time.Duration
	last         string
}

func writeGuest(ctx context.Context, m api.Module, data []byte) (uint32, error) {
	alloc := m.ExportedFunction("alloc")
	if alloc == nil {
		return 0, errors.New("missing alloc")
	}
	out, e := alloc.Call(ctx, uint64(len(data)))
	if e != nil || len(out) != 1 {
		return 0, errors.New("allocation failed")
	}
	ptr := uint32(out[0])
	if m.Memory() == nil || !m.Memory().Write(ptr, data) {
		return 0, errors.New("invalid guest pointer")
	}
	return ptr, nil
}
func returnGuest(ctx context.Context, m api.Module, data []byte) uint64 {
	p, e := writeGuest(ctx, m, data)
	if e != nil {
		panic(e)
	}
	return uint64(p)<<32 | uint64(len(data))
}
func guestBytes(m api.Module, p, n uint32) []byte {
	if m.Memory() == nil {
		panic("missing guest memory")
	}
	b, ok := m.Memory().Read(p, n)
	if !ok {
		panic("invalid guest pointer")
	}
	return b
}
func (s *invocation) file(ctx context.Context, path string) ([]byte, error) {
	started := time.Now()
	defer func() { s.readTime += time.Since(started) }()
	s.calls++
	if s.calls > s.config.ReadCalls {
		return nil, errors.New("host read count exceeded")
	}
	if !utf8.ValidString(path) || len(path) > 4096 {
		return nil, errors.New("invalid repository path")
	}
	b, e := s.read(ctx, path)
	if e != nil {
		return nil, e
	}
	s.bytes += len(b)
	if len(b) > s.config.FileBytes || s.bytes > s.config.ReadBytes {
		return nil, errors.New("host read bytes exceeded")
	}
	return b, nil
}

// read_file(path_ptr,path_len)->packed pointer/length, raw bytes. Zero indicates
// error; last_error returns UTF-8 diagnostic. Guest frees every returned buffer.
func hostReadFile(ctx context.Context, m api.Module, p, n uint32) uint64 {
	s := ctx.Value(invocationKey{}).(*invocation)
	if n > 4096 {
		s.last = "path too long"
		return 0
	}
	b, e := s.file(ctx, string(guestBytes(m, p, n)))
	if e != nil {
		s.last = e.Error()
		return 0
	}
	s.last = ""
	return returnGuest(ctx, m, b)
}
func hostLastError(ctx context.Context, m api.Module) uint64 {
	return returnGuest(ctx, m, []byte(ctx.Value(invocationKey{}).(*invocation).last))
}

// read_files takes a JSON array of paths. Output is binary framing: LE u32
// count, then for each file LE u32 status (0=bytes,1=UTF8 error), LE u32 length,
// followed by that many raw bytes. Batch failures are isolated per path.
func hostReadFiles(ctx context.Context, m api.Module, p, n uint32) uint64 {
	s := ctx.Value(invocationKey{}).(*invocation)
	if n > 1<<20 {
		s.last = "path list too large"
		return 0
	}
	var paths []string
	if json.Unmarshal(guestBytes(m, p, n), &paths) != nil || len(paths) > s.config.ReadCalls {
		s.last = "invalid path list"
		return 0
	}
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(paths)))
	for _, path := range paths {
		b, e := s.file(ctx, path)
		status := uint32(0)
		if e != nil {
			status = 1
			b = []byte(e.Error())
		}
		out = binary.LittleEndian.AppendUint32(out, status)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(b)))
		out = append(out, b...)
	}
	return returnGuest(ctx, m, out)
}
