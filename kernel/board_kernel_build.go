// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"android/soong/android"
	mkparser "android/soong/androidmk/parser"

	"github.com/google/blueprint/pathtools"
	"github.com/google/blueprint/proptools"
)

// boardKernelBuild is the kernel_build module that the board variables give: its properties, and
// the names and the next steps that its messages use for them.
type boardKernelBuild struct {
	properties     *kernelBuildProperties
	names          settingNames
	sourceDirSteps sourceDirSteps
}

// boardKernelBuildProperties converts the namespace into the kernel_build module. kernel_build
// checks the values, so a problem that it finds is reported with the board variable. A value that
// does not convert is reported here.
func boardKernelBuildProperties(ctx android.LoadHookContext, vars boardVars) boardKernelBuild {
	kernelDir := vars.value(targetKernelSource)
	arch := kernelArch(vars.value(targetKernelArch), ctx.Config().DevicePrimaryArchType())
	dtsDir := path.Join("arch", arch, "boot", "dts")
	dtImagesVars := fmt.Sprintf("BOARD_INCLUDE_DTB_IN_BOOTIMG, %s or %s", targetNeedsDtboimage, boardKernelSeparatedDtbo)
	steps := sourceDirSteps{
		add:    fmt.Sprintf("Add a link to the directory that contains these files directly inside %s of %s.", dtsDir, targetKernelSource),
		remove: fmt.Sprintf("Remove the link to it from %s of %s.", dtsDir, targetKernelSource),
	}
	names := settingNames{
		propKernelDir:           targetKernelSource.name,
		propExtraSourceDirs:     fmt.Sprintf("the links directly inside %s of %s", dtsDir, targetKernelSource),
		propKernelArch:          targetKernelArch.name,
		propImage:               boardKernelImageName.name,
		propDefconfig:           targetKernelConfig.name,
		propConfigFragments:     targetKernelConfig.name,
		propExternalModules:     targetKernelExtModules.name,
		propExternalModulesRoot: targetKernelExtModuleRoot.name,
		propExternalModulesDirs: targetKernelExtModules.name,
		propModuleRenames:       targetModuleAliases.name,
		propDtImages:            dtImagesVars,
		propDtImagesMerge:       boardUsesQcomMergeDtbsScript.name,
		propDtboPageSize:        boardKernelPagesize.name,
	}

	checkKernelVersion(ctx, vars, kernelDir)
	p := &kernelBuildProperties{
		Kernel_dir:        proptools.StringPtr(kernelDir),
		Extra_source_dirs: linkedSourceDirs(ctx, kernelDir, dtsDir),
		Kernel_arch:       proptools.StringPtr(arch),
		Image:             vars.optional(boardKernelImageName),
		Module_renames:    vars.words(targetModuleAliases),
	}
	if config := vars.words(targetKernelConfig); len(config) > 0 {
		p.Defconfig = proptools.StringPtr(config[0])
		p.Config_fragments = config[1:]
	}
	// TARGET_KERNEL_CLANG_PATH wins over TARGET_KERNEL_CLANG_VERSION.
	if v := vars.value(targetKernelClangPath); v != "" {
		p.Clang_dir = proptools.StringPtr(v)
		names[propClangDir] = targetKernelClangPath.name
	} else if v := vars.value(targetKernelClangVersion); v != "" {
		p.Clang_dir = proptools.StringPtr(path.Join(clangBase, "clang-"+v))
		names[propClangDir] = targetKernelClangVersion.name
	}
	if vars.isSet(targetKernelExtModules) {
		p.External_modules.Root = vars.optional(targetKernelExtModuleRoot)
		p.External_modules.Dirs = vars.words(targetKernelExtModules)
	}

	partitionVars := ctx.Config().ProductVariables().PartitionVarsForSoongMigrationOnlyDoNotUse
	if partitionVars.BoardIncludeDtbInBootimg || vars.isTrue(targetNeedsDtboimage) || vars.isTrue(boardKernelSeparatedDtbo) {
		p.Dt_images.Merge = proptools.StringPtr(mergeNone)
		if vars.isTrue(boardUsesQcomMergeDtbsScript) {
			p.Dt_images.Merge = proptools.StringPtr(mergeQcom)
		}
		if pageSize := vars.optional(boardKernelPagesize); pageSize != nil {
			if n, err := strconv.ParseInt(*pageSize, 10, 64); err != nil {
				ctx.ModuleErrorf("%s is %q, which is not a number. Set %s to the page size of the device in bytes, such as 4096.",
					boardKernelPagesize, *pageSize, boardKernelPagesize)
			} else {
				p.Dt_images.Dtbo_page_size = proptools.Int64Ptr(n)
			}
		}
	}
	return boardKernelBuild{properties: p, names: names, sourceDirSteps: steps}
}

