// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// staging builds a staging directory like modules_install's: the files given, relative to
// lib/modules/5.10, each holding its own path, and the links `build` and `source` to directories
// that hold .ko files too.
func staging(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "staging/lib/modules/5.10")
	for _, f := range files {
		p := filepath.Join(dir, f)
		os.MkdirAll(filepath.Dir(p), 0777)
		if err := os.WriteFile(p, []byte(f), 0666); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(root, "obj/drivers"), 0777)
	os.WriteFile(filepath.Join(root, "obj/drivers/linked.ko"), []byte("linked"), 0666)
	os.MkdirAll(dir, 0777)
	os.Symlink(filepath.Join(root, "obj"), filepath.Join(dir, "build"))
	os.Symlink(filepath.Join(root, "obj"), filepath.Join(dir, "source"))
	return filepath.Join(root, "staging")
}

// outputs returns the files of the output directory with their content.
func outputs(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := make(map[string]string)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		got[e.Name()] = string(data)
	}
	return got
}

func TestCollectModules(t *testing.T) {
	st := staging(t, "kernel/drivers/a.ko", "kernel/drivers/b.ko", "extra/c.ko", "modules.order")
	out := filepath.Join(filepath.Dir(st), "flat")
	os.MkdirAll(out, 0777)
	os.WriteFile(filepath.Join(out, "stale.ko"), []byte("stale"), 0666)

	if err := collectModules(st, out, nil, "module_renames"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a.ko": "kernel/drivers/a.ko", "b.ko": "kernel/drivers/b.ko", "c.ko": "extra/c.ko"}
	if got := outputs(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("without renames: got %v, want %v", got, want)
	}

	if err := collectModules(st, out, []string{"a.ko:x.ko"}, "module_renames"); err != nil {
		t.Fatal(err)
	}
	want = map[string]string{"x.ko": "kernel/drivers/a.ko", "b.ko": "kernel/drivers/b.ko", "c.ko": "extra/c.ko"}
	if got := outputs(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("a rename: got %v, want %v", got, want)
	}
}

func TestEmptyStaging(t *testing.T) {
	st := staging(t)
	out := filepath.Join(filepath.Dir(st), "flat")
	if err := collectModules(st, out, nil, "module_renames"); err != nil {
		t.Fatal(err)
	}
	if got := outputs(t, out); len(got) != 0 {
		t.Errorf("got %v, want an empty directory", got)
	}
}

func TestFinalNames(t *testing.T) {
	modules := []string{"/s/kernel/a.ko", "/s/kernel/b.ko", "/s/extra/c.ko"}
	check := func(renames []string, want map[string]string) {
		t.Helper()
		got, err := finalNames(modules, renames, "module_renames")
		if err != nil {
			t.Fatalf("%q: %v", renames, err)
		}
		byName := make(map[string]string)
		for m, n := range got {
			byName[filepath.Base(m)] = n
		}
		if !reflect.DeepEqual(byName, want) {
			t.Errorf("%q: got %v, want %v", renames, byName, want)
		}
	}
	// A chain: the collected a.ko becomes b.ko and the collected b.ko becomes c.ko... which the
	// collected c.ko then clashes with, so c.ko moves on too.
	check([]string{"a.ko:b.ko", "b.ko:c.ko", "c.ko:d.ko"}, map[string]string{"a.ko": "b.ko", "b.ko": "c.ko", "c.ko": "d.ko"})
	check([]string{"c.ko:d.ko", "b.ko:c.ko", "a.ko:b.ko"}, map[string]string{"a.ko": "b.ko", "b.ko": "c.ko", "c.ko": "d.ko"})
	// A swap, in both orders.
	check([]string{"a.ko:b.ko", "b.ko:a.ko"}, map[string]string{"a.ko": "b.ko", "b.ko": "a.ko", "c.ko": "c.ko"})
	check([]string{"b.ko:a.ko", "a.ko:b.ko"}, map[string]string{"a.ko": "b.ko", "b.ko": "a.ko", "c.ko": "c.ko"})

	fails := func(modules, renames []string, text string) {
		t.Helper()
		_, err := finalNames(modules, renames, "MODULE_ALIASES")
		if err == nil || !strings.Contains(err.Error(), text) {
			t.Errorf("%q: got %v, want an error with %q", renames, err, text)
		}
	}
	// Each message refers to the renames by the name it is handed and ends with the next step.
	fails(modules, []string{"none.ko:x.ko"}, `MODULE_ALIASES contains "none.ko:x.ko", which matches no module. `+
		"Correct the first name to the base name of a module that the kernel builds, or remove the entry from MODULE_ALIASES.")
	fails(append(modules, "/s/extra/a.ko"), []string{"a.ko:x.ko"}, `MODULE_ALIASES contains "a.ko:x.ko", which matches 2 modules: `+
		"/s/kernel/a.ko /s/extra/a.ko. Give the modules different names in their kbuild files, or remove the entry from MODULE_ALIASES.")
	fails(append(modules, "/s/extra/a.ko"), nil, "a.ko: /s/extra/a.ko /s/kernel/a.ko\n"+
		"Give the modules different names in their kbuild files, or build only one of them.")
	fails(modules, []string{"a.ko:c.ko"}, "c.ko: /s/extra/c.ko /s/kernel/a.ko")
	fails(modules, []string{"a.ko"}, `MODULE_ALIASES contains "a.ko", which is not <name>.ko:<name>.ko. Write the entry as <from>.ko:<to>.ko.`)
	fails(modules, []string{"a.ko:b.ko:c.ko"}, `"a.ko:b.ko:c.ko", which is not <name>.ko:<name>.ko`)
	fails(modules, []string{"a.ko:b.ko", "a.ko:c.ko"}, "MODULE_ALIASES renames a.ko twice. Remove one of the two entries from MODULE_ALIASES.")
}

func TestListDeviceTrees(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"vendor/qcom/b.dtb", "vendor/qcom/a-overlay.dtbo", "vendor/B.dtb", "a.dtb", "x.dts"} {
		p := filepath.Join(dir, f)
		os.MkdirAll(filepath.Dir(p), 0777)
		os.WriteFile(p, []byte(f), 0666)
	}
	list := filepath.Join(t.TempDir(), "list")
	if err := listDeviceTrees(dir, ".dtb", list, "Enable the device trees."); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(list)
	want := []string{dir + "/a.dtb", dir + "/vendor/B.dtb", dir + "/vendor/qcom/b.dtb"}
	if !sort.StringsAreSorted(want) || string(data) != strings.Join(want, "\n")+"\n" {
		t.Errorf("got %q, want %q", data, want)
	}

	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, "a.dtb"), nil, 0666)
	err := listDeviceTrees(empty, ".dtbo", list, "Enable the device trees.")
	if err == nil || err.Error() != "the kernel build made no *.dtbo file below "+empty+". Enable the device trees." {
		t.Errorf("got %v", err)
	}
}
