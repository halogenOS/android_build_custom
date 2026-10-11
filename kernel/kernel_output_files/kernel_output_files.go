// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

// kernel_output_files collects the outputs of a kernel build.
//
// Modes:
//
//	modules  collects every kernel module below the staging directory of modules_install, applies
//	         the renames and copies the modules, each under its final base name, into one
//	         directory.
//	dt       lists the device tree files with a suffix below a directory, in byte order of their
//	         paths.
package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, " ") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kernel_output_files modules|dt [flags]")
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "modules":
		flags := flag.NewFlagSet("modules", flag.ExitOnError)
		staging := flags.String("staging", "", "the staging directory of modules_install")
		out := flags.String("out", "", "the directory the modules are copied into, deleted and created again")
		renamesName := flags.String("renames-name", "", "the name of the renames in messages")
		var renames multiFlag
		flags.Var(&renames, "rename", "a rename from.ko:to.ko (repeated)")
		flags.Parse(os.Args[2:])
		err = collectModules(*staging, *out, renames, *renamesName)
	case "dt":
		flags := flag.NewFlagSet("dt", flag.ExitOnError)
		dir := flags.String("dir", "", "the directory the files lie below")
		suffix := flags.String("suffix", "", "the suffix of the files, .dtb or .dtbo")
		out := flags.String("o", "", "the list file to write")
		nextStep := flags.String("next-step", "", "what to do about a directory without such a file, which the failure message gives")
		flags.Parse(os.Args[2:])
		err = listDeviceTrees(*dir, *suffix, *out, *nextStep)
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "kernel_output_files %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

// findModules returns every regular file named *.ko below dir, in byte order. It follows no
// symbolic link: modules_install links `build` to the object directory and `source` to the kernel
// source, and following them would collect modules twice or reach into the source.
func findModules(dir string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".ko") {
			found = append(found, path)
		}
		return nil
	})
	sort.Strings(found)
	return found, err
}

// finalNames gives each collected module its final base name. Every rename selects its file by the
// base names as collected, before any rename is made, so the order of the renames does not change
// the result. A rename that selects no file or more than one fails, and so do two files that end up
// with the same base name. renamesName is the name that the messages use for the renames.
func finalNames(modules []string, renames []string, renamesName string) (map[string]string, error) {
	byName := make(map[string][]string)
	for _, m := range modules {
		byName[filepath.Base(m)] = append(byName[filepath.Base(m)], m)
	}
	names := make(map[string]string)
	for _, m := range modules {
		names[m] = filepath.Base(m)
	}
	seenFrom := make(map[string]bool)
	for _, r := range renames {
		from, to, ok := strings.Cut(r, ":")
		if !ok || !strings.HasSuffix(from, ".ko") || !strings.HasSuffix(to, ".ko") ||
			strings.ContainsAny(from, "/:") || strings.ContainsAny(to, "/:") {
			return nil, fmt.Errorf("%s contains %q, which is not <name>.ko:<name>.ko. Write the entry as <from>.ko:<to>.ko.", renamesName, r)
		}
		if seenFrom[from] {
			return nil, fmt.Errorf("%s renames %s twice. Remove one of the two entries from %s.", renamesName, from, renamesName)
		}
		seenFrom[from] = true
		switch matches := byName[from]; len(matches) {
		case 0:
			return nil, fmt.Errorf("%s contains %q, which matches no module. Correct the first name to the base name of a module "+
				"that the kernel builds, or remove the entry from %s.", renamesName, r, renamesName)
		case 1:
			names[matches[0]] = to
		default:
			return nil, fmt.Errorf("%s contains %q, which matches %d modules: %s. Give the modules different names in their "+
				"kbuild files, or remove the entry from %s.", renamesName, r, len(matches), strings.Join(matches, " "), renamesName)
		}
	}
	owners := make(map[string][]string)
	for _, m := range modules {
		owners[names[m]] = append(owners[names[m]], m)
	}
	var clashes []string
	for name, files := range owners {
		if len(files) > 1 {
			sort.Strings(files)
			clashes = append(clashes, fmt.Sprintf("  %s: %s", name, strings.Join(files, " ")))
		}
	}
	if len(clashes) > 0 {
		sort.Strings(clashes)
		return nil, fmt.Errorf("modules share a base name:\n%s\nGive the modules different names in their kbuild files, "+
			"or build only one of them.", strings.Join(clashes, "\n"))
	}
	return names, nil
}

func collectModules(staging, out string, renames []string, renamesName string) error {
	if staging == "" || out == "" || renamesName == "" {
		return fmt.Errorf("-staging, -out and -renames-name are required")
	}
	modules, err := findModules(staging)
	if err != nil {
		return err
	}
	names, err := finalNames(modules, renames, renamesName)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0777); err != nil {
		return err
	}
	for _, m := range modules {
		if err := copyFile(m, filepath.Join(out, names[m])); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	o, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(o, in); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}

// listDeviceTrees writes the paths of the files with the suffix below dir to out, one per line, in
// byte order of their paths. A directory without such a file fails with nextStep.
func listDeviceTrees(dir, suffix, out, nextStep string) error {
	if dir == "" || suffix == "" || out == "" || nextStep == "" {
		return fmt.Errorf("-dir, -suffix, -o and -next-step are required")
	}
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && strings.HasSuffix(d.Name(), suffix) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return fmt.Errorf("the kernel build made no *%s file below %s. %s", suffix, dir, nextStep)
	}
	sort.Strings(found)
	return os.WriteFile(out, []byte(strings.Join(found, "\n")+"\n"), 0666)
}
