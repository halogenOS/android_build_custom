// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// tree creates files, directories and symbolic links below a scratch directory. A value that
// starts with "->" makes a link to the rest, "dir" a directory, anything else a file.
func tree(t *testing.T, entries map[string]string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sortedKeys(entries) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0777); err != nil {
			t.Fatal(err)
		}
		v := entries[name]
		switch {
		case v == "dir":
			err = os.MkdirAll(p, 0777)
		case strings.HasPrefix(v, "->"):
			err = os.Symlink(strings.TrimPrefix(v, "->"), p)
		default:
			err = os.WriteFile(p, []byte(v), 0666)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func sortedKeys(m map[string]string) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	// Directories before the files and links below them.
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func mustAllow(t *testing.T, paths ...string) allowList {
	t.Helper()
	l, err := newAllowList(paths)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestParseMakeOutput(t *testing.T) {
	out := "noise from make\n" +
		"KBUILD_INPUT_CHECK|/o/kernel/.fork.o.cmd|source_kernel/fork.o|/k/kernel/fork.c\n" +
		"KBUILD_INPUT_CHECK|/o/kernel/.fork.o.cmd|deps_kernel/fork.o|/k/include/linux/a.h   include/config/x \n" +
		"KBUILD_INPUT_CHECK|/o/kernel/.fork.o.cmd|deps_kernel/fork.o.empty|\n" +
		"KBUILD_INPUT_CHECK|/o/include/config/auto.conf.cmd|deps_config|init/Kconfig Kconfig\n"
	got, err := parseMakeOutput([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []cmdValue{
		{"/o/kernel/.fork.o.cmd", "source_kernel/fork.o", []string{"/k/kernel/fork.c"}},
		{"/o/kernel/.fork.o.cmd", "deps_kernel/fork.o", []string{"/k/include/linux/a.h", "include/config/x"}},
		{"/o/kernel/.fork.o.cmd", "deps_kernel/fork.o.empty", []string{}},
		{"/o/include/config/auto.conf.cmd", "deps_config", []string{"init/Kconfig", "Kconfig"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCheckValues(t *testing.T) {
	root := tree(t, map[string]string{
		"k/kernel/fork.c":                "c",
		"k/include/linux/a.h":            "h",
		"k/drivers/to_sibling":           "->../../sibling",
		"k/drivers/to_extra":             "->../../extra",
		"sibling/secret.h":               "s",
		"extra/dt.dtsi":                  "d",
		"clang/lib/include/stddef.h":     "x",
		"o/include/config/auto.conf":     "c",
		"o/include/generated/autoconf.h": "g",
	})
	k, o, clang := root+"/k", root+"/o", root+"/clang"
	allowed := mustAllow(t, k, root+"/extra", o, clang)
	toolchains := mustAllow(t, clang)

	// A deps_ entry that $(wildcard ...) expanded to a path is a word of the value, and one that it
	// expanded to nothing leaves no word.
	values := []cmdValue{
		{o + "/kernel/.fork.o.cmd", "source_kernel/fork.o", []string{k + "/kernel/fork.c"}},
		{o + "/kernel/.fork.o.cmd", "deps_kernel/fork.o", []string{
			k + "/include/linux/../linux/a.h",        // `..` cleaned
			"include/generated/autoconf.h",           // relative to the object directory
			k + "/drivers/to_extra/dt.dtsi",          // a link into a listed directory
			k + "/drivers/to_sibling/secret.h",       // a link into an unlisted sibling
			clang + "/lib/include/stddef.h",          // a toolchain read that exists
			clang + "/lib/include/gone.h",            // a toolchain read that no longer exists
			k + "/drivers/to_sibling/gone/removed.h", // no longer exists: its leading part is resolved
		}},
		{o + "/include/config/auto.conf.cmd", "deps_config", []string{root + "/sibling/Kconfig"}},
	}
	bases := readBases{objDir: o, autoConf: o + "/include/config/auto.conf.cmd", srcTree: k}
	bad, reads := checkValues(values, bases, root, allowed, toolchains)
	wantBad := []outside{
		{root + "/sibling/secret.h", o + "/kernel/.fork.o.cmd"},
		{root + "/sibling/gone/removed.h", o + "/kernel/.fork.o.cmd"},
		{root + "/sibling/Kconfig", o + "/include/config/auto.conf.cmd"},
	}
	if !reflect.DeepEqual(bad, wantBad) {
		t.Errorf("reads outside: got %q, want %q", bad, wantBad)
	}
	if want := []string{"clang/lib/include/stddef.h"}; !reflect.DeepEqual(reads, want) {
		t.Errorf("toolchain reads: got %q, want %q", reads, want)
	}

	// Without the reads outside, nothing is reported.
	bad, _ = checkValues(values[:1], bases, root, allowed, toolchains)
	if len(bad) != 0 {
		t.Errorf("got reads outside %q, want none", bad)
	}

	// Kconfig records its files relative to the kernel source directory. Made absolute against
	// the object directory, which is allowed, the one that leaves kernel_dir through a link would
	// pass.
	kconfig := []cmdValue{{o + "/include/config/auto.conf.cmd", "deps_config", []string{
		"init/Kconfig",
		"drivers/to_extra/Kconfig",
		"drivers/to_sibling/Kconfig",
	}}}
	bad, _ = checkValues(kconfig, bases, root, allowed, toolchains)
	if want := []outside{{root + "/sibling/Kconfig", o + "/include/config/auto.conf.cmd"}}; !reflect.DeepEqual(bad, want) {
		t.Errorf("Kconfig reads outside: got %q, want %q", bad, want)
	}
	// A deps_config value of another .cmd file keeps the object directory as its base.
	other := []cmdValue{{o + "/kernel/.fork.o.cmd", "deps_config", []string{"include/generated/autoconf.h"}}}
	if bad, _ = checkValues(other, bases, root, allowed, toolchains); len(bad) != 0 {
		t.Errorf("a value of another .cmd file was not read against the object directory: %q", bad)
	}
}

// TestReadsOutsideError checks that the failure lists each read with its .cmd file and then gives
// the next step it is handed.
func TestReadsOutsideError(t *testing.T) {
	msg := readsOutsideError([]outside{
		{"/t/kernel/dts/qcom/a.dtsi", "/o/arch/arm64/boot/dts/.a.dtb.cmd"},
		{"/t/kernel/dts/qcom/b.dtsi", "/o/arch/arm64/boot/dts/.b.dtb.cmd"},
	}, "Add the directory to the list.").Error()
	at := 0
	for _, part := range []string{
		"  /t/kernel/dts/qcom/a.dtsi (recorded in /o/arch/arm64/boot/dts/.a.dtb.cmd)\n",
		"  /t/kernel/dts/qcom/b.dtsi (recorded in /o/arch/arm64/boot/dts/.b.dtb.cmd)\n",
		"Add the directory to the list.\n",
	} {
		i := strings.Index(msg[at:], part)
		if i < 0 {
			t.Fatalf("%q does not follow position %d of %q", part, at, msg)
		}
		at += i + len(part)
	}
}

func TestDepfileEntries(t *testing.T) {
	root := tree(t, map[string]string{
		"k/Makefile":          "m",
		"k/drivers/a.c":       "a",
		"k/drivers/ksu":       "->../KernelSU/kernel",
		"k/drivers/to_ksu":    "->ksu",
		"k/arch/vendor":       "->../../dts",
		"dts/x.dts":           "x",
		"k/include/linux/b.h": "b",
	})
	k := root + "/k"
	got, err := depfileEntries([]string{k + "/drivers", k + "/arch", k + "/Makefile"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{k + "/drivers", k + "/drivers/a.c", k + "/arch", k + "/arch/vendor", k + "/Makefile"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// An entry that does not exist fails the mode, which then writes no output.
	out := root + "/out.d"
	err = runDepfile([]string{"-o", out, "-t", "target", k + "/missing"})
	if err == nil {
		t.Errorf("an entry that does not exist did not fail")
	}
	if fileExists(out) {
		t.Errorf("the failed mode wrote %s", out)
	}
	if err := runDepfile([]string{"-o", out, "-t", "target", k + "/drivers"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if !strings.HasPrefix(string(data), "target:") || !strings.Contains(string(data), k+"/drivers/a.c") ||
		strings.Contains(string(data), "ksu") {
		t.Errorf("unexpected depfile:\n%s", data)
	}
}

func TestLinks(t *testing.T) {
	root := tree(t, map[string]string{
		"k/arch/vendor":  "->../../dts",
		"k/drivers/ksu":  "->../KernelSU/kernel",
		"k/drivers/self": "->../arch",
		"dts/x.dts":      "x",
		"outside/y.h":    "y",
	})
	k := root + "/k"
	args := []string{"-next-step", "Add the directory.", "-allow", k, "-allow", root + "/dts", k}
	if err := runLinks(args); err != nil {
		t.Errorf("links inside the allowed directories failed: %v", err)
	}
	if err := os.Symlink("../../outside", k+"/drivers/out"); err != nil {
		t.Fatal(err)
	}
	// The failure lists the link and then gives the next step it is handed.
	err := runLinks(args)
	if err == nil || !strings.Contains(err.Error(), k+"/drivers/out -> ../../outside (resolves to "+root+"/outside)\nAdd the directory.") {
		t.Errorf("a link outside the allowed directories: got %v", err)
	}
	if err := runLinks(args[2:]); err == nil || err.Error() != "-next-step is required" {
		t.Errorf("links without a next step: got %v", err)
	}
}

func TestCheckHeaderValues(t *testing.T) {
	root := tree(t, map[string]string{
		"k/include/uapi/linux/a.h":          "a",
		"outside/b.h":                       "b",
		"staging/usr/include/generated/c.h": "c",
	})
	k, staging := root+"/k", root+"/staging"
	allowed := mustAllow(t, k, staging)
	script := k + "/scripts/headers_install.sh"
	values := []cmdValue{
		{"a.cmd", "cmd_usr/include/linux/a.h", []string{"sh", script, k + "/include/uapi/linux/a.h", "usr/include/linux/a.h"}},
		{"c.cmd", "cmd_usr/include/c.h", []string{"sh", script, "usr/include/generated/c.h", "usr/include/c.h"}},
	}
	if err := headerError(checkHeaderValues(values, staging, allowed), "Add the directory."); err != nil {
		t.Errorf("got %v, want no problem", err)
	}
	values = append(values,
		cmdValue{"b.cmd", "cmd_usr/include/b.h", []string{"sh", script, k + "/../outside/b.h", "usr/include/b.h"}},
		cmdValue{"d.cmd", "cmd_usr/include/d.h", []string{"cp", "x", "y"}})
	p := checkHeaderValues(values, staging, allowed)
	want := headerProblems{
		unknownForm: []string{`  d.cmd: "cp x y"`},
		outside:     []string{"  usr/include/b.h comes from " + root + "/outside/b.h"},
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("got %q, want %q", p, want)
	}
	// Each kind of problem is followed by its next step, the one handed in for headers from outside.
	msg := headerError(p, "Add the directory.").Error()
	for _, part := range []string{
		`  d.cmd: "cp x y"` + "\nUse a kernel source whose headers_install installs each header with scripts/headers_install.sh.",
		"  usr/include/b.h comes from " + root + "/outside/b.h\nAdd the directory.",
	} {
		if !strings.Contains(msg, part) {
			t.Errorf("%q lacks %q", msg, part)
		}
	}
}

func identityLines(t *testing.T, cache string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := runIdentity(append([]string{"-cache", cache}, args...), &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestIdentity(t *testing.T) {
	root := tree(t, map[string]string{
		"clang/bin/clang":                    "clang",
		"clang/bin/ld.lld":                   "->lld",
		"clang/bin/lld":                      "lld",
		"clang/lib/python3/x.pyc":            "cache",
		"clang/lib/python3/__pycache__/y.py": "cache",
		"tools/make":                         "make",
		"path/sed":                           "->../tools/make",
	})
	cache := root + "/cache"
	got := identityLines(t, cache, "-link-targets", root+"/path", root+"/clang", root+"/tools/make")
	h := func(s string) string { d, _ := hashFileBytes([]byte(s)); return d }
	want := h("clang") + "  " + root + "/clang/bin/clang\n" +
		root + "/clang/bin/ld.lld -> lld\n" +
		h("lld") + "  " + root + "/clang/bin/lld\n" +
		h("make") + "  " + root + "/tools/make\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	// The cache is a JSON object keyed by path, and it holds no entry for a link.
	var entries map[string]cacheEntry
	if data, err := os.ReadFile(cache); err != nil || json.Unmarshal(data, &entries) != nil ||
		entries[root+"/clang/bin/clang"].Sha256 != h("clang") || len(entries) != 3 {
		t.Errorf("the cache does not hold the hashes of the three files: %v", entries)
	}

	// A file rewritten with its modification time set back gets a new line.
	p := root + "/clang/bin/lld"
	info, _ := os.Stat(p)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(p, []byte("LLD"), 0666); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, info.ModTime(), info.ModTime())
	if got := identityLines(t, cache, root+"/clang"); !strings.Contains(got, h("LLD")+"  "+p) {
		t.Errorf("a rewritten file kept its old line:\n%s", got)
	}

	// A cache entry whose stat values still match is used without reading the file.
	data, _ := os.ReadFile(cache)
	forged := strings.Replace(string(data), h("clang"), strings.Repeat("0", 64), 1)
	os.WriteFile(cache, []byte(forged), 0666)
	if got := identityLines(t, cache, root+"/clang"); !strings.Contains(got, strings.Repeat("0", 64)+"  "+root+"/clang/bin/clang") {
		t.Errorf("the matching cache entry was not used:\n%s", got)
	}

	// A cache that cannot be read gives the same lines as no cache.
	os.WriteFile(cache, []byte("damaged"), 0666)
	withDamaged := identityLines(t, cache, root+"/clang")
	os.Remove(cache)
	if withNone := identityLines(t, cache, root+"/clang"); withDamaged != withNone {
		t.Errorf("a damaged cache changed the lines:\n%s\n%s", withDamaged, withNone)
	}
}

func hashFileBytes(b []byte) (string, error) {
	f, err := os.CreateTemp("", "hash")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	f.Write(b)
	f.Close()
	return hashFile(f.Name())
}
