package gitstore

import (
	"strings"
	"testing"
)

func TestPluginPathsIgnoreUnrelatedUnsupportedNames(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, plugin := range []bool{false, true} {
		data := []byte("100644 blob " + oid + "\tfixture/back\\slash.txt\x00" + "100644 blob " + oid + "\tfixture/bad\xff.txt\x00")
		if plugin {
			data = append(data, []byte("100644 blob "+oid+"\tnested/good.sourcegraph.wasm\x00")...)
		}
		got, err := pluginPathsFromTree(data, "", ".sourcegraph.wasm", func(string, bool) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		if plugin && (len(got) != 1 || got[0] != "nested/good.sourcegraph.wasm") {
			t.Fatal(got)
		}
		if !plugin && len(got) != 0 {
			t.Fatal(got)
		}
	}
}
func TestPluginPathsStillValidateCandidatesAndScope(t *testing.T) {
	oid := strings.Repeat("a", 40)
	invalid := []byte("100644 blob " + oid + "\tbad\\plugin.sourcegraph.wasm\x00")
	if _, err := pluginPathsFromTree(invalid, "", ".sourcegraph.wasm", func(string, bool) bool { return true }); err == nil {
		t.Fatal("invalid candidate accepted")
	}
	data := []byte("100644 blob " + oid + "\tinside/a.sourcegraph.wasm\x00" + "100644 blob " + oid + "\toutside/a.sourcegraph.wasm\x00" + "120000 blob " + oid + "\tinside/link.sourcegraph.wasm\x00")
	got, err := pluginPathsFromTree(data, "inside", ".sourcegraph.wasm", func(string, bool) bool { return true })
	if err != nil || len(got) != 1 || got[0] != "inside/a.sourcegraph.wasm" {
		t.Fatalf("%v %v", got, err)
	}
	got, err = pluginPathsFromTree(data, "inside", ".sourcegraph.wasm", func(string, bool) bool { return false })
	if err != nil || len(got) != 0 {
		t.Fatalf("scope widened: %v %v", got, err)
	}
}
