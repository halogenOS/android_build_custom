// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

// Package kernel contains the Soong module types that build a kernel from source.
package kernel

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"android/soong/android"
	"android/soong/cc/config"

	"github.com/google/blueprint"
	"github.com/google/blueprint/proptools"
)

var pctx = android.NewPackageContext("custom/soong/kernel")

func init() {
	registerBuildComponents(android.InitRegistrationContext)
}

func registerBuildComponents(ctx android.RegistrationContext) {
	ctx.RegisterModuleType("kernel_build", KernelBuildFactory)
	ctx.RegisterModuleType("kernel_uapi_headers", KernelUapiHeadersFactory)
}

// Paths of the tree that the kernel rules use. Every path below prebuilts/build-tools in the
// package comes from these.
const (
	buildTools      = "prebuilts/build-tools"
	buildToolsBin   = buildTools + "/linux-x86/bin"
	buildToolsPath  = buildTools + "/path/linux-x86"
	bisonData       = buildTools + "/common/bison"
	perlPath        = "prebuilts/tools-lineage/linux-x86/bin/perl"
	perlBase        = "prebuilts/tools-lineage/common/perl-base"
	glibcSysroot    = "prebuilts/gcc/linux-x86/host/x86_64-linux-glibc2.17-4.8/sysroot"
	boringsslHeader = "external/boringssl/src/include"
	clangRoot       = "prebuilts/clang"
	clangBase       = clangRoot + "/host/linux-x86"
)

// The tags of the outputs of a kernel_build module, besides "" for the image.
const (
	tagKernel    = ".kernel"
	tagSystemMap = ".system_map"
	tagModules   = ".modules"
	tagDtbs      = ".dtbs"
	tagDtb       = ".dtb"
	tagDtbo      = ".dtbo"
)

// The values of dt_images.merge.
const (
	mergeQcom = "qcom"
	mergeNone = "none"
)

// The programs merge_dtbs runs by name for dt_images `merge: "qcom"`. Two of them, fdtput and
// fdtoverlaymerge, are defined only by the external/dtc fork.
var qcomDtTools = []string{"fdtget", "fdtoverlay", "fdtoverlaymerge", "fdtput", "ufdt_apply_overlay"}

// KernelSourcesInfo carries what a kernel_uapi_headers module takes from the kernel_build module
// it exports the headers of, so that the settings of a kernel exist in one place only.
// @auto-generate: gob
type KernelSourcesInfo struct {
	KernelDir           string
	ExtraSourceDirs     []string
	ExternalModulesRoot string
	KernelArch          string
	// ClangDir is the clang directory the kernel is built with, relative to the tree top.
	ClangDir string
	// AddSourceDirStep is the next step for files outside the source directories that the header
	// rule reads.
	AddSourceDirStep string
	// MissingPaths is the message of the paths of the kernel_build module's properties that do
	// not exist, empty when every path exists.
	MissingPaths string
}

var KernelSourcesInfoProvider = blueprint.NewProvider[KernelSourcesInfo]()

// setKernelSourcesInfo sets the provider of a kernel_build module whose properties are valid.
// missingPathsMessage is the message of the module's missing paths, empty when every path exists.
func setKernelSourcesInfo(ctx android.ModuleContext, k *kernelBuild, missingPathsMessage string) {
	p := &k.properties
	android.SetProvider(ctx, KernelSourcesInfoProvider, KernelSourcesInfo{
		KernelDir:           proptools.String(p.Kernel_dir),
		ExtraSourceDirs:     p.Extra_source_dirs,
		ExternalModulesRoot: k.externalModulesRoot(),
		KernelArch:          proptools.String(p.Kernel_arch),
		ClangDir:            k.clangDir(ctx),
		AddSourceDirStep:    k.addSourceDirStep(),
		MissingPaths:        missingPathsMessage,
	})
}

