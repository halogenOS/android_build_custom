// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

// kernel_partition_modules_builder runs the build-time steps of a kernel_partition_modules module
// around AOSP's kernel_modules_builder.
//
// Modes:
//
//	select  checks the load lists and the install list against the modules of the zip, chooses
//	        the installed set, checks it against the kernel's System.map with depmod and writes a
//	        zip of the installed modules with a member modules.install that lists them.
//	finish  writes modules.load and modules.load.recovery from their lists, or takes modules.dep
//	        out of the install zip as the primary file without a load list, and writes the final
//	        install zip.
package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kernel_partition_modules_builder select|finish [flags]")
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "select":
		err = runSelect(os.Args[2:])
	case "finish":
		err = runFinish(os.Args[2:])
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "kernel_partition_modules_builder %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

// moduleKey is the name an entry or a module is compared by: its base name without a trailing .ko,
// so `name` and `name.ko` name the same module.
func moduleKey(name string) string {
	return strings.TrimSuffix(filepath.Base(name), ".ko")
}

// givenList is a load list that was given, with its entries in order, none for a file without a
// word. A list that was not given is a nil *givenList, so an empty list is never taken for an
// absent one.
type givenList struct {
	entries []string
}

// readList reads a load list as the whitespace-separated words of the file, in order.
func readList(file string) (*givenList, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return &givenList{entries: strings.Fields(string(data))}, nil
}

// matchList checks every entry of a list against the modules: each entry must name one of them, and
// no entry may appear twice. It returns the modules the entries name, in list order. listName is
// the name that the messages use for the list, such as the setting that the list comes from.
func matchList(listName string, entries []string, modules []string) ([]string, error) {
	byKey := make(map[string]string)
	for _, m := range modules {
		byKey[moduleKey(m)] = m
	}
	var matched []string
	var problems []string
	seen := make(map[string]bool)
	for _, e := range entries {
		key := moduleKey(e)
		if seen[key] {
			problems = append(problems, fmt.Sprintf("%s contains %s twice. Remove the second %s from %s.", listName, e, e, listName))
			continue
		}
		seen[key] = true
		m, ok := byKey[key]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s contains %s, which the kernel does not build. "+
				"Enable the module in the kernel configuration, or remove %s from %s.", listName, e, e, listName))
			continue
		}
		matched = append(matched, m)
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "\n"))
	}
	return matched, nil
}

// installedSet chooses the modules to install: every module with installAll, which needs at least
// one unless allowEmpty is set, otherwise the union of the lists, each module once.
func installedSet(modules []string, installAll, allowEmpty bool, zipName string, lists ...[]string) ([]string, error) {
	if installAll {
		if len(modules) == 0 && !allowEmpty {
			return nil, fmt.Errorf("zip.install_all is set, and the zip %s contains no .ko file. "+
				"Enable a module in the kernel configuration, or set zip.allow_empty: true for a kernel without modules.", zipName)
		}
		return modules, nil
	}
	var set []string
	seen := make(map[string]bool)
	for _, list := range lists {
		for _, m := range list {
			if !seen[m] {
				seen[m] = true
				set = append(set, m)
			}
		}
	}
	return set, nil
}

// zipModules lists the .ko members of a zip, in the zip's order.
func zipModules(r *zip.Reader) []string {
	var modules []string
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, ".ko") && !strings.Contains(f.Name, "/") {
			modules = append(modules, f.Name)
		}
	}
	return modules
}

// unknownSymbols reports whether depmod's error output names a module that needs an unknown symbol.
func unknownSymbols(stderr []byte) bool {
	return bytes.Contains(stderr, []byte("needs unknown symbol"))
}

func runSelect(args []string) error {
	flags := flag.NewFlagSet("select", flag.ExitOnError)
	zipFile := flags.String("zip", "", "the zip of modules")
	loadList := flags.String("load-list", "", "the partition's modules.load")
	recoveryList := flags.String("recovery-load-list", "", "the partition's modules.load.recovery")
	installList := flags.String("install-list", "", "the modules to install besides those of the load lists")
	loadListName := flags.String("load-list-name", "", "the name of the load list in messages, required with -load-list")
	recoveryListName := flags.String("recovery-load-list-name", "",
		"the name of the recovery load list in messages, required with -recovery-load-list")
	installListName := flags.String("install-list-name", "", "the name of the install list in messages, required with -install-list")
	installAll := flags.Bool("install-all", false, "install every module of the zip")
	allowEmpty := flags.Bool("allow-empty", false, "with -install-all, install nothing for a zip without modules")
	systemMap := flags.String("system-map", "", "the kernel's System.map")
	depmod := flags.String("depmod", "", "the depmod program")
	temp := flags.String("temp", "", "a directory for the depmod check, deleted and created again")
	out := flags.String("o", "", "the zip of the installed modules to write")
	flags.Parse(args)
	if *zipFile == "" || *systemMap == "" || *depmod == "" || *temp == "" || *out == "" {
		return fmt.Errorf("-zip, -system-map, -depmod, -temp and -o are required")
	}

	r, err := zip.OpenReader(*zipFile)
	if err != nil {
		return err
	}
	defer r.Close()
	modules := zipModules(&r.Reader)

	var lists [][]string
	for _, l := range []struct{ flag, name, file string }{
		{"load-list", *loadListName, *loadList}, {"recovery-load-list", *recoveryListName, *recoveryList},
		{"install-list", *installListName, *installList},
	} {
		if l.file == "" {
			continue
		}
		if l.name == "" {
			return fmt.Errorf("-%s-name is required with -%s", l.flag, l.flag)
		}
		list, err := readList(l.file)
		if err != nil {
			return err
		}
		matched, err := matchList(l.name, list.entries, modules)
		if err != nil {
			return err
		}
		lists = append(lists, matched)
	}
	set, err := installedSet(modules, *installAll, *allowEmpty, *zipFile, lists...)
	if err != nil {
		return err
	}

	if err := checkSymbols(&r.Reader, set, *systemMap, *depmod, *temp); err != nil {
		return err
	}
	return writeSelected(&r.Reader, set, *out)
}

