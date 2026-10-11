// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"android/soong/makedeps"
)

// runDepfile is the depfile mode: it names every path below the entries in the depfile of a
// target, as dir_to_depfile does, and leaves out every symbolic link whose target does not exist,
// which nothing can read. It writes nothing when its walk fails.
func runDepfile(args []string) error {
	flags := flag.NewFlagSet("depfile", flag.ExitOnError)
	output := flags.String("o", "", "the depfile to write")
	target := flags.String("t", "", "the target the depfile is for")
	var lists multiFlag
	flags.Var(&lists, "list", "file with further entries, one per line (repeated)")
	flags.Parse(args)
	if *output == "" || *target == "" {
		return fmt.Errorf("-o and -t are required")
	}
	entries := append([]string{}, flags.Args()...)
	for _, l := range lists {
		more, err := readList(l)
		if err != nil {
			return err
		}
		entries = append(entries, more...)
	}
	paths, err := depfileEntries(entries)
	if err != nil {
		return err
	}
	deps := makedeps.Deps{Output: *target, Inputs: paths}
	return os.WriteFile(*output, deps.Print(), 0666)
}

// runLinks is the link mode: every symbolic link below the entries whose target exists has to
// resolve inside an allowed directory. A link whose target does not exist is passed over.
func runLinks(args []string) error {
	flags := flag.NewFlagSet("links", flag.ExitOnError)
	nextStep := flags.String("next-step", "", "what to do about links outside the allowed directories, which the failure message gives")
	var allow multiFlag
	flags.Var(&allow, "allow", "allowed directory (repeated)")
	flags.Parse(args)
	if *nextStep == "" {
		return fmt.Errorf("-next-step is required")
	}
	allowed, err := newAllowList(allow)
	if err != nil {
		return err
	}
	var problems []string
	for _, entry := range flags.Args() {
		err := filepath.WalkDir(entry, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return fmt.Errorf("%s: %w", path, err)
			}
			if !filepath.IsAbs(resolved) {
				if resolved, err = filepath.Abs(resolved); err != nil {
					return err
				}
			}
			if !allowed.allows(resolved) {
				target, _ := os.Readlink(path)
				problems = append(problems, fmt.Sprintf("  %s -> %s (resolves to %s)", path, target, resolved))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("symbolic links that lead to files outside the allowed directories:\n%s\n%s", strings.Join(problems, "\n"), *nextStep)
	}
	return nil
}

// fileStat is what the identity cache keeps of a file to tell whether it may have changed. The
// times are in nanoseconds.
type fileStat struct {
	Size  int64 `json:"size"`
	Inode int64 `json:"inode"`
	Mtime int64 `json:"mtime"`
	Ctime int64 `json:"ctime"`
}

func statOf(info fs.FileInfo) fileStat {
	s := fileStat{Size: info.Size()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		s.Inode = int64(st.Ino)
		s.Mtime = st.Mtim.Sec*1e9 + st.Mtim.Nsec
		s.Ctime = st.Ctim.Sec*1e9 + st.Ctim.Nsec
	}
	return s
}

// cacheEntry is a file's entry in the identity cache, a JSON object keyed by the file's path.
type cacheEntry struct {
	Stat   fileStat `json:"stat"`
	Sha256 string   `json:"sha256"`
}

// readHashCache reads the cache of file hashes; a cache that cannot be read or decoded is treated
// as empty.
func readHashCache(file string) map[string]cacheEntry {
	data, err := os.ReadFile(file)
	if err != nil {
		return make(map[string]cacheEntry)
	}
	var cache map[string]cacheEntry
	if err := json.Unmarshal(data, &cache); err != nil || cache == nil {
		return make(map[string]cacheEntry)
	}
	return cache
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// skippedByIdentity reports the Python bytecode caches, which a Python program may write into a
// toolchain directory without changing the toolchain.
func skippedByIdentity(path string) bool {
	if strings.HasSuffix(path, ".pyc") {
		return true
	}
	for _, part := range strings.Split(path, "/") {
		if part == "__pycache__" {
			return true
		}
	}
	return false
}

// runIdentity is the identity mode: one line per file (its sha256 and path) and per symbolic link
// (its path and target) below each path, in byte order of the paths. A file is read again only when
// its size, inode, modification time or change time differs from the cache.
func runIdentity(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("identity", flag.ExitOnError)
	cacheFile := flags.String("cache", "", "the cache of file hashes")
	var linkDirs multiFlag
	flags.Var(&linkDirs, "link-targets", "directory whose symbolic links' targets are covered (repeated)")
	flags.Parse(args)
	if *cacheFile == "" {
		return fmt.Errorf("-cache is required")
	}
	roots := append([]string{}, flags.Args()...)
	for _, dir := range linkDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Type()&fs.ModeSymlink == 0 {
				continue
			}
			target, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
			if err != nil {
				return err
			}
			roots = append(roots, target)
		}
	}

	cache := readHashCache(*cacheFile)
	newCache := make(map[string]cacheEntry)
	lines := make(map[string]string)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if skippedByIdentity(path) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				lines[path] = path + " -> " + target
			case d.Type().IsRegular():
				info, err := d.Info()
				if err != nil {
					return err
				}
				st := statOf(info)
				hash := ""
				if c, ok := cache[path]; ok && c.Stat == st {
					hash = c.Sha256
				} else if hash, err = hashFile(path); err != nil {
					return err
				}
				newCache[path] = cacheEntry{st, hash}
				lines[path] = hash + "  " + path
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	paths := make([]string, 0, len(lines))
	for p := range lines {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	w := bufio.NewWriter(out)
	for _, p := range paths {
		fmt.Fprintln(w, lines[p])
	}
	if err := w.Flush(); err != nil {
		return err
	}

	cacheData, err := json.Marshal(newCache)
	if err != nil {
		return err
	}
	return writeFileAtomically(*cacheFile, cacheData)
}
