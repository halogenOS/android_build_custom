// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"android/soong/android"

	"github.com/google/blueprint/pathtools"
	"github.com/google/blueprint/proptools"
)

// replacedInstallDirs keeps every other producer out of an install directory that an install
// replaces as a whole. It writes two lists of the installs that extract an extra files zip into the
// parent directory of their primary file, each on every run and as an empty JSON array when no
// install of its kind exists:
//   - replaced_install_dirs, the directories that such an install replaces as a whole. soong_ui
//     reads it to remove a directory that is no longer installed, and the signing run reads it.
//   - extracted_zip_installs, the primary files of the other such installs, which keep the other
//     files in the directory. The signing run reads it.
//
// Such an install declares only its primary file, so the signing run passes each primary file of
// both lists to siso, which copies the whole parent directory from the regular build.
type replacedInstallDirs struct {
	// entriesForTesting holds the entries of the list in a unit test.
	entriesForTesting []replacedDirEntry
}

func replacedInstallDirsFactory() android.Singleton {
	return &replacedInstallDirs{}
}

// installedFile is a file that one module variant installs with a full install.
type installedFile struct {
	module string
	path   string
}

// extractedZip is an install of one module variant that extracts an extra files zip into dir, the
// parent directory of its primary file, with a full install that does not replace the directory.
type extractedZip struct {
	module  string
	primary string
	dir     string
}

// replacedDir is a directory that an install replaces as a whole.
type replacedDir struct {
	module  string
	primary string
	dir     string
}

// replacedDirEntry is one entry of the list soong_ui reads: the primary file and the directory of
// a replaced directory, both relative to the product out directory.
type replacedDirEntry struct {
	Primary string `json:"primary"`
	Dir     string `json:"dir"`
}

// buildFromSourceStep is the next step for a kernel module from outside the kernel build in a
// replaced directory, whose modules come from a kernel built from source: a prebuilt module does not
// match that kernel.
const buildFromSourceStep = "Build the kernel module from the kernel source instead: enable it in the kernel " +
	"configuration, or build it as an external module."

// isKernelModule reports whether the file is a kernel module.
func isKernelModule(path string) bool { return strings.HasSuffix(path, ".ko") }

// atOrBelow reports whether path is dir or lies below it.
func atOrBelow(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

func (s *replacedInstallDirs) GenerateBuildActions(ctx android.SingletonContext) {
	var replaced []replacedDir
	var files []installedFile
	var zips []extractedZip
	ctx.VisitAllModuleProxies(func(m android.ModuleProxy) {
		name := ctx.ModuleName(m)
		if sub := ctx.ModuleSubDir(m); sub != "" {
			name += " (" + sub + ")"
		}
		info := android.GetInstallFiles(ctx, m)
		// InstallFiles holds only full installs: an install adds its path there only when the
		// module requires a full install.
		for _, f := range info.InstallFiles {
			files = append(files, installedFile{module: name, path: f.String()})
		}
		for _, spec := range info.PackagingSpecs {
			if !spec.ExtraZip().Valid() || !spec.RequiresFullInstall() {
				continue
			}
			full := spec.FullInstallPath().String()
			// An extra files zip is extracted into the directory of its primary file.
			dir := filepath.Dir(full)
			if spec.ReplacesInstallDir() {
				replaced = append(replaced, replacedDir{module: name, primary: full, dir: dir})
			} else {
				zips = append(zips, extractedZip{module: name, primary: full, dir: dir})
			}
		}
	})

	for _, r := range replaced {
		for _, other := range replaced {
			if other.module != r.module && (atOrBelow(other.dir, r.dir) || atOrBelow(r.dir, other.dir)) {
				ctx.Errorf("%s replaces the install directory %s as a whole, and %s extracts its install zip into %s. "+
					"Install one of the two modules into another directory.", r.module, r.dir, other.module, other.dir)
			}
		}
		for _, f := range files {
			if f.module != r.module && atOrBelow(f.path, r.dir) {
				step := fmt.Sprintf("Install %s into another directory, or remove it from the product.", f.module)
				if isKernelModule(f.path) {
					step = fmt.Sprintf("Remove %s from the product. %s", f.module, buildFromSourceStep)
				}
				ctx.Errorf("%s replaces the install directory %s as a whole, and %s installs %s there. %s",
					r.module, r.dir, f.module, f.path, step)
			}
		}
		for _, z := range zips {
			if z.module != r.module && (atOrBelow(z.dir, r.dir) || atOrBelow(r.dir, z.dir)) {
				ctx.Errorf("%s replaces the install directory %s as a whole, and %s extracts its install zip into %s. "+
					"Install one of the two modules into another directory.", r.module, r.dir, z.module, z.dir)
			}
		}
	}

	productOut := android.PathForDeviceFirstInstall(ctx).String()
	for _, c := range productCopyFiles(ctx.Config()) {
		dest := filepath.Join(productOut, c.dest)
		for _, r := range replaced {
			if atOrBelow(dest, r.dir) {
				step := "Remove the entry, or copy the file into another directory."
				if isKernelModule(dest) {
					step = "Remove the entry. " + buildFromSourceStep
				}
				ctx.Errorf("%s replaces the install directory %s as a whole, and the PRODUCT_COPY_FILES entry %q copies a file there. %s",
					r.module, r.dir, c.entry, step)
			}
		}
	}

	entries := replacedDirEntries(productOut, replaced)
	if ctx.Config().RunningInsideUnitTest() {
		s.entriesForTesting = entries
		return
	}
	writeList(ctx, "replaced_install_dirs", entries)
	writeList(ctx, "extracted_zip_installs", extractedZipPrimaries(productOut, zips))
}

// writeList writes list as JSON into the file of the out directory that has name with the product
// suffix.
func writeList(ctx android.SingletonContext, name string, list any) {
	content, err := json.Marshal(list)
	if err != nil {
		ctx.Errorf("%s", err.Error())
		return
	}
	out := android.PathForOutput(ctx, name+proptools.String(ctx.Config().ProductVariables().Make_suffix)+".json").String()
	if err := pathtools.WriteFileIfChanged(absoluteOutPath(out), append(content, '\n'), 0666); err != nil {
		ctx.Errorf("%s", err.Error())
	}
}

// replacedDirEntries gives one entry for each replaced directory below productOut, both paths
// relative to it, in byte order of the directory. The result is never nil, so the list is a JSON
// array even without an entry.
func replacedDirEntries(productOut string, replaced []replacedDir) []replacedDirEntry {
	entries := []replacedDirEntry{}
	for _, r := range replaced {
		primary, primaryOk := strings.CutPrefix(r.primary, productOut+"/")
		dir, dirOk := strings.CutPrefix(r.dir, productOut+"/")
		if primaryOk && dirOk {
			entries = append(entries, replacedDirEntry{Primary: primary, Dir: dir})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Dir < entries[j].Dir })
	return entries
}

// extractedZipPrimaries gives the primary file of each install in zips below productOut, relative
// to it, once each and in byte order. The result is never nil, so the list is a JSON array even
// without an entry.
func extractedZipPrimaries(productOut string, zips []extractedZip) []string {
	primaries := []string{}
	for _, z := range zips {
		if primary, ok := strings.CutPrefix(z.primary, productOut+"/"); ok {
			primaries = append(primaries, primary)
		}
	}
	slices.Sort(primaries)
	return slices.Compact(primaries)
}

// absoluteOutPath makes a path of the out directory absolute; soong_build runs at the tree top.
func absoluteOutPath(p string) string {
	if resolved, err := filepath.Abs(p); err == nil {
		return resolved
	}
	return p
}