// checkSymbols runs depmod over the installed set against the kernel's System.map and fails when
// a module needs an unknown symbol.
func checkSymbols(r *zip.Reader, set []string, systemMap, depmod, temp string) error {
	if err := os.RemoveAll(temp); err != nil {
		return err
	}
	dir := filepath.Join(temp, "lib", "modules", "0.0")
	if err := os.MkdirAll(dir, 0777); err != nil {
		return err
	}
	want := make(map[string]bool)
	for _, m := range set {
		want[m] = true
	}
	for _, f := range r.File {
		if want[f.Name] {
			if err := extract(f, filepath.Join(dir, f.Name)); err != nil {
				return err
			}
		}
	}
	// depmod requires a well-formed kernel version, so 0.0 stands for it.
	cmd := exec.Command(depmod, "-ae", "-F", systemMap, "-b", temp, "0.0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	os.Stderr.Write(stderr.Bytes())
	if unknownSymbols(stderr.Bytes()) {
		return fmt.Errorf("kernel modules need symbols that the System.map %s of the kernel does not contain. "+
			"Enable the kernel options that export the symbols in depmod's output above, or remove the modules that need them.", systemMap)
	}
	if runErr != nil {
		return fmt.Errorf("depmod failed: %v. Correct the module that depmod reports in its output above.", runErr)
	}
	return nil
}

func extract(f *zip.File, to string) error {
	in, err := f.Open()
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

// writeSelected writes a zip with the modules of the set, copied from the zip, and a member
// modules.install that names them, one per line.
func writeSelected(r *zip.Reader, set []string, out string) error {
	byName := make(map[string]*zip.File)
	for _, f := range r.File {
		byName[f.Name] = f
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, m := range set {
		if err := w.Copy(byName[m]); err != nil {
			return err
		}
	}
	install, err := w.Create("modules.install")
	if err != nil {
		return err
	}
	for _, m := range set {
		if _, err := io.WriteString(install, m+"\n"); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0666)
}

// loadFileContent writes the entries of a list as a modules.load file: one line per entry holding
// its base name, each line ending in a newline.
func loadFileContent(entries []string) []byte {
	var b bytes.Buffer
	for _, e := range entries {
		b.WriteString(filepath.Base(e) + "\n")
	}
	return b.Bytes()
}

func runFinish(args []string) error {
	flags := flag.NewFlagSet("finish", flag.ExitOnError)
	installs := flags.String("installs", "", "the install zip of kernel_modules_builder")
	loadList := flags.String("load-list", "", "the partition's modules.load")
	recoveryList := flags.String("recovery-load-list", "", "the partition's modules.load.recovery")
	out := flags.String("o", "", "the final install zip to write")
	primary := flags.String("primary", "", "the primary file to write: modules.load, or modules.dep without a load list")
	flags.Parse(args)
	if *installs == "" || *out == "" || *primary == "" {
		return fmt.Errorf("-installs, -o and -primary are required")
	}
	// A list that is not given stays nil.
	var load, recovery *givenList
	var err error
	if *loadList != "" {
		if load, err = readList(*loadList); err != nil {
			return err
		}
	}
	if *recoveryList != "" {
		if recovery, err = readList(*recoveryList); err != nil {
			return err
		}
	}
	r, err := zip.OpenReader(*installs)
	if err != nil {
		return err
	}
	defer r.Close()
	zipData, primaryData, err := finishInstalls(&r.Reader, load, recovery)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, zipData, 0666); err != nil {
		return err
	}
	return os.WriteFile(*primary, primaryData, 0666)
}

// finishInstalls gives the final install zip and the primary file. Each list is nil when it is
// not given. With a load list, an empty one included, the primary file is modules.load, written
// from it; without one it is the modules.dep of the install zip, which then leaves the zip.
// modules.load.recovery goes into the zip when its list is given.
func finishInstalls(r *zip.Reader, load, recovery *givenList) ([]byte, []byte, error) {
	var primary []byte
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	var names []string
	byName := make(map[string]*zip.File)
	for _, f := range r.File {
		names = append(names, f.Name)
		byName[f.Name] = f
	}
	sort.Strings(names)
	for _, name := range names {
		f := byName[name]
		if load == nil && name == "modules.dep" {
			in, err := f.Open()
			if err != nil {
				return nil, nil, err
			}
			primary, err = io.ReadAll(in)
			in.Close()
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		if err := w.Copy(f); err != nil {
			return nil, nil, err
		}
	}
	if load != nil {
		primary = loadFileContent(load.entries)
	} else if primary == nil {
		return nil, nil, fmt.Errorf("the install zip of kernel_modules_builder contains no modules.dep, which the builder writes " +
			"for every partition. Check that kernel_modules_builder of build/soong is unchanged.")
	}
	if recovery != nil {
		f, err := w.Create("modules.load.recovery")
		if err != nil {
			return nil, nil, err
		}
		if _, err := f.Write(loadFileContent(recovery.entries)); err != nil {
			return nil, nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), primary, nil
}
