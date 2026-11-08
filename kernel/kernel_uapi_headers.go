// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"fmt"
	"strings"

	"android/soong/android"

	"github.com/google/blueprint"
	"github.com/google/blueprint/proptools"
)

type kernelUapiHeadersProperties struct {
	// The kernel_build module whose kernel's user-space headers are exported, `:<name>` or
	// `//<namespace>:<name>`. Without it, the module that BOARD_KERNEL_BUILD_MODULE names.
	Kernel_build *string
}

type kernelUapiHeaders struct {
	android.ModuleBase

	properties kernelUapiHeadersProperties

	stamp      android.OutputPath
	headerDirs android.Paths

	// ref is the module reference used, and fromBoard reports that it comes from
	// BOARD_KERNEL_BUILD_MODULE.
	ref       string
	fromBoard bool
}

// KernelUapiHeadersInfo contains the directory of the installed headers, the stamp that shows a
// change in them and the configuration of the kernel they come from.
// @auto-generate: gob
type KernelUapiHeadersInfo struct {
	// Usr is the directory that contains every header installed below usr.
	Usr android.OutputPath
	// Stamp gets a new modification time whenever a header below Usr changed.
	Stamp android.OutputPath
	// Config is the configuration used to build the kernel, the output ConfigTag of the
	// kernel_build module that builds it.
	Config android.Path
}

var KernelUapiHeadersInfoProvider = blueprint.NewProvider[KernelUapiHeadersInfo]()

// KernelUapiHeadersFactory makes a kernel_uapi_headers module: it exports the user-space headers
// of a kernel_build module's kernel, which headers_install writes, as generated headers.
func KernelUapiHeadersFactory() android.Module {
	module := &kernelUapiHeaders{}
	module.AddProperties(&module.properties)
	android.InitAndroidModule(module)
	return module
}

// kernelBuildDepTag is the dependency on the kernel_build module. It allows a disabled module, so
// that the header module reports it itself, naming BOARD_KERNEL_BUILD_MODULE, where Soong's own
// check would only name the module.
type kernelBuildDepTag struct {
	blueprint.BaseDependencyTag
	android.AlwaysAllowDisabledModuleDependencyTag
}

var kernelBuildTag = kernelBuildDepTag{}

// qualifiedNameNextStep is the next step for a module reference that leads to no enabled kernel_build
// module.
const qualifiedNameNextStep = "Correct it to the qualified name of a kernel_build module, //<path>:<name> or :<name>."

// refError reports a problem with the module reference, naming where it comes from.
func (h *kernelUapiHeaders) refError(ctx android.BaseModuleContext, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if h.fromBoard {
		ctx.ModuleErrorf("BOARD_KERNEL_BUILD_MODULE %q: %s", h.ref, msg)
	} else {
		ctx.PropertyErrorf("kernel_build", "%q: %s", h.ref, msg)
	}
}

// setRef takes the module reference from the kernel_build property or, without it, from
// BOARD_KERNEL_BUILD_MODULE.
func (h *kernelUapiHeaders) setRef(ctx android.BaseModuleContext) {
	h.ref = proptools.String(h.properties.Kernel_build)
	h.fromBoard = h.properties.Kernel_build == nil
	if h.fromBoard {
		h.ref = ctx.Config().BoardKernelBuildModule()
	}
}

func (h *kernelUapiHeaders) DepsMutator(ctx android.BottomUpMutatorContext) {
	h.setRef(ctx)
	ctx.AddHostToolDependencies("kbuild_input_check")
	if h.ref == "" {
		return
	}
	name, ok := parseModuleRef(h.ref)
	if !ok {
		h.refError(ctx, "is not of the form //<path>:<name> or :<name>. %s", qualifiedNameNextStep)
		return
	}
	// Checked here, so the message names the reference and not Soong's missing dependency.
	if !ctx.OtherModuleExists(name) {
		h.refError(ctx, "leads to no module. %s", qualifiedNameNextStep)
		return
	}
	// Every kernel_build module has the one variant os android, arch common; a module without it
	// is of another type, and a dependency on it would fail with Soong's missing variant error.
	variations := ctx.Config().AndroidCommonTarget.Variations()
	if !ctx.OtherModuleDependencyVariantExists(variations, name) {
		h.refError(ctx, "leads to a module that is not a kernel_build module. %s", qualifiedNameNextStep)
		return
	}
	ctx.AddVariationDependencies(variations, kernelBuildTag, name)
}

