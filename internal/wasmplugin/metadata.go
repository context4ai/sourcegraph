package wasmplugin

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
)

// Inspect parses only section framing and the advisory custom section. It does
// not compile, instantiate, run start functions or invoke repository code.
func Inspect(wasm []byte, limit int) (Metadata, error) {
	var out Metadata
	if len(wasm) < 8 || !bytes.Equal(wasm[:8], []byte{0, 97, 115, 109, 1, 0, 0, 0}) {
		return out, errors.New("not a WebAssembly v1 binary (Git LFS pointers are not supported)")
	}
	for rest := wasm[8:]; len(rest) > 0; {
		id := rest[0]
		rest = rest[1:]
		n, k := binary.Uvarint(rest)
		if k <= 0 || k > 5 || n > uint64(len(rest)-k) {
			return out, errors.New("invalid Wasm section")
		}
		section := rest[k : k+int(n)]
		rest = rest[k+int(n):]
		if id != 0 {
			continue
		}
		size, k := binary.Uvarint(section)
		if k <= 0 || k > 5 || size > uint64(len(section)-k) {
			return out, errors.New("invalid custom section")
		}
		if string(section[k:k+int(size)]) != "sourcegraph.plugin.v1" {
			continue
		}
		data := section[k+int(size):]
		if len(data) > limit {
			return out, errors.New("plugin metadata exceeds configured size")
		}
		if e := json.Unmarshal(data, &out); e != nil {
			return out, errors.New("invalid plugin metadata JSON")
		}
	}
	if out.ABIVersion != 2 {
		return out, errors.New("unsupported plugin ABI version: rebuild with abi_version 2 and attachments output")
	}
	return out, nil
}