type kernelBuildProperties struct {
	// The kernel source directory, relative to the top of the source tree.
	Kernel_dir *string

	// Directories the kernel source reaches through symbolic links, relative to the top of the
	// source tree. They are inputs of the kernel build.
	Extra_source_dirs []string

	// The kernel's name of its architecture, passed as ARCH=: arm, arm64, riscv or x86.
	Kernel_arch *string

	// The image target and the file arch/<kernel_arch>/boot/<image>, such as Image.
	Image *string

	// The base configuration, relative to <kernel_dir>/arch/<kernel_arch>/configs.
	Defconfig *string

	// Configuration fragments merged in this order, relative to
	// <kernel_dir>/arch/<kernel_arch>/configs.
	Config_fragments []string

	// The clang directory, relative to the top of the source tree and below prebuilts/clang.
	// Defaults to the platform's clang directory.
	Clang_dir *string

	External_modules struct {
		// The external module tree, relative to the top of the source tree.
		Root *string

		// Module directories below root, built in this order.
		Dirs []string
	}

	// Renames of kernel modules by exact base name, each `from.ko:to.ko`.
	Module_renames []string

	Dt_images struct {
		// How dtb.img and dtbo.img are made: `qcom` merges the techpack overlays of a Qualcomm
		// kernel with merge_dtbs, `none` packs the device tree files as the kernel builds them.
		Merge *string

		// The page size passed to mkdtboimg.
		Dtbo_page_size *int64
	}
}

type kernelBuild struct {
	android.ModuleBase

	properties kernelBuildProperties

	// names are the names that the module's messages use for its properties, and sourceDirSteps
	// the next steps for a directory outside its source directories. The factory sets them.
	names          settingNames
	sourceDirSteps sourceDirSteps

	// dtboImage is the dtbo.img that BOARD_PREBUILT_DTBOIMAGE names for the named module.
	dtboImage android.Path
}

// KernelBuildFactory makes a kernel_build module: it builds a kernel with kbuild in a kept object
// tree and, with dt_images, dtb.img and dtbo.img.
func KernelBuildFactory() android.Module {
	return newKernelBuild(nil, sourceDirSteps{})
}

// kernelBuildFactoryWithNames gives the factory of kernel_build modules whose messages use names for
// their properties and next steps for a directory outside their source directories, for code that
// creates a module from settings of another form. Soong makes each variant of a module with its
// factory and copies only the properties, so the names have to come from the factory.
func kernelBuildFactoryWithNames(names settingNames, steps sourceDirSteps) android.ModuleFactory {
	return func() android.Module { return newKernelBuild(names, steps) }
}

func newKernelBuild(names settingNames, steps sourceDirSteps) *kernelBuild {
	module := &kernelBuild{names: names, sourceDirSteps: steps}
	module.AddProperties(&module.properties)
	android.InitAndroidArchModule(module, android.DeviceSupported, android.MultilibCommon)
	return module
}

// dependencyTag marks the dependencies of the kernel module types that are not host tools.
type dependencyTag struct {
	blueprint.BaseDependencyTag
	name string
}

var libcryptoTag = dependencyTag{name: "libcrypto"}

func (k *kernelBuild) externalModulesSet() bool {
	return k.properties.External_modules.Root != nil || k.properties.External_modules.Dirs != nil
}

// externalModulesRoot is external_modules.root, empty when external_modules is not set.
func (k *kernelBuild) externalModulesRoot() string {
	return proptools.String(k.properties.External_modules.Root)
}

// dtImagesSet reports whether dt_images is set, which is when it names merge or dtbo_page_size.
func (p *kernelBuildProperties) dtImagesSet() bool {
	return p.Dt_images.Merge != nil || p.Dt_images.Dtbo_page_size != nil
}

func (k *kernelBuild) dtImagesSet() bool { return k.properties.dtImagesSet() }

func (k *kernelBuild) qcomMerge() bool {
	return k.dtImagesSet() && proptools.String(k.properties.Dt_images.Merge) == mergeQcom
}

// missingQcomTools names the programs of qcomDtTools that no module defines.
func missingQcomTools(ctx interface{ OtherModuleExists(string) bool }) []string {
	var missing []string
	for _, tool := range qcomDtTools {
		if !ctx.OtherModuleExists(tool) {
			missing = append(missing, tool)
		}
	}
	return missing
}

func (k *kernelBuild) DepsMutator(ctx android.BottomUpMutatorContext) {
	tools := []string{"dtc", "depmod", "kbuild_input_check", "kernel_output_files", "soong_zip"}
	if k.dtImagesSet() {
		tools = append(tools, "mkdtboimg", "zipsync")
		if k.qcomMerge() {
			tools = append(tools, "merge_dtbs")
			// A direct dependency on a name that no module defines is an analysis error, which
			// stops soong_build for every product, so the device tree programs are added only
			// when all of them exist. Otherwise the module reports them (missingTools).
			if len(missingQcomTools(ctx)) == 0 {
				tools = append(tools, qcomDtTools...)
			}
		}
	}
	ctx.AddHostToolDependencies(tools...)
	ctx.AddFarVariationDependencies(append(ctx.Config().BuildOSTarget.Variations(),
		blueprint.Variation{Mutator: "link", Variation: "shared"}), libcryptoTag, "libcrypto")
}