func (h *kernelUapiHeaders) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	h.setRef(ctx)
	modOut := android.PathForModuleOut(ctx)
	usr := modOut.Join(ctx, "gen", "usr")
	h.stamp = modOut.Join(ctx, "gen", "headers.stamp")
	h.headerDirs = android.Paths{usr.Join(ctx, "include")}
	// The stamp is no checkbuild file, so `m checkbuild` reaches no ErrorRule of the header rule.
	ctx.ModulePhonyFiles(h.stamp)
	registerGoalPhonies(ctx, h.ExportedToMake(), android.Paths{h.stamp})

	if h.ref == "" {
		// Without a kernel, the configuration is an output of this module that fails like the stamp.
		config := modOut.Join(ctx, "config")
		msg := "no kernel module is configured: BOARD_KERNEL_BUILD_MODULE is not set, so " + ctx.ModuleName() +
			" cannot export the headers and the configuration of a kernel. Set TARGET_KERNEL_SOURCE and TARGET_KERNEL_CONFIG in BoardConfig.mk, " +
			"or write a kernel_build module and set BOARD_KERNEL_BUILD_MODULE to it."
		android.ErrorRule(ctx, h.stamp, msg)
		android.ErrorRule(ctx, config, msg)
		android.SetProvider(ctx, KernelUapiHeadersInfoProvider, KernelUapiHeadersInfo{Usr: usr, Stamp: h.stamp, Config: config})
		return
	}
	var info *KernelSourcesInfo
	ctx.VisitDirectDepsProxyAllowDisabled(func(m android.ModuleProxy) {
		if ctx.OtherModuleDependencyTag(m) != kernelBuildTag {
			return
		}
		if !android.OtherModulePointerProviderOrDefault(ctx, m, android.CommonModuleInfoProvider).Enabled {
			h.refError(ctx, "leads to the disabled module %s. Enable the module, or correct the reference to another kernel_build module.",
				ctx.OtherModuleName(m))
			return
		}
		// A kernel_build module is known by its provider, since one that a load hook creates has
		// another type name. Every kernel_build module whose properties are valid sets it, and one
		// whose properties are not stops soong_build before this module runs.
		i, ok := android.OtherModuleProvider(ctx, m, KernelSourcesInfoProvider)
		if !ok {
			h.refError(ctx, "leads to %s, a %s module, not a kernel_build module. %s", ctx.OtherModuleName(m), ctx.OtherModuleType(m),
				qualifiedNameNextStep)
			return
		}
		qualified := qualifiedName(ctx.OtherModuleNamespace(m).Path, ctx.OtherModuleName(m))
		if h.fromBoard && qualified != h.ref {
			h.refError(ctx, "leads to the kernel_build module by another path than its qualified name %s. Write it as %s.", qualified, qualified)
			return
		}
		info = &i
		android.SetProvider(ctx, KernelUapiHeadersInfoProvider,
			KernelUapiHeadersInfo{Usr: usr, Stamp: h.stamp, Config: android.OutputFileForModule(ctx, m, ConfigTag)})
	})
	if info == nil {
		return
	}
	if info.MissingPaths != "" {
		android.ErrorRule(ctx, h.stamp, info.MissingPaths)
		return
	}
	h.buildHeaderRule(ctx, modOut.String(), info)
}

