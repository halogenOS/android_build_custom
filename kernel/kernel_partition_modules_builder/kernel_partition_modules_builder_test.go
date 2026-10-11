// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeList(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "list")
	if err := os.WriteFile(p, []byte(content), 0666); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadAndMatchList(t *testing.T) {
	modules := []string{"a.ko", "b.ko", "c.ko", "d.ko"}
	// Entries with and without .ko, blanks around a name, a line of blanks, a carriage return at
	// a line's end, a line with two names and an entry with a directory part.
	list, err := readList(writeList(t, "a.ko\n  b  \n   \nc.ko\r\nd x/e.ko\n"))
	if err != nil {
		t.Fatal(err)
	}
	entries := list.entries
	if want := []string{"a.ko", "b", "c.ko", "d", "x/e.ko"}; !reflect.DeepEqual(entries, want) {
		t.Errorf("entries: got %q, want %q", entries, want)
	}
	_, err = matchList("load_list", entries, modules)
	if err == nil || !strings.Contains(err.Error(), "load_list contains x/e.ko, which the kernel does not build. "+
		"Enable the module in the kernel configuration, or remove x/e.ko from load_list.") {
		t.Errorf("an entry that names no module: got %v", err)
	}
	matched, err := matchList("load_list", entries[:4], modules)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.ko", "b.ko", "c.ko", "d.ko"}; !reflect.DeepEqual(matched, want) {
		t.Errorf("matched: got %q, want %q", matched, want)
	}
	// An entry that appears twice, with and without .ko.
	_, err = matchList("recovery_load_list", []string{"a.ko", "b", "a"}, modules)
	if err == nil || !strings.Contains(err.Error(), "recovery_load_list contains a twice. Remove the second a from recovery_load_list.") {
		t.Errorf("a repeated entry: got %v", err)
	}
}

func TestInstalledSet(t *testing.T) {
	modules := []string{"a.ko", "b.ko", "c.ko"}
	// A module in both lists is installed once.
	set, err := installedSet(modules, false, false, "z.zip", []string{"b.ko", "a.ko"}, []string{"a.ko", "c.ko"})
	if want := []string{"b.ko", "a.ko", "c.ko"}; err != nil || !reflect.DeepEqual(set, want) {
		t.Errorf("union: got %q, %v, want %q", set, err, want)
	}
	if set, err := installedSet(modules, true, false, "z.zip", []string{"b.ko"}); err != nil || !reflect.DeepEqual(set, modules) {
		t.Errorf("install_all: got %q, %v", set, err)
	}
	// A kernel that builds no modules fails install_all, unless an empty set is allowed.
	_, err = installedSet(nil, true, false, "z.zip")
	if err == nil || !strings.Contains(err.Error(), "zip.install_all is set, and the zip z.zip contains no .ko file. "+
		"Enable a module in the kernel configuration, or set zip.allow_empty: true for a kernel without modules.") {
		t.Errorf("install_all without modules: got %v", err)
	}
	if set, err := installedSet(nil, true, true, "z.zip"); err != nil || len(set) != 0 {
		t.Errorf("install_all with allow_empty without modules: got %q, %v", set, err)
	}
}

// TestSelectListNames checks that select fails for a list given without the name that its messages
// use for it.
func TestSelectListNames(t *testing.T) {
	dir := t.TempDir()
	zipFile := filepath.Join(dir, "modules.zip")
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	if _, err := w.Create("a.ko"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zipFile, buf.Bytes(), 0666); err != nil {
		t.Fatal(err)
	}
	err := runSelect([]string{"-zip", zipFile, "-system-map", "System.map", "-depmod", "depmod", "-temp", dir,
		"-o", filepath.Join(dir, "selected.zip"), "-recovery-load-list", writeList(t, "a.ko")})
	if err == nil || err.Error() != "-recovery-load-list-name is required with -recovery-load-list" {
		t.Errorf("a recovery load list without its name: got %v", err)
	}
}

