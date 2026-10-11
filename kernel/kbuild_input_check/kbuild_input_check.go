// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

// kbuild_input_check checks and records the inputs of the kernel build rules.
//
// Modes:
//
//	check     lists the source_, deps_ and deps_config values of the .*.cmd files of a kept kbuild
//	          object tree, fails for every read outside the allowed directories and writes the
//	          reads inside the toolchain directories to a file.
//	depfile   writes a depfile naming every path below the given entries, leaving out symbolic
//	          links whose target does not exist.
//	links     fails for every symbolic link below the given entries that resolves outside the
//	          allowed directories.
//	headers   checks the sources of the headers that headers_install installed.
//	identity  prints one line per file and symbolic link below the given paths, with a cache of
//	          the file hashes.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// multiFlag collects the values of a flag given several times.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, " ") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: kbuild_input_check check|depfile|links|headers|identity [flags] [paths...]")
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "check":
		err = runCheck(os.Args[2:])
	case "depfile":
		err = runDepfile(os.Args[2:])
	case "links":
		err = runLinks(os.Args[2:])
	case "headers":
		err = runHeaders(os.Args[2:])
	case "identity":
		err = runIdentity(os.Args[2:], os.Stdout)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "kbuild_input_check %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

// resolvePath resolves the symbolic links of an absolute path. The longest leading part of the
// path that exists is resolved with filepath.EvalSymlinks and the rest is appended, so a path that
// no longer exists is still resolved as far as it can be. The path is not cleaned before, since a
// `..` after a symbolic link leads out of the link's target, not out of the link's directory.
func resolvePath(p string) string {
	cur := p
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			if rest == "" {
				return filepath.Clean(r)
			}
			return filepath.Clean(r + "/" + rest)
		}
		i := strings.LastIndex(cur, "/")
		if i <= 0 {
			return filepath.Clean(p)
		}
		if rest == "" {
			rest = cur[i+1:]
		} else {
			rest = cur[i+1:] + "/" + rest
		}
		cur = cur[:i]
	}
}

// absolute makes p absolute against dir without cleaning it.
func absolute(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return dir + "/" + p
}

// allowList holds resolved directories and files; a path is allowed when it is one of them or lies
// below one of them.
type allowList []string

func newAllowList(paths []string) (allowList, error) {
	var l allowList
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			wd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			p = absolute(wd, p)
		}
		l = append(l, resolvePath(p))
	}
	return l, nil
}

func (l allowList) allows(resolved string) bool {
	for _, a := range l {
		if resolved == a || strings.HasPrefix(resolved, a+"/") || a == "/" {
			return true
		}
	}
	return false
}

// depfileEntries walks each entry with filepath.WalkDir, which follows no symbolic link, and
// returns every path it visits, the entries included, leaving out every symbolic link whose target
// does not exist. Any other error stops the walk.
func depfileEntries(entries []string) ([]string, error) {
	var paths []string
	for _, entry := range entries {
		err := filepath.WalkDir(entry, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 {
				if _, err := os.Stat(path); err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						return nil
					}
					return fmt.Errorf("%s: %w", path, err)
				}
			}
			paths = append(paths, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return paths, nil
}

// readList reads a file with one path per line.
func readList(file string) ([]string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// writeFileAtomically writes data to a temporary file beside file and renames it over file.
func writeFileAtomically(file string, data []byte) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0666); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