// buildHeaderRule writes the rule that installs the kernel's user-space headers into a staging
// directory, checks where they come from, cleans the two signal headers for bionic and copies the
// result into the export directory, touching the stamp only when a header changed.
func (h *kernelUapiHeaders) buildHeaderRule(ctx android.ModuleContext, modOut string, info *KernelSourcesInfo) {
	dirs := kernelSourceDirs(info.KernelDir, info.ExtraSourceDirs, info.ExternalModulesRoot)
	entries := sourceDirEntries(ctx, dirs)
	var allow []string
	for _, d := range dirs {
		allow = append(allow, "-allow "+treeWord(d))
	}
	cleaner := android.PathForSource(ctx, "build/custom/kernel/tools/clean_headers.sh")

	var b strings.Builder
	w := func(format string, args ...interface{}) { fmt.Fprintf(&b, format, args...) }
	w("KIC=%s\n", treeWord(ctx.Config().HostToolPath(ctx, "kbuild_input_check").String()))
	w("K=%s\n", treeWord(info.KernelDir))
	w("STAMP=%s\n", treeWord(h.stamp.String()))
	w("MAKE=\"$T\"/%s/make\n", buildToolsBin)
	w("SYSROOT=\"$T\"/%s\n", glibcSysroot)
	// 1. An empty staging directory.
	b.WriteString(`{ rm -rf "$MOD"/staging && mkdir -p "$MOD"/staging; } || exit $?
`)
	w("J=$(%s) || exit $?\n", jobCountCommand)
	// 2. headers_install, then the check of where each installed header comes from.
	w("\"$MAKE\" -j\"$J\" -C \"$K\" O=\"$MOD\"/staging ARCH=%s LLVM=1 LLVM_IAS=1 %s PAHOLE="+
		" headers_install || exit $?\n", shq(info.KernelArch), hostMakeVars)
	w("\"$KIC\" headers -make \"$MAKE\" -staging \"$MOD\"/staging -next-step %s %s || exit $?\n",
		shq(info.AddSourceDirStep), strings.Join(allow, " "))
	// 3. The cleaner, run from the tree top.
	w("%s \"$MOD\"/staging || exit $?\n", shq(cleaner.String()))
	// 4. The export: gen.changed is created before any file of the export directory changes. The
	// copy contains every header installed below usr.
	b.WriteString(`mkdir -p "$MOD"/gen/usr || exit $?
changes=$(rsync -rcn --delete --itemize-changes "$MOD"/staging/usr/ "$MOD"/gen/usr/) || exit $?
if [ -n "$changes" ]; then : > "$MOD"/gen.changed || exit $?; fi
rsync -rc --delete "$MOD"/staging/usr/ "$MOD"/gen/usr/ || exit $?
`)
	// 5. The symbolic links of the sources may not lead outside the walked directories.
	w("\"$KIC\" links -next-step %s %s %s || exit $?\n", shq(info.AddSourceDirStep), strings.Join(allow, " "),
		words(quoteAll(entries)...))
	// 6. The depfile, then the check for inputs changed during the run.
	// clang-stable is an input because bionic's header cleaner loads libclang from it.
	deps := append(quoteAll(entries), "bionic/libc/kernel/tools", clangBase+"/clang-stable", shq(info.ClangDir),
		buildToolsBin, buildToolsPath, glibcSysroot)
	w("set -- %s\n", words(deps...))
	b.WriteString(`write_depfile "$MOD"/gen.d "$STAMP" "$@" || exit $?
check_unchanged "$@" || exit 1
`)
	// 7. The stamp, only when a header changed.
	b.WriteString(`if [ ! -e "$STAMP" ] || [ -e "$MOD"/gen.changed ]; then touch "$STAMP" || exit $?; fi
rm -f "$MOD"/gen.changed
`)

	rule := android.NewRuleBuilder(pctx, ctx).SandboxDisabled().Restat()
	rule.Command().Text("true").BuiltTool("kbuild_input_check")
	shellRule{
		name:     "headers",
		modOut:   modOut,
		clangDir: info.ClangDir,
		body:     b.String(),
	}.build(ctx, rule).
		Implicit(cleaner).
		Implicit(android.PathForSource(ctx, buildToolsBin, "make")).
		ImplicitOutput(h.stamp).
		ImplicitDepFile(android.PathForModuleOut(ctx, "gen.d"))
	rule.Build("kernel_headers", "kernel headers of "+ctx.ModuleName())
}

// The methods of android.SourceFileGenerator and android.GeneratedTriggerDepsProducer. The stamp
// gets a new modification time whenever an exported header changed, and it is the one declared
// output: which headers headers_install writes is known only once it ran.

func (h *kernelUapiHeaders) GeneratedSourceFiles() android.Paths { return nil }

func (h *kernelUapiHeaders) GeneratedHeaderDirs() android.Paths { return h.headerDirs }

func (h *kernelUapiHeaders) GeneratedDeps() android.Paths { return android.Paths{h.stamp} }

func (h *kernelUapiHeaders) GeneratedTriggerDeps() android.Paths { return android.Paths{h.stamp} }

var _ android.SourceFileGenerator = (*kernelUapiHeaders)(nil)
var _ android.GeneratedTriggerDepsProducer = (*kernelUapiHeaders)(nil)