// isCleanRelative reports a non-empty relative path that equals its cleaned form, is not `.` and
// has no `..` component.
func isCleanRelative(p string) bool {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p || p == "." {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// isSingleComponent reports a non-empty path component other than `.` and `..`.
func isSingleComponent(p string) bool {
	return p != "" && !strings.Contains(p, "/") && p != "." && p != ".."
}

// overlaps reports whether one clean relative path equals, contains or lies inside the other,
// compared by whole path components.
func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// The kernel's names of the architectures of the devices Soong builds for.
var validArches = []string{"arm", "arm64", "riscv", "x86"}

// kernelArches gives, for the device's primary architecture, the kernel architectures a named
// module may build, the kernel architecture of that device architecture first: arm takes arm or
// arm64, a 32-bit user space on a 64-bit kernel.
var kernelArches = map[android.ArchType][]string{
	android.Arm64:   {"arm64"},
	android.Arm:     {"arm", "arm64"},
	android.X86:     {"x86"},
	android.X86_64:  {"x86"},
	android.Riscv64: {"riscv"},
}

// checkPathList checks a property that lists paths: each entry has to be a clean relative path
// and may be named once. further runs for every entry that passes both checks, with the entry's
// index, for the checks of one property that go beyond these two.
func (k *kernelBuild) checkPathList(ctx android.ModuleContext, prop kernelProperty, list []string, further func(i int, p string)) {
	seen := make(map[string]bool)
	for i, p := range list {
		switch {
		case !isCleanRelative(p):
			k.errorf(ctx, prop, "%q is not a clean relative path. Write the entry without ., .. and extra slashes.", p)
		case seen[p]:
			k.errorf(ctx, prop, "%q is listed twice. Remove the second entry from %s.", p, k.name(prop))
		case further != nil:
			further(i, p)
		}
		seen[p] = true
	}
}

// checkProperties reports every malformed or missing property value as an analysis error naming
// the property, for every kernel_build module.
func (k *kernelBuild) checkProperties(ctx android.ModuleContext) {
	p := &k.properties
	kernelDir := proptools.String(p.Kernel_dir)
	setKernelDir := fmt.Sprintf("Set %s to the kernel source directory, relative to the top of the source tree.", k.name(propKernelDir))
	if p.Kernel_dir == nil {
		k.errorf(ctx, propKernelDir, "is required. %s", setKernelDir)
	} else if !isCleanRelative(kernelDir) {
		k.errorf(ctx, propKernelDir, "%q is not a clean relative path. %s", kernelDir, setKernelDir)
	}
	if arch := proptools.String(p.Kernel_arch); p.Kernel_arch == nil {
		k.errorf(ctx, propKernelArch, "is required. Set %s to one of %s.", k.name(propKernelArch), strings.Join(validArches, ", "))
	} else if !android.InList(arch, validArches) {
		k.errorf(ctx, propKernelArch, "%q is none of %s. Set %s to one of them.", arch, strings.Join(validArches, ", "), k.name(propKernelArch))
	}
	setImage := fmt.Sprintf("Set %s to the image target of the kernel, such as Image.", k.name(propImage))
	if p.Image == nil {
		k.errorf(ctx, propImage, "is required. %s", setImage)
	} else if !isSingleComponent(*p.Image) {
		k.errorf(ctx, propImage, "%q is not a single path component. %s", *p.Image, setImage)
	}
	setDefconfig := fmt.Sprintf("Set %s to the base configuration in arch/<arch>/configs of the kernel source.", k.name(propDefconfig))
	if p.Defconfig == nil {
		k.errorf(ctx, propDefconfig, "is required. %s", setDefconfig)
	} else if !isCleanRelative(*p.Defconfig) {
		k.errorf(ctx, propDefconfig, "%q is not a clean relative path. %s", *p.Defconfig, setDefconfig)
	}
	k.checkPathList(ctx, propConfigFragments, p.Config_fragments, nil)
	if d := p.Clang_dir; d != nil && !(isCleanRelative(*d) && strings.HasPrefix(*d, clangRoot+"/")) {
		k.errorf(ctx, propClangDir, "%q is not a directory below %s. "+
			"Set %s to a clang directory below %s, or remove it to use the platform clang.", *d, clangRoot, k.name(propClangDir), clangRoot)
	}

	root := k.externalModulesRoot()
	if k.externalModulesSet() {
		setRoot := fmt.Sprintf("Set %s to the external module tree, relative to the top of the source tree.", k.name(propExternalModulesRoot))
		if p.External_modules.Root == nil {
			k.errorf(ctx, propExternalModulesRoot, "is required when %s is set. %s", k.name(propExternalModules), setRoot)
		} else if !isCleanRelative(root) {
			k.errorf(ctx, propExternalModulesRoot, "%q is not a clean relative path. %s", root, setRoot)
		} else if isCleanRelative(kernelDir) && overlaps(root, kernelDir) {
			k.errorf(ctx, propExternalModulesRoot, "%q is, contains or lies inside %s %q. Set %s to a directory outside %s.",
				root, k.name(propKernelDir), kernelDir, k.name(propExternalModulesRoot), k.name(propKernelDir))
		}
		if len(p.External_modules.Dirs) == 0 {
			k.errorf(ctx, propExternalModulesDirs, "must not be empty when %s is set. List the module directories in %s, or remove %s.",
				k.name(propExternalModules), k.name(propExternalModulesDirs), k.name(propExternalModules))
		}
		k.checkPathList(ctx, propExternalModulesDirs, p.External_modules.Dirs, nil)
	}

	k.checkPathList(ctx, propExtraSourceDirs, p.Extra_source_dirs, func(i int, e string) {
		switch {
		case isCleanRelative(kernelDir) && overlaps(e, kernelDir):
			k.errorf(ctx, propExtraSourceDirs, "%q is, contains or lies inside %s %q. %s",
				e, k.name(propKernelDir), kernelDir, k.removeSourceDirStep())
		case isCleanRelative(root) && overlaps(e, root):
			k.errorf(ctx, propExtraSourceDirs, "%q is, contains or lies inside %s %q. %s",
				e, k.name(propExternalModulesRoot), root, k.removeSourceDirStep())
		default:
			for _, other := range p.Extra_source_dirs[:i] {
				if other != e && isCleanRelative(other) && overlaps(e, other) {
					inner, outer := e, other
					if strings.HasPrefix(other, e+"/") {
						inner, outer = other, e
					}
					k.errorf(ctx, propExtraSourceDirs, "%q lies inside %q. %s", inner, outer, k.removeSourceDirStep())
				}
			}
		}
	})

	renamed := make(map[string]bool)
	for _, r := range p.Module_renames {
		from, to, ok := strings.Cut(r, ":")
		if !ok || !isModuleName(from) || !isModuleName(to) {
			k.errorf(ctx, propModuleRenames, "%q is not <name>.ko:<name>.ko. Write the entry as <from>.ko:<to>.ko.", r)
			continue
		}
		if renamed[from] {
			k.errorf(ctx, propModuleRenames, "%q is renamed twice. Remove one of its two entries from %s.", from, k.name(propModuleRenames))
		}
		renamed[from] = true
	}

	if k.dtImagesSet() {
		setMerge := fmt.Sprintf("Set %s to %q or %q.", k.name(propDtImagesMerge), mergeQcom, mergeNone)
		if merge := proptools.String(p.Dt_images.Merge); p.Dt_images.Merge == nil {
			k.errorf(ctx, propDtImagesMerge, "is required when %s is set. %s", k.name(propDtImages), setMerge)
		} else if merge != mergeQcom && merge != mergeNone {
			k.errorf(ctx, propDtImagesMerge, "%q is neither %q nor %q. %s", merge, mergeQcom, mergeNone, setMerge)
		}
		setPageSize := fmt.Sprintf("Set %s to the page size of the device in bytes, such as 4096.", k.name(propDtboPageSize))
		if p.Dt_images.Dtbo_page_size == nil {
			k.errorf(ctx, propDtboPageSize, "is required when %s is set. %s", k.name(propDtImages), setPageSize)
		} else if *p.Dt_images.Dtbo_page_size <= 0 {
			k.errorf(ctx, propDtboPageSize, "%d is not a positive integer. %s", *p.Dt_images.Dtbo_page_size, setPageSize)
		}
	}
}

// isModuleName reports a module file name as module_renames takes it: a single path component
// ending in .ko, without the `:` that separates the two names of an entry.
func isModuleName(n string) bool {
	return strings.HasSuffix(n, ".ko") && len(n) > len(".ko") && isSingleComponent(n) && !strings.Contains(n, ":")
}

// clangDir is the clang directory, relative to the tree top: clang_dir, or the one cc/config's
// ClangPath resolves for the platform. A clang_dir that does not exist is left to missingPaths.
func (k *kernelBuild) clangDir(ctx android.PathContext) string {
	if d := proptools.String(k.properties.Clang_dir); d != "" {
		return d
	}
	return config.ClangPath(ctx, "").String()
}

// missingPaths checks, with ExistentPathForSource, whose glob makes Soong analyse again when a path
// appears or disappears, that each path property names something that exists. A path below
// another is checked only when the other exists.
func (k *kernelBuild) missingPaths(ctx android.ModuleContext) []string {
	p := &k.properties
	var missing []string
	// exists reports a path that does not exist with the property that gives it and the next step.
	exists := func(prop kernelProperty, rel, nextStep string) bool {
		if !android.ExistentPathForSource(ctx, rel).Valid() {
			missing = append(missing, fmt.Sprintf("%s: %s does not exist. %s", k.name(prop), rel, nextStep))
			return false
		}
		return true
	}
	kernelDir := proptools.String(p.Kernel_dir)
	if exists(propKernelDir, kernelDir, fmt.Sprintf("Sync the kernel repository to that path, or correct %s.", k.name(propKernelDir))) {
		configs := path.Join(kernelDir, "arch", proptools.String(p.Kernel_arch), "configs")
		exists(propDefconfig, path.Join(configs, proptools.String(p.Defconfig)),
			fmt.Sprintf("Correct %s, or check %s.", k.name(propDefconfig), k.name(propKernelArch)))
		for _, f := range p.Config_fragments {
			exists(propConfigFragments, path.Join(configs, f),
				fmt.Sprintf("Add the fragment to the kernel source, or correct %s.", k.name(propConfigFragments)))
		}
	}
	for _, e := range p.Extra_source_dirs {
		exists(propExtraSourceDirs, e, fmt.Sprintf("Sync the repository to that path, or correct %s.", k.name(propExtraSourceDirs)))
	}
	if k.externalModulesSet() {
		root := k.externalModulesRoot()
		if exists(propExternalModulesRoot, root,
			fmt.Sprintf("Sync the module repository to that path, or correct %s.", k.name(propExternalModulesRoot))) {
			for _, d := range p.External_modules.Dirs {
				exists(propExternalModulesDirs, path.Join(root, d),
					fmt.Sprintf("Sync the module repository, or correct %s.", k.name(propExternalModulesDirs)))
			}
		}
	}
	if d := proptools.String(p.Clang_dir); d != "" {
		exists(propClangDir, d, fmt.Sprintf("Add the clang directory at that path, correct %s, or remove %s to use the platform clang.",
			k.name(propClangDir), k.name(propClangDir)))
	}
	return missing
}

// missingTools names the parts of the tree the kbuild rule needs besides the paths of its
// properties: the perl of prebuilts/tools-lineage and, for dt_images `merge: "qcom"`, the device
// tree programs of the external/dtc fork.
func (k *kernelBuild) missingTools(ctx android.ModuleContext) []string {
	var missing []string
	for _, rel := range []string{perlPath, perlBase} {
		if !android.ExistentPathForSource(ctx, rel).Valid() {
			missing = append(missing, fmt.Sprintf("%s does not exist: prebuilts/tools-lineage is not in the tree. Sync prebuilts/tools-lineage.", rel))
		}
	}
	if k.qcomMerge() {
		if tools := missingQcomTools(ctx); len(tools) > 0 {
			missing = append(missing, fmt.Sprintf("dt_images: merge: %q needs the host tools %s, which no module defines; "+
				"they come from the external/dtc fork of android_external_dtc. Sync external/dtc from that fork.", mergeQcom, strings.Join(tools, ", ")))
		}
	}
	return missing
}

// qualifiedName is a module's name as BOARD_KERNEL_BUILD_MODULE writes it: //<namespace>:<name>
// for the namespace path of a module outside the root namespace, or :<name> in the root namespace.
func qualifiedName(namespacePath, name string) string {
	if namespacePath != "." {
		return "//" + namespacePath + ":" + name
	}
	return ":" + name
}

// moduleRefPattern is the one condition for the two forms of a module reference, //<path>:<name>
// and :<name>: a path of at least one character without `:` after `//`, or nothing, then `:` and a
// name of at least one character without `:` or `/`.
var moduleRefPattern = regexp.MustCompile(`^(//[^:]+)?:([^:/]+)$`)

// parseModuleRef gives the name a dependency on a module reference is added with: the bare name
// for :<name>, the reference itself for //<path>:<name>, and false for any other text.
func parseModuleRef(ref string) (string, bool) {
	m := moduleRefPattern.FindStringSubmatch(ref)
	if m == nil {
		return "", false
	}
	if m[1] == "" {
		return m[2], true
	}
	return ref, true
}

// kernelBuildMessage is the message of a kernel_build module's problems: `kernel_build <name>: `
// and the problems, each a sentence with its next step, joined by spaces.
func kernelBuildMessage(name string, problems []string) string {
	return "kernel_build " + name + ": " + strings.Join(problems, " ")
}

// boardKernelModuleIs reports whether BOARD_KERNEL_BUILD_MODULE refers to the module with the name in
// the namespace with the path.
func boardKernelModuleIs(cfg android.Config, namespacePath, name string) bool {
	v := cfg.BoardKernelBuildModule()
	return v != "" && v == qualifiedName(namespacePath, name)
}

// copyFileEntry is one PRODUCT_COPY_FILES entry with its destination, the entry's second
// `:`-separated field, relative to the product out directory.
type copyFileEntry struct {
	entry, dest string
}

// productCopyFiles gives the PRODUCT_COPY_FILES entries of the product in their order, leaving out
// an entry without a destination.
func productCopyFiles(cfg android.Config) []copyFileEntry {
	var entries []copyFileEntry
	for _, entry := range cfg.ProductVariables().PartitionVarsForSoongMigrationOnlyDoNotUse.ProductCopyFiles {
		if parts := strings.Split(entry, ":"); len(parts) >= 2 {
			entries = append(entries, copyFileEntry{entry: entry, dest: parts[1]})
		}
	}
	return entries
}

// registerGoalPhonies registers the names that `m <name>` builds when Kati runs, where Soong's own
// targets of the module carry the suffix -soong and a module without a Make entry has no target of
// its bare name: <namespace>-<name> outside the root namespace, and <name> when the module's
// namespace is exported to Make.
func registerGoalPhonies(ctx android.ModuleContext, exportedToMake bool, files android.Paths) {
	if !ctx.Config().KatiEnabled() {
		return
	}
	if ns := ctx.Namespace().Path; ns != "." {
		ctx.Phony(strings.ReplaceAll(ns, "/", ".")+"-"+ctx.ModuleName(), files...)
	}
	if exportedToMake {
		ctx.Phony(ctx.ModuleName(), files...)
	}
}

// kernelOutputs are the files the module offers.
type kernelOutputs struct {
	image, systemMap, modules, dtbs, dtb, dtbo android.WritablePath
}

func (o kernelOutputs) all() android.WritablePaths {
	paths := android.WritablePaths{o.image, o.systemMap, o.modules}
	for _, p := range []android.WritablePath{o.dtbs, o.dtb, o.dtbo} {
		if p != nil {
			paths = append(paths, p)
		}
	}
	return paths
}

func (k *kernelBuild) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	k.checkProperties(ctx)
	if ctx.Failed() {
		return
	}
	named := boardKernelModuleIs(ctx.Config(), ctx.Namespace().Path, ctx.ModuleName())

	outs := kernelOutputs{
		image:     android.PathForModuleOut(ctx, proptools.String(k.properties.Image)),
		systemMap: android.PathForModuleOut(ctx, "System.map"),
		modules:   android.PathForModuleOut(ctx, "modules.zip"),
	}
	if k.dtImagesSet() {
		outs.dtbs = android.PathForModuleOut(ctx, "dtbs.zip")
		outs.dtb = android.PathForModuleOut(ctx, "dt_images", "dtb.img")
		outs.dtbo = android.PathForModuleOut(ctx, "dt_images", "dtbo.img")
	}

	// A missing path is an analysis error only for the module BOARD_KERNEL_BUILD_MODULE names:
	// soong_build stops on any analysis error, and a device tree whose kernel repositories are
	// not synced must leave every other product buildable. Any other module fails when it is built.
	// kernelBuildMessage builds both messages: the provider's, which the header module reads, from
	// the missing paths, and the ErrorRules' from the missing paths and tools.
	missingPaths := k.missingPaths(ctx)
	missingPathsMessage := ""
	if len(missingPaths) > 0 {
		missingPathsMessage = kernelBuildMessage(ctx.ModuleName(), missingPaths)
	}
	setKernelSourcesInfo(ctx, k, missingPathsMessage)
	missing := append(append([]string{}, missingPaths...), k.missingTools(ctx)...)
	if len(missing) > 0 {
		if named {
			for _, m := range missing {
				ctx.ModuleErrorf("%s", m)
			}
			return
		}
		msg := kernelBuildMessage(ctx.ModuleName(), missing)
		for _, out := range outs.all() {
			android.ErrorRule(ctx, out, msg)
		}
	} else {
		k.buildRules(ctx, outs)
	}

	ctx.SetOutputFiles(android.Paths{outs.image}, "")
	ctx.SetOutputFiles(android.Paths{outs.image}, tagKernel)
	ctx.SetOutputFiles(android.Paths{outs.systemMap}, tagSystemMap)
	ctx.SetOutputFiles(android.Paths{outs.modules}, tagModules)
	if k.dtImagesSet() {
		ctx.SetOutputFiles(android.Paths{outs.dtbs}, tagDtbs)
		ctx.SetOutputFiles(android.Paths{outs.dtb}, tagDtb)
		ctx.SetOutputFiles(android.Paths{outs.dtbo}, tagDtbo)
	}
	// The outputs are no checkbuild files: `m checkbuild` would otherwise build every kernel of
	// the exported namespaces and fail on any that carries an ErrorRule for a missing path.
	phonyFiles := outs.all().Paths()
	ctx.ModulePhonyFiles(phonyFiles...)
	registerGoalPhonies(ctx, k.ExportedToMake(), phonyFiles)

	if named {
		k.installNamed(ctx, outs)
	}
}

// installNamed installs the kernel and dtb.img of the module that BOARD_KERNEL_BUILD_MODULE names
// and checks the board against it.
func (k *kernelBuild) installNamed(ctx android.ModuleContext, outs kernelOutputs) {
	vars := ctx.Config().ProductVariables().PartitionVarsForSoongMigrationOnlyDoNotUse
	if !k.ExportedToMake() {
		ctx.ModuleErrorf("BOARD_KERNEL_BUILD_MODULE is set to this module, but its namespace %q is not in PRODUCT_SOONG_NAMESPACES, "+
			"so Make gets no installed kernel. Add %q to PRODUCT_SOONG_NAMESPACES.", ctx.Namespace().Path, ctx.Namespace().Path)
	}

	arch := proptools.String(k.properties.Kernel_arch)
	device := ctx.Config().DevicePrimaryArchType()
	if !android.InList(arch, kernelArches[device]) {
		k.errorf(ctx, propKernelArch, "%q does not fit the device's primary architecture %s. Set %s to %s.",
			arch, device, k.name(propKernelArch), strings.Join(kernelArches[device], " or "))
	}

	includeDtb := vars.BoardIncludeDtbInBootimg
	prebuiltDtbo := vars.BoardPrebuiltDtboImage
	if k.dtImagesSet() {
		if vars.BoardDtboPartitionSize == "" {
			k.errorf(ctx, propDtImages, "is set, but the board sets no BOARD_DTBOIMG_PARTITION_SIZE. "+
				"Set BOARD_DTBOIMG_PARTITION_SIZE to the size of the dtbo partition.")
		}
		if prebuiltDtbo != "" {
			ctx.ModuleErrorf("BOARD_KERNEL_BUILD_MODULE is set to this module, and BOARD_PREBUILT_DTBOIMAGE (%s) makes a second dtbo.img. "+
				"Remove BOARD_PREBUILT_DTBOIMAGE.", prebuiltDtbo)
		}
	} else {
		if includeDtb {
			k.errorf(ctx, propDtImages, "is not set, but the board sets BOARD_INCLUDE_DTB_IN_BOOTIMG, which needs dtb.img. "+
				"Set %s, or remove BOARD_INCLUDE_DTB_IN_BOOTIMG.", k.name(propDtImages))
		}
		switch {
		case vars.BoardDtboPartitionSize != "" && prebuiltDtbo != "":
			k.errorf(ctx, propDtImages, "is not set, and the board sets BOARD_PREBUILT_DTBOIMAGE (%s), but the dtbo partition "+
				"gets dtbo.img only from this module. Remove BOARD_PREBUILT_DTBOIMAGE, and set %s.", prebuiltDtbo, k.name(propDtImages))
		case vars.BoardDtboPartitionSize != "":
			k.errorf(ctx, propDtImages, "is not set, but the board sets BOARD_DTBOIMG_PARTITION_SIZE, which needs dtbo.img. "+
				"Set %s, or remove BOARD_DTBOIMG_PARTITION_SIZE.", k.name(propDtImages))
		}
	}

	if binaries := ctx.Config().ProductVariables().BoardKernelBinaries; len(binaries) > 0 {
		ctx.ModuleErrorf("BOARD_KERNEL_BUILD_MODULE is set to this module, and BOARD_KERNEL_BINARIES (%s) lists other kernel files. "+
			"Remove BOARD_KERNEL_BINARIES.", strings.Join(binaries, " "))
	}
	installsDtb := k.dtImagesSet() && includeDtb
	for _, c := range productCopyFiles(ctx.Config()) {
		if c.dest == "kernel" || (installsDtb && c.dest == "dtb.img") {
			ctx.ModuleErrorf("BOARD_KERNEL_BUILD_MODULE is set to this module, and the PRODUCT_COPY_FILES entry %q copies a second %s. "+
				"Remove the entry.", c.entry, c.dest)
		}
	}
	if installsDtb && vars.BoardPrebuiltDtbDir != "" {
		ctx.ModuleErrorf("BOARD_KERNEL_BUILD_MODULE is set to this module, and BOARD_PREBUILT_DTBIMAGE_DIR (%s) makes a second dtb.img. "+
			"Remove BOARD_PREBUILT_DTBIMAGE_DIR.", vars.BoardPrebuiltDtbDir)
	}
	if ctx.Failed() {
		return
	}

	dir := android.PathForModuleInPartitionInstall(ctx, "")
	ctx.InstallFileWithoutCheckbuild(dir, "kernel", outs.image)
	if installsDtb {
		ctx.InstallFileWithoutCheckbuild(dir, "dtb.img", outs.dtb)
	}
	if k.dtImagesSet() {
		k.dtboImage = outs.dtbo
	}
}

// MakeVars gives the named module's dtbo.img to Make as BOARD_PREBUILT_DTBOIMAGE, which every dtbo
// site of core/Makefile reads.
func (k *kernelBuild) MakeVars(ctx android.MakeVarsModuleContext) []android.ModuleMakeVarsValue {
	if k.dtboImage == nil {
		return nil
	}
	return []android.ModuleMakeVarsValue{{Name: "BOARD_PREBUILT_DTBOIMAGE", Value: k.dtboImage.String()}}
}

// topLevelEntries lists every entry directly inside dir except .git, names starting with a dot
// included, with two globs, so that an entry that appears or disappears makes Soong analyse again.
// The second glob is needed because Soong's glob leaves out the names that start with a dot unless
// the pattern does, and a .scmversion there changes the kernel's release.
func topLevelEntries(ctx android.ModuleContext, dir string) []string {
	entries, err := ctx.GlobWithDeps(dir+"/*", nil)
	if err != nil {
		ctx.ModuleErrorf("glob of %s: %s", dir, err)
		return nil
	}
	dots, err := ctx.GlobWithDeps(dir+"/.*", []string{dir + "/.git"})
	if err != nil {
		ctx.ModuleErrorf("glob of %s: %s", dir, err)
		return nil
	}
	var all []string
	for _, e := range append(entries, dots...) {
		e = strings.TrimSuffix(e, "/")
		if base := path.Base(e); base == "." || base == ".." || base == ".git" {
			continue
		}
		all = append(all, e)
	}
	all = android.FirstUniqueStrings(all)
	sort.Strings(all)
	return all
}

// relTo gives target relative to base, both relative to the tree top.
func relTo(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}
