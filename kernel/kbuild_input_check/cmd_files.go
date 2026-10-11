// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// cmdValue is the value of one variable of a kbuild .cmd file, split into words.
type cmdValue struct {
	file  string
	name  string
	words []string
}

// cmdFilesBatch is the number of .cmd files one make run reads.
const cmdFilesBatch = 2000

// readCmdValues reads the variables whose names match one of the make patterns from each .cmd
// file with the given make, run in dir, so that `$(wildcard ...)` entries expand there as they do
// when kbuild reads the files. Every variable of a file is undefined after it was printed, so the
// variables of one file never show up as those of another.
func readCmdValues(makeBin, dir string, files []string, patterns string) ([]cmdValue, error) {
	var values []cmdValue
	for start := 0; start < len(files); start += cmdFilesBatch {
		end := start + cmdFilesBatch
		if end > len(files) {
			end = len(files)
		}
		batch, err := readCmdValuesBatch(makeBin, dir, files[start:end], patterns)
		if err != nil {
			return nil, err
		}
		values = append(values, batch...)
	}
	return values, nil
}

func readCmdValuesBatch(makeBin, dir string, files []string, patterns string) ([]cmdValue, error) {
	var mk bytes.Buffer
	mk.WriteString(".DEFAULT_GOAL := kbuild_input_check_done\n")
	for _, f := range files {
		fmt.Fprintf(&mk, "include %s\n", f)
		fmt.Fprintf(&mk, "$(foreach v,$(filter %s,$(.VARIABLES)),$(info KBUILD_INPUT_CHECK|%s|$(v)|$($(v))))\n", patterns, f)
		mk.WriteString("$(foreach v,$(filter cmd_% savedcmd_% source_% deps_%,$(.VARIABLES)),$(eval undefine $(v)))\n")
	}
	mk.WriteString("kbuild_input_check_done: ; @:\n")
	tmp, err := os.CreateTemp("", "kbuild_input_check.*.mk")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(mk.Bytes()); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	cmd := exec.Command(makeBin, "-s", "--no-print-directory", "-C", dir, "-f", tmp.Name())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s reading the .cmd files failed: %v\n%s", makeBin, err, stderr.String())
	}
	return parseMakeOutput(out)
}

// parseMakeOutput reads the lines the generated makefile prints, one per variable:
// KBUILD_INPUT_CHECK|<.cmd file>|<variable>|<value>.
func parseMakeOutput(out []byte) ([]cmdValue, error) {
	var values []cmdValue
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "|", 4)
		if len(parts) != 4 || parts[0] != "KBUILD_INPUT_CHECK" {
			continue
		}
		values = append(values, cmdValue{file: parts[1], name: parts[2], words: strings.Fields(parts[3])})
	}
	return values, scanner.Err()
}