func makeZip(t *testing.T, files map[string]string) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	var names []string
	for n := range files {
		names = append(names, n)
	}
	for _, n := range sortedStrings(names) {
		f, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(files[n]))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func sortedStrings(s []string) []string {
	out := append([]string{}, s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func zipContent(t *testing.T, r *zip.Reader) map[string]string {
	t.Helper()
	got := make(map[string]string)
	for _, f := range r.File {
		in, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(in)
		in.Close()
		got[f.Name] = string(data)
	}
	return got
}

func readZip(t *testing.T, data []byte) *zip.Reader {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestZipModulesAndSelected(t *testing.T) {
	r := makeZip(t, map[string]string{"a.ko": "A", "b.ko": "B", "c.ko": "C", "notes.txt": "n"})
	if got := zipModules(r); !reflect.DeepEqual(got, []string{"a.ko", "b.ko", "c.ko"}) {
		t.Errorf("modules: %q", got)
	}
	out := filepath.Join(t.TempDir(), "selected.zip")
	if err := writeSelected(r, []string{"c.ko", "a.ko"}, out); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	want := map[string]string{"c.ko": "C", "a.ko": "A", "modules.install": "c.ko\na.ko\n"}
	if got := zipContent(t, readZip(t, data)); !reflect.DeepEqual(got, want) {
		t.Errorf("selected zip: got %q, want %q", got, want)
	}
}

func TestLoadFileContent(t *testing.T) {
	got := string(loadFileContent([]string{"a.ko", "dir/b.ko", "c"}))
	if want := "a.ko\nb.ko\nc\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFinishInstalls(t *testing.T) {
	installs := map[string]string{"a.ko": "A", "modules.dep": "/lib/modules/a.ko:\n", "modules.alias": "", "modules.blocklist": "blocklist a\n"}

	// With a load list: modules.load is the primary file, modules.dep stays in the zip, and
	// modules.load.recovery joins it.
	zipData, primary, err := finishInstalls(makeZip(t, installs), &givenList{[]string{"x/a.ko"}}, &givenList{[]string{"a.ko", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(primary) != "a.ko\n" {
		t.Errorf("modules.load: %q", primary)
	}
	got := zipContent(t, readZip(t, zipData))
	want := map[string]string{}
	for k, v := range installs {
		want[k] = v
	}
	want["modules.load.recovery"] = "a.ko\nb\n"
	if !reflect.DeepEqual(got, want) {
		t.Errorf("zip with a load list: got %q, want %q", got, want)
	}
	if _, ok := got["modules.load"]; ok {
		t.Errorf("modules.load is in the zip")
	}

	// Without a load list: modules.dep is the primary file and leaves the zip, and no modules.load
	// is written.
	zipData, primary, err = finishInstalls(makeZip(t, installs), nil, &givenList{[]string{"a.ko"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(primary) != installs["modules.dep"] {
		t.Errorf("primary without a load list: %q", primary)
	}
	got = zipContent(t, readZip(t, zipData))
	if _, ok := got["modules.dep"]; ok {
		t.Errorf("modules.dep stayed in the zip without a load list")
	}
	if _, ok := got["modules.load"]; ok {
		t.Errorf("modules.load written without a load list")
	}
	if got["modules.load.recovery"] != "a.ko\n" || got["a.ko"] != "A" {
		t.Errorf("zip without a load list: %q", got)
	}

	// An empty load list is a given list, whether its entries are nil or empty: modules.load is the
	// primary file, empty, and no modules.load.recovery is written for the absent recovery list.
	for _, empty := range []*givenList{{}, {entries: []string{}}} {
		zipData, primary, err = finishInstalls(makeZip(t, installs), empty, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(primary) != 0 {
			t.Errorf("primary of an empty load list: %q", primary)
		}
		got = zipContent(t, readZip(t, zipData))
		if _, ok := got["modules.load.recovery"]; ok {
			t.Errorf("modules.load.recovery written without a recovery list")
		}
		if got["modules.dep"] != installs["modules.dep"] {
			t.Errorf("modules.dep left the zip with an empty load list: %q", got)
		}
	}

	// Without a load list, an install zip without modules.dep fails with the next step.
	_, _, err = finishInstalls(makeZip(t, map[string]string{"a.ko": "A"}), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "contains no modules.dep, which the builder writes for every partition. "+
		"Check that kernel_modules_builder of build/soong is unchanged.") {
		t.Errorf("an install zip without modules.dep: got %v", err)
	}

	// readList gives a given list without entries for a file without a word.
	if list, err := readList(writeList(t, " \n")); err != nil || list == nil || len(list.entries) != 0 {
		t.Errorf("readList of a file without a word: %v, %v", list, err)
	}
}

func TestUnknownSymbols(t *testing.T) {
	if !unknownSymbols([]byte("depmod: WARNING: /x/lib/modules/0.0/a.ko needs unknown symbol foo\n")) {
		t.Errorf("an unknown symbol was not found")
	}
	if unknownSymbols([]byte("depmod: WARNING: could not open modules.order\n")) {
		t.Errorf("another warning counted as an unknown symbol")
	}
}