// kernelVersion is the version of a kernel source: VERSION and PATCHLEVEL of its Makefile.
type kernelVersion struct{ version, patchLevel int }

func (v kernelVersion) atLeast(o kernelVersion) bool {
	return v.version > o.version || v.version == o.version && v.patchLevel >= o.patchLevel
}

func (v kernelVersion) String() string { return fmt.Sprintf("%d.%d", v.version, v.patchLevel) }

// From 5.10 a kernel builds with clang and LLVM=1 alone, older kernels select GCC tools unless
// TARGET_KERNEL_NO_GCC is true, and from 6.11 the host programs of a kernel need the host sysroot.
var (
	clangAloneFrom  = kernelVersion{5, 10}
	hostSysrootFrom = kernelVersion{6, 11}
)

// sourceFileSystem is the file system of the source tree, which the context of a load hook has
// through blueprint, although android.LoadHookContext does not declare it.
type sourceFileSystem interface{ Fs() pathtools.FileSystem }

// checkKernelVersion reports a kernel source whose version the board configuration builds
// otherwise than kernel_build: 6.11 and newer, and older than 5.10 without TARGET_KERNEL_NO_GCC. A
// kernel source that does not exist is left to kernel_build.
func checkKernelVersion(ctx android.LoadHookContext, vars boardVars, kernelDir string) {
	if !android.ExistentPathForSource(ctx, kernelDir).Valid() {
		return
	}
	makefile := path.Join(kernelDir, "Makefile")
	if !android.ExistentPathForSource(ctx, makefile).Valid() {
		ctx.ModuleErrorf("%s %s contains no Makefile. Sync the kernel repository to %s, or correct %s.",
			targetKernelSource, kernelDir, kernelDir, targetKernelSource)
		return
	}
	ctx.AddNinjaFileDeps(makefile)
	r, err := ctx.(sourceFileSystem).Fs().Open(makefile)
	if err != nil {
		ctx.ModuleErrorf("reading %s: %s. Make the file readable.", makefile, err)
		return
	}
	defer r.Close()
	v, err := readKernelVersion(makefile, r)
	if err != nil {
		ctx.ModuleErrorf("%s %s: %s. Set %s to a Linux kernel source.", targetKernelSource, kernelDir, err, targetKernelSource)
		return
	}
	switch {
	case v.atLeast(hostSysrootFrom):
		ctx.ModuleErrorf("%s %s contains Linux %s, and kernels %s and newer are not supported, since their host programs need "+
			"the host sysroot. Use a kernel source below %s.", targetKernelSource, kernelDir, v, hostSysrootFrom, hostSysrootFrom)
	case !v.atLeast(clangAloneFrom) && !vars.isTrue(targetKernelNoGcc):
		ctx.ModuleErrorf("%s %s contains Linux %s. Kernels older than %s build with GCC tools unless %s is true, and kernel_build "+
			"uses clang and LLVM=1 only. Set %s to true to build the kernel with clang and LLVM=1, or use a kernel source of %s "+
			"or newer.", targetKernelSource, kernelDir, v, clangAloneFrom, targetKernelNoGcc, targetKernelNoGcc, clangAloneFrom)
	}
}

// readKernelVersion reads the version of a kernel source from its Makefile: the values of the first
// assignments of VERSION and PATCHLEVEL, which have to be numbers.
func readKernelVersion(file string, r io.Reader) (kernelVersion, error) {
	nodes, errs := mkparser.NewParser(file, r).Parse()
	if len(errs) > 0 {
		return kernelVersion{}, fmt.Errorf("the Makefile cannot be parsed: %w", errs[0])
	}
	values := make(map[string]*mkparser.MakeString)
	for _, n := range nodes {
		if a, ok := n.(*mkparser.Assignment); ok && a.Target == nil && a.Name.Const() {
			if variable := strings.TrimSpace(a.Name.Value(nil)); values[variable] == nil {
				values[variable] = a.Value
			}
		}
	}
	number := func(variable string) (int, error) {
		v := values[variable]
		if v != nil && v.Const() {
			if n, err := strconv.Atoi(strings.TrimSpace(v.Value(nil))); err == nil {
				return n, nil
			}
		}
		return 0, fmt.Errorf("the Makefile assigns no number to %s", variable)
	}
	version, err := number("VERSION")
	if err != nil {
		return kernelVersion{}, err
	}
	patchLevel, err := number("PATCHLEVEL")
	if err != nil {
		return kernelVersion{}, err
	}
	return kernelVersion{version, patchLevel}, nil
}