// findCmdFiles lists every file named .*<suffix> below dir, in byte order.
func findCmdFiles(dir, suffix string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.Type().IsRegular() && strings.HasPrefix(name, ".") && strings.HasSuffix(name, suffix) {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// relativeToTop writes a path below top relative to it, as the depfiles name tree paths.
func relativeToTop(top, p string) string {
	if top != "" && strings.HasPrefix(p, top+"/") {
		return strings.TrimPrefix(p, top+"/")
	}
	return p
}

// runCheck is the check mode: every read that kbuild recorded in the kept object tree has to lie in
// an allowed directory once its symbolic links are resolved.
func runCheck(args []string) error {
	flags := flag.NewFlagSet("check", flag.ExitOnError)
	makeBin := flags.String("make", "", "the make that reads the .cmd files")
	objDir := flags.String("objdir", "", "the kernel's object directory, where kbuild's make runs")
	srcTree := flags.String("srctree", "", "the kernel source directory, against which Kconfig records its files")
	scanDir := flags.String("scan", "", "the kept object tree whose .cmd files are read")
	top := flags.String("top", "", "the top of the source tree")
	reads := flags.String("reads", "", "file to write the reads inside the toolchain directories to")
	nextStep := flags.String("next-step", "", "what to do about reads outside the allowed directories, which the failure message gives")
	var allow, toolchain multiFlag
	flags.Var(&allow, "allow", "allowed directory or file (repeated)")
	flags.Var(&toolchain, "toolchain", "toolchain directory whose reads are listed (repeated)")
	flags.Parse(args)
	if *makeBin == "" || *objDir == "" || *srcTree == "" || *scanDir == "" || *reads == "" || *nextStep == "" {
		return fmt.Errorf("-make, -objdir, -srctree, -scan, -reads and -next-step are required")
	}

	allowed, err := newAllowList(append(append([]string{}, allow...), toolchain...))
	if err != nil {
		return err
	}
	toolchains, err := newAllowList(toolchain)
	if err != nil {
		return err
	}

	files, err := findCmdFiles(*scanDir, ".cmd")
	if err != nil {
		return err
	}
	autoConf := filepath.Join(*objDir, "include/config/auto.conf.cmd")
	if fileExists(autoConf) {
		files = append(files, autoConf)
	}
	values, err := readCmdValues(*makeBin, *objDir, files, "source_% deps_%")
	if err != nil {
		return err
	}

	bases := readBases{objDir: *objDir, autoConf: autoConf, srcTree: *srcTree}
	bad, list := checkValues(values, bases, *top, allowed, toolchains)
	var data bytes.Buffer
	for _, p := range list {
		data.WriteString(p + "\n")
	}
	if err := writeFileAtomically(*reads, data.Bytes()); err != nil {
		return err
	}

	if len(bad) > 0 {
		return readsOutsideError(bad, *nextStep)
	}
	return nil
}

// readsOutsideError is the failure for reads outside the allowed directories: each read with its
// .cmd file, then nextStep.
func readsOutsideError(bad []outside, nextStep string) error {
	var msg strings.Builder
	msg.WriteString("the kernel build read files outside the directories it is allowed to read:\n")
	for _, o := range bad {
		fmt.Fprintf(&msg, "  %s (recorded in %s)\n", o.path, o.file)
	}
	msg.WriteString(nextStep + "\n" +
		"The .cmd file of an object that the kernel no longer builds is left in the kept object tree;\n" +
		"such a .cmd file can be deleted by hand, and deleting the .cmd file of an object that is still\n" +
		"built only makes kbuild build that object again.")
	return fmt.Errorf("%s", msg.String())
}

// outside is a read outside the allowed directories and the .cmd file that records it.
type outside struct{ path, file string }

// readBases holds the directories that the relative words of the recorded reads are relative to.
// kbuild's make runs in objDir, so the source_ and deps_ words of the .cmd files are relative to
// it. Kconfig records the files it read in the deps_config value of autoConf relative to srcTree,
// the kernel source directory.
type readBases struct {
	objDir   string
	autoConf string
	srcTree  string
}

func (b readBases) of(v cmdValue) string {
	if v.file == b.autoConf && v.name == "deps_config" {
		return b.srcTree
	}
	return b.objDir
}

// checkValues checks every word of the values against the allowed directories after making it
// absolute against its base directory and resolving its symbolic links. It returns the reads
// outside them, in the order found, and the reads inside the toolchain directories that exist,
// relative to top when they lie below it, sorted.
func checkValues(values []cmdValue, bases readBases, top string, allowed, toolchains allowList) ([]outside, []string) {
	var bad []outside
	seenBad := make(map[outside]bool)
	toolchainReads := make(map[string]bool)
	for _, v := range values {
		base := bases.of(v)
		for _, word := range v.words {
			resolved := resolvePath(absolute(base, word))
			if !allowed.allows(resolved) {
				o := outside{resolved, v.file}
				if !seenBad[o] {
					seenBad[o] = true
					bad = append(bad, o)
				}
				continue
			}
			if toolchains.allows(resolved) && fileExists(resolved) {
				toolchainReads[relativeToTop(top, resolved)] = true
			}
		}
	}
	var list []string
	for p := range toolchainReads {
		list = append(list, p)
	}
	sort.Strings(list)
	return bad, list
}

// runHeaders is the header mode: every header that headers_install installed below the staging
// directory has to come from an allowed directory or from the staging directory.
func runHeaders(args []string) error {
	flags := flag.NewFlagSet("headers", flag.ExitOnError)
	makeBin := flags.String("make", "", "the make that reads the .cmd files")
	staging := flags.String("staging", "", "the object directory of headers_install")
	nextStep := flags.String("next-step", "", "what to do about headers from outside the allowed directories, which the failure message gives")
	var allow multiFlag
	flags.Var(&allow, "allow", "allowed directory (repeated)")
	flags.Parse(args)
	if *makeBin == "" || *staging == "" || *nextStep == "" {
		return fmt.Errorf("-make, -staging and -next-step are required")
	}
	allowed, err := newAllowList(append(append([]string{}, allow...), *staging))
	if err != nil {
		return err
	}
	files, err := findCmdFiles(filepath.Join(*staging, "usr/include"), ".h.cmd")
	if err != nil {
		return err
	}
	values, err := readCmdValues(*makeBin, *staging, files, "cmd_%")
	if err != nil {
		return err
	}
	return headerError(checkHeaderValues(values, *staging, allowed), *nextStep)
}

// headerProblems are the installed headers whose source is not checked: unknownForm, those whose
// install command does not have the form of kbuild's cmd_install, and outside, those that come from
// outside the allowed directories.
type headerProblems struct{ unknownForm, outside []string }

// checkHeaderValues checks the install commands of the installed headers: each has the form of
// kbuild's cmd_install, and its source, made absolute against the staging directory and resolved,
// lies in an allowed directory; allowed includes the staging directory.
func checkHeaderValues(values []cmdValue, staging string, allowed allowList) headerProblems {
	var p headerProblems
	for _, v := range values {
		w := v.words
		if len(w) != 4 || w[0] != "sh" || !strings.HasSuffix(w[1], "/scripts/headers_install.sh") {
			p.unknownForm = append(p.unknownForm, fmt.Sprintf("  %s: %q", v.file, strings.Join(w, " ")))
			continue
		}
		resolved := resolvePath(absolute(staging, w[2]))
		if !allowed.allows(resolved) {
			p.outside = append(p.outside, fmt.Sprintf("  %s comes from %s", w[3], resolved))
		}
	}
	return p
}

// headerError is the failure for the header problems, each kind with its next step, nextStep for the
// headers from outside the allowed directories, or nil without a problem.
func headerError(p headerProblems, nextStep string) error {
	var parts []string
	if len(p.unknownForm) > 0 {
		parts = append(parts, "installed headers whose install command does not have the form "+
			"`sh <kernel>/scripts/headers_install.sh <source> <destination>`:\n"+strings.Join(p.unknownForm, "\n")+
			"\nUse a kernel source whose headers_install installs each header with scripts/headers_install.sh.")
	}
	if len(p.outside) > 0 {
		parts = append(parts, "installed headers from outside the allowed directories:\n"+strings.Join(p.outside, "\n")+"\n"+nextStep)
	}
	if len(parts) == 0 {
		return nil
	}
	return errors.New(strings.Join(parts, "\n"))
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