// kernelArch gives the kernel architecture for a value of TARGET_KERNEL_ARCH: for the name of a
// device architecture, such as x86_64, its kernel architecture, such as x86, and any other value as
// it is. Without a value it is the kernel architecture of the device's primary architecture.
func kernelArch(value string, device android.ArchType) string {
	arch := device
	if value != "" && arch.UnmarshalText([]byte(value)) != nil {
		return value
	}
	if arches, ok := kernelArches[arch]; ok {
		return arches[0]
	}
	return value
}

// linkedSourceDirs gives the directories outside kernelDir that the links directly inside dtsDir of
// the kernel source lead to, such as the device tree sources that a Qualcomm kernel takes from
// another repository, each once and in byte order. A link that the build cannot use is reported.
func linkedSourceDirs(ctx android.LoadHookContext, kernelDir, dtsDir string) []string {
	dts := path.Join(kernelDir, dtsDir)
	entries, err := ctx.GlobWithDeps(dts+"/*", nil)
	if err != nil {
		ctx.ModuleErrorf("glob of %s: %s", dts, err)
		return nil
	}
	if len(entries) > 0 {
		// The glob makes Soong analyse again when a link appears or disappears, or when its target
		// becomes a directory. A link that is pointed at another directory is replaced in dts,
		// which changes the modification time of dts.
		ctx.AddNinjaFileDeps(dts)
	}
	links := make(map[string]dtsLink)
	for _, e := range entries {
		// The glob marks a directory, and a link to one, with a trailing slash.
		if link := android.PathForSource(ctx, strings.TrimSuffix(e, "/")); ctx.IsSymlink(link) {
			links[link.String()] = dtsLink{target: ctx.Readlink(link), toDir: strings.HasSuffix(e, "/")}
		}
	}
	exists := func(p string) bool { return android.ExistentPathForSource(ctx, p).Valid() }
	dirs, problems := linkTargetsOutside(kernelDir, links, exists)
	for _, problem := range problems {
		ctx.ModuleErrorf("%s", problem)
	}
	return dirs
}

// dtsLink is a link directly inside the dts directory: the target that it contains, and whether it
// leads to a directory.
type dtsLink struct {
	target string
	toDir  bool
}

// linkTargetsOutside gives the directories outside kernelDir that links lead to, each once and in
// byte order, and a message with the next step for each link to an absolute path, out of the source
// tree or to a target that does not exist. links maps the path of each link from the tree top to
// the link. exists reports whether a path from the tree top exists; it tells a link to a file, which
// is no source directory, from a link to nothing.
func linkTargetsOutside(kernelDir string, links map[string]dtsLink, exists func(string) bool) (dirs, problems []string) {
	for _, linkPath := range android.SortedKeys(links) {
		link := links[linkPath]
		if path.IsAbs(link.target) {
			problems = append(problems, fmt.Sprintf("The link %s leads to the absolute path %s. "+
				"Replace it with a link relative to its own directory.", linkPath, link.target))
			continue
		}
		dir := path.Join(path.Dir(linkPath), link.target)
		switch {
		case dir == ".." || strings.HasPrefix(dir, "../"):
			problems = append(problems, fmt.Sprintf("The link %s leads to %s, outside the source tree. "+
				"Move the directory into the source tree, and link to it there.", linkPath, link.target))
		case !link.toDir && !exists(dir):
			problems = append(problems, fmt.Sprintf("The link %s leads to %s, which does not exist. "+
				"Sync the repository to %s.", linkPath, link.target, dir))
		case link.toDir && dir != kernelDir && !strings.HasPrefix(dir, kernelDir+"/"):
			dirs = append(dirs, dir)
		}
	}
	return android.SortedUniqueStrings(dirs), problems
}
