// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"android/soong/android"

	"github.com/google/blueprint/proptools"
)

// What the kbuild rule and the header rule share.
const (
	// hostMakeVars are the make variables of the host programs kbuild builds and runs. They are
	// compiled and linked against the glibc sysroot of the tree.
	hostMakeVars = `"HOSTCFLAGS=--sysroot=$SYSROOT" "HOSTLDFLAGS=--sysroot=$SYSROOT -fuse-ld=lld --rtlib=compiler-rt"`
	// jobCountCommand prints the number of jobs make runs with.
	jobCountCommand = "getconf _NPROCESSORS_ONLN"
)

// kernelSourceDirs lists the source directories of a kernel: kernel_dir, extra_source_dirs and,
// when set, external_modules.root.
func kernelSourceDirs(kernelDir string, extraSourceDirs []string, externalModulesRoot string) []string {
	dirs := append([]string{kernelDir}, extraSourceDirs...)
	if externalModulesRoot != "" {
		dirs = append(dirs, externalModulesRoot)
	}
	return dirs
}

// sourceDirEntries lists the top-level entries of the source directories, directory after
// directory, each directory's as topLevelEntries gives them.
func sourceDirEntries(ctx android.ModuleContext, dirs []string) []string {
	var entries []string
	for _, d := range dirs {
		entries = append(entries, topLevelEntries(ctx, d)...)
	}
	return entries
}

// hostToolDeps walks the named host tool dependencies and gives, for each tool, its packaging specs,
// which hold its shared libraries and required tools, or, for a tool without packaging specs, the
// installed tool itself as a plain path.
func hostToolDeps(ctx android.ModuleContext, tools []string) (specs []android.PackagingSpec, plain android.Paths) {
	ctx.VisitDirectDepsProxy(func(m android.ModuleProxy) {
		if ctx.OtherModuleDependencyTag(m) != android.HostToolDepTag || !android.InList(ctx.OtherModuleName(m), tools) {
			return
		}
		info := android.GetHostToolInfo(ctx, m)
		if info == nil {
			return
		}
		list := info.TransitivePackagingSpecs.ToList()
		if len(list) == 0 {
			if info.HostToolPath.Valid() {
				plain = append(plain, info.HostToolPath.Path())
			}
			return
		}
		for _, spec := range list {
			if spec.Partition() != "" {
				continue
			}
			specs = append(specs, spec)
		}
	})
	return specs, plain
}

// toolLibraries gives the installed shared libraries among packaging specs, each once: the files a
// host tool loads at run time.
func toolLibraries(specs []android.PackagingSpec) android.Paths {
	var libraries android.Paths
	for _, spec := range specs {
		if strings.HasSuffix(spec.FullInstallPath().String(), ".so") {
			libraries = append(libraries, spec.FullInstallPath())
		}
	}
	return android.FirstUniquePaths(libraries)
}

// libcryptoPath is the output of Soong's host shared variant of libcrypto.
func libcryptoPath(ctx android.ModuleContext) android.Path {
	var lib android.Path
	ctx.VisitDirectDepsProxy(func(m android.ModuleProxy) {
		if ctx.OtherModuleDependencyTag(m) != libcryptoTag {
			return
		}
		if files := android.OutputFilesForModule(ctx, m, ""); len(files) > 0 {
			lib = files[0]
		}
	})
	return lib
}

func (k *kernelBuild) buildRules(ctx android.ModuleContext, outs kernelOutputs) {
	p := &k.properties
	kernelDir := proptools.String(p.Kernel_dir)
	modOut := android.PathForModuleOut(ctx).String()
	clangDir := k.clangDir(ctx)
	gitState := android.PathForModuleOut(ctx, "git_state.txt")

	root := k.externalModulesRoot()
	sourceDirs := kernelSourceDirs(kernelDir, p.Extra_source_dirs, root)
	sourceEntries := sourceDirEntries(ctx, sourceDirs)

	k.buildGitStateRule(ctx, modOut, clangDir, kernelDir, gitState)

	dtc := ctx.Config().HostToolPath(ctx, "dtc")
	depmod := ctx.Config().HostToolPath(ctx, "depmod")
	// kbuild runs dtc and depmod outside a sandbox by the installed paths the rule names below, so it
	// takes from their packaging specs only the shared libraries they load.
	specs, _ := hostToolDeps(ctx, []string{"dtc", "depmod"})
	libraries := toolLibraries(specs)
	libcrypto := libcryptoPath(ctx)
	if libcrypto == nil {
		ctx.ModuleErrorf("no output of the host shared libcrypto. Sync external/boringssl, which defines libcrypto.")
		return
	}

	s := kbuildScript{
		props:         p,
		names:         k.names,
		addSourceDir:  k.addSourceDirStep(),
		externalRoot:  root,
		sourceDirs:    sourceDirs,
		sourceEntries: sourceEntries,
		clangDir:      clangDir,
		dtc:           dtc.String(),
		depmod:        depmod.String(),
		libraries:     libraries.Strings(),
		libcrypto:     libcrypto.String(),
		inputCheck:    ctx.Config().HostToolPath(ctx, "kbuild_input_check").String(),
		outputFiles:   ctx.Config().HostToolPath(ctx, "kernel_output_files").String(),
		soongZip:      ctx.Config().HostToolPath(ctx, "soong_zip").String(),
		gitState:      gitState.String(),
		imageOut:      outs.image.String(),
		systemMapOut:  outs.systemMap.String(),
		modulesOut:    outs.modules.String(),
	}
	if outs.dtbs != nil {
		s.dtbsOut = outs.dtbs.String()
	}

	rule := android.NewRuleBuilder(pctx, ctx).SandboxDisabled().Restat().HighMem()
	// The Go host tools that the rule's own commands run are called by the paths BuiltTool writes
	// and declares, with the install files of their modules; the shell finds them without a PATH
	// lookup. The first command only declares them.
	rule.Command().Text("true").
		BuiltTool("kbuild_input_check").
		BuiltTool("kernel_output_files").
		BuiltTool("soong_zip").
		ImplicitTool(dtc).
		ImplicitTool(depmod).
		ImplicitTools(libraries).
		ImplicitTool(libcrypto)
	cmd := shellRule{
		name:     "kbuild",
		modOut:   modOut,
		toolDir:  true,
		clangDir: clangDir,
		env: []string{
			"BISON_PKGDATADIR=" + treeWord(bisonData),
			"PERL5LIB=" + treeWord(perlBase),
		},
		body: s.body(),
	}.build(ctx, rule)
	cmd.Implicit(gitState).
		Implicit(android.PathForSource(ctx, perlPath)).
		Implicit(android.PathForSource(ctx, clangDir, "bin", "clang")).
		Implicit(android.PathForSource(ctx, buildToolsBin, "make"))
	configs := path.Join(kernelDir, "arch", proptools.String(p.Kernel_arch), "configs")
	cmd.Implicit(android.PathForSource(ctx, configs, proptools.String(p.Defconfig)))
	for _, f := range p.Config_fragments {
		cmd.Implicit(android.PathForSource(ctx, configs, f))
	}
	cmd.ImplicitOutput(outs.image).
		ImplicitOutput(outs.systemMap).
		ImplicitOutput(outs.modules).
		ImplicitDepFile(android.PathForModuleOut(ctx, "kbuild.d"))
	if outs.dtbs != nil {
		cmd.ImplicitOutput(outs.dtbs)
	}
	rule.Build("kbuild", "kbuild "+ctx.ModuleName())

	if k.dtImagesSet() {
		k.buildDtRule(ctx, outs)
	}
}

// buildGitStateRule writes the rule that records the git state the kernel's reported identity comes
// from: the last commit with its time, the description setlocalversion reads and whether the
// checkout is dirty. The file keeps its modification time when the three lines stay the same, so
// a git operation that changes none of them runs this rule and not the kbuild rule.
func (k *kernelBuild) buildGitStateRule(ctx android.ModuleContext, modOut, clangDir, kernelDir string, gitState android.WritablePath) {
	var b strings.Builder
	fmt.Fprintf(&b, "KIC=%s\n", treeWord(ctx.Config().HostToolPath(ctx, "kbuild_input_check").String()))
	fmt.Fprintf(&b, "K=%s\n", treeWord(kernelDir))
	fmt.Fprintf(&b, "OUT=%s\n", treeWord(gitState.String()))
	b.WriteString(`top=$(git -C "$K" rev-parse --show-toplevel 2>&1)
if [ "$(realpath "$top" 2>/dev/null)" != "$(realpath "$K")" ]; then
  die 1 "` + kernelDir + ` is not the top of a git work tree of its own: git rev-parse --show-toplevel printed: $top. ` +
		`Sync the kernel repository to ` + kernelDir + `, or set ` + k.name(propKernelDir) + ` to the top of its git work tree."
fi
line1=$(git -C "$K" log -1 --format='%H %ct' 2>"$TMPDIR/git_log.err")
if [ -z "$line1" ]; then
  die 1 "git log -1 in ` + kernelDir + ` printed no commit: $(cat "$TMPDIR/git_log.err"). Check out a commit in ` + kernelDir + `."
fi
line2=$(git -C "$K" describe --abbrev=12 2>/dev/null)
if { git -C "$K" --no-optional-locks status -uno --porcelain 2>/dev/null || git -C "$K" diff-index --name-only HEAD; } |
    grep -qvE '^(.. )?scripts/package'; then
  line3=dirty
else
  line3=clean
fi
printf '%s\n%s\n%s\n' "$line1" "$line2" "$line3" > "$OUT.tmp" || die $?
stage "$OUT.tmp" "$OUT" || die $?
rel() { case "$1" in "$T"/*) echo "${1#"$T"/}" ;; *) echo "$1" ;; esac; }
gitdir=$(realpath "$(git -C "$K" rev-parse --absolute-git-dir)") || die $?
common=$(realpath "$(git -C "$K" rev-parse --path-format=absolute --git-common-dir)") || die $?
index=$(realpath "$(git -C "$K" rev-parse --path-format=absolute --git-path index)") || die $?
set -- `)
	b.WriteString(words(quoteAll(topLevelEntries(ctx, kernelDir))...))
	b.WriteString(` "$(rel "$gitdir/HEAD")"
if [ -e "$common/packed-refs" ]; then set -- "$@" "$(rel "$common/packed-refs")"; fi
set -- "$@" "$(rel "$common/refs")" "$(rel "$index")"
write_depfile "$MOD/git_state.d" "$OUT" "$@" || die $?
check_unchanged "$@" || die 1
`)

	rule := android.NewRuleBuilder(pctx, ctx).SandboxDisabled().Restat()
	rule.Command().Text("true").BuiltTool("kbuild_input_check")
	shellRule{
		name:     "git_state",
		modOut:   modOut,
		toolDir:  true,
		clangDir: clangDir,
		body:     b.String(),
	}.build(ctx, rule).
		ImplicitOutput(gitState).
		ImplicitDepFile(android.PathForModuleOut(ctx, "git_state.d"))
	rule.Build("git_state", "git state of "+ctx.ModuleName())
}

func quoteAll(list []string) []string {
	quoted := make([]string, len(list))
	for i, s := range list {
		quoted[i] = shq(s)
	}
	return quoted
}

// probeTries is the number of times the kbuild script probes, a tenth of a second apart, whether
// the out directory's file system gives a new file a change time later than the marker's. The
// bound makes the wait ten seconds at a tenth of a second per try, so that a file system that never
// gives a later change time fails the rule instead of holding the rule's lock without end.
const probeTries = 100

// kbuildScript holds what the kbuild rule's inner script names: the module's properties and the
// paths the module resolved for them, relative to the tree top or absolute.
type kbuildScript struct {
	props *kernelBuildProperties
	// names are the names that the module's messages use for its properties, and addSourceDir is
	// the next step for reads outside the source directories.
	names        settingNames
	addSourceDir string
	// externalRoot is external_modules.root, empty when external_modules is not set.
	externalRoot string
	// sourceDirs are the kernel's source directories and sourceEntries their top-level entries.
	sourceDirs, sourceEntries []string
	clangDir                  string
	dtc, depmod, libcrypto    string
	// libraries are the shared libraries dtc and depmod load.
	libraries []string
	// inputCheck, outputFiles and soongZip are the Go host tools kbuild_input_check,
	// kernel_output_files and soong_zip.
	inputCheck, outputFiles, soongZip string
	gitState                          string
	imageOut, systemMapOut            string
	modulesOut, dtbsOut               string
}

// The script reads the module's properties only through these accessors.

func (s *kbuildScript) kernelDir() string { return proptools.String(s.props.Kernel_dir) }

func (s *kbuildScript) arch() string { return proptools.String(s.props.Kernel_arch) }

func (s *kbuildScript) image() string { return proptools.String(s.props.Image) }

func (s *kbuildScript) defconfig() string { return proptools.String(s.props.Defconfig) }

func (s *kbuildScript) extraSourceDirs() []string { return s.props.Extra_source_dirs }

func (s *kbuildScript) configFragments() []string { return s.props.Config_fragments }

func (s *kbuildScript) externalDirs() []string { return s.props.External_modules.Dirs }

func (s *kbuildScript) moduleRenames() []string { return s.props.Module_renames }

// dtImages reports whether the kernel makes device tree files: dt_images is set, by the same test
// as the module's.
func (s *kbuildScript) dtImages() bool { return s.props.dtImagesSet() }

// dts is the bash word of the object tree's device tree directory.
func (s *kbuildScript) dts() string { return "\"$OBJ\"/arch/" + shq(s.arch()) + "/boot/dts" }

// body is the kbuild rule's inner script after the lock is held: the phases in order.
func (s *kbuildScript) body() string {
	return s.setup() + s.configuration() + s.buildSteps() + s.modulesAndOutputs()
}

// setup writes the variables, the cleanup after a stopped run and the mark of this run, the tool
// links, the kernel's reported identity, the toolchain identity with the reset of the object tree
// on a change of it, and the make variables.
func (s *kbuildScript) setup() string {
	var b strings.Builder
	w := func(format string, args ...interface{}) { fmt.Fprintf(&b, format, args...) }
	w("KIC=%s\nKOF=%s\nSZ=%s\n", treeWord(s.inputCheck), treeWord(s.outputFiles), treeWord(s.soongZip))
	w("K=%s\n", treeWord(s.kernelDir()))
	w("OBJ=\"$MOD\"/kbuild/%s\n", shq(s.kernelDir()))
	w("STG=\"$MOD\"/modules_staging\n")
	w("MAKE=\"$T\"/%s/make\n", buildToolsBin)
	w("CLANG=%s\n", treeWord(s.clangDir))
	w("SYSROOT=\"$T\"/%s\n", glibcSysroot)
	w("DTC=%s\nLIBCRYPTO=%s\nLIBCRYPTO_DIR=%s\n", treeWord(s.dtc), treeWord(s.libcrypto), treeWord(filepath.Dir(s.libcrypto)))

	// The failure hook: once the run has marked itself, a failure with a status from 1 to 127
	// deletes the marker when no process of the rule's session is left besides the inner bash
	// and the processes it descends from within the session, since the command then failed on
	// its own and nothing may still be writing.
	b.WriteString(`marked=0
failure_hook() {
  local st=$1
  if [ "$marked" = 1 ] && [ "$st" -ge 1 ] && [ "$st" -le 127 ] && ! session_has_others; then
    rm -f "$MOD/kbuild.started"
  fi
}
`)
	// The cleanup after a stopped run, then the mark of this run: every file this run writes gets
	// a change time later than the marker's, so the next run after a stop can delete them.
	b.WriteString(`if [ -e "$MOD/kbuild.started" ] && [ -d "$MOD/kbuild" ]; then
  find "$MOD/kbuild" \( -type f -o -type l \) -newercc "$MOD/kbuild.started" -delete || exit $?
fi
touch "$MOD/kbuild.started" || exit $?
marked=1
tries=0
while :; do
  { rm -f "$MOD/kbuild.probe" && : > "$MOD/kbuild.probe"; } || die $?
  [ -n "$(find "$MOD/kbuild.probe" -newercc "$MOD/kbuild.started")" ] && break
  tries=$((tries + 1))
`)
	w("  if [ $tries -ge %d ]; then\n", probeTries)
	b.WriteString(`    rm -f "$MOD/kbuild.probe"
    die 1 "the file system of the out directory gave $MOD/kbuild.probe no change time later than that of $MOD/kbuild.started. Put the out directory on a file system with change times finer than a second."
  fi
  sleep 0.1
done
rm -f "$MOD/kbuild.probe"
`)
	w("J=$(%s) || die $?\n", jobCountCommand)
	w("ln -sfn \"$DTC\" \"$MOD\"/tools/dtc && ln -sfn %s \"$MOD\"/tools/depmod && ln -sfn \"$T\"/%s \"$MOD\"/tools/perl || die $?\n",
		treeWord(s.depmod), perlPath)

	// The kernel's reported identity: fixed user, host and version, and the commit time of the
	// kernel's last commit as the date.
	w("read -r commit ctime _ < %s || die 1 \"cannot read %s\"\n", treeWord(s.gitState), s.gitState)
	b.WriteString(`timestamp=$(date -u -d "@$ctime") || die $?
export KBUILD_BUILD_USER=build-user KBUILD_BUILD_HOST=build-host KBUILD_BUILD_VERSION=1
export KBUILD_BUILD_TIMESTAMP="$timestamp"
`)

	// The toolchain identity: a change of it empties the object tree.
	b.WriteString("{ \"$CLANG\"/bin/clang --version && echo " + shq(s.arch()) + " && echo \"$T\" && echo " + shq(s.kernelDir()))
	for _, e := range s.extraSourceDirs() {
		b.WriteString(" && echo " + shq(e))
	}
	b.WriteString(" && echo " + shq(s.externalRoot))
	w(" && \"$KIC\" identity -cache \"$MOD\"/toolchain.hashes -link-targets \"$T\"/%s \"$CLANG\" \"$MAKE\""+
		" \"$T\"/%s/flex \"$T\"/%s/bison \"$T\"/%s/m4 \"$T\"/%s \"$T\"/%s \"$T\"/%s \"$SYSROOT\"; } > \"$TMPDIR\"/toolchain.id || die $?\n",
		buildToolsPath, buildToolsBin, buildToolsBin, buildToolsBin, perlPath, bisonData, perlBase)
	b.WriteString(`if ! cmp -s "$TMPDIR"/toolchain.id "$MOD"/toolchain.id; then
  rm -rf "$MOD"/kbuild "$MOD"/modules_staging || die $?
  stage "$TMPDIR"/toolchain.id "$MOD"/toolchain.id || die $?
fi
`)

	// The make variables of every kbuild make; every path is absolute.
	w("MV=(ARCH=%s LLVM=1 LLVM_IAS=1 LEX=\"$T\"/%s/flex YACC=\"$T\"/%s/bison M4=\"$T\"/%s/m4 DTC_EXT=\"$DTC\""+
		" %s \"CRYPTO_CFLAGS=-I$T/%s\" \"CRYPTO_LIBS=$LIBCRYPTO -Wl,-rpath,$LIBCRYPTO_DIR\" PAHOLE=)\n",
		shq(s.arch()), buildToolsBin, buildToolsBin, buildToolsBin, hostMakeVars, boringsslHeader)
	b.WriteString("mk() { \"$MAKE\" -j\"$J\" \"$@\" \"${MV[@]}\"; }\n")
	return b.String()
}

// configuration writes the configuration steps in the kept object tree. An unchanged .config keeps
// its modification time, so kbuild sees no configuration change.
func (s *kbuildScript) configuration() string {
	var b strings.Builder
	w := func(format string, args ...interface{}) { fmt.Fprintf(&b, format, args...) }
	configs := "\"$K\"/arch/" + shq(s.arch()) + "/configs/"
	b.WriteString(`mkdir -p "$OBJ" || die $?
if [ -f "$OBJ/.config" ]; then cp -p "$OBJ/.config" "$TMPDIR"/config.saved || die $?; fi
`)
	w("cp %s%s \"$OBJ\"/.config || die $?\n", configs, shq(s.defconfig()))
	b.WriteString("mk -C \"$K\" O=\"$OBJ\" olddefconfig || die $?\n")
	for _, f := range s.configFragments() {
		// merge_config.sh makes its temporary files in its working directory.
		w("( cd \"$TMPDIR\" && \"$K\"/scripts/kconfig/merge_config.sh -m -O \"$OBJ\" \"$OBJ\"/.config %s%s ) || die $?\n", configs, shq(f))
		b.WriteString("mk -C \"$K\" O=\"$OBJ\" olddefconfig || die $?\n")
	}
	b.WriteString(`if [ -f "$TMPDIR"/config.saved ] && cmp -s "$OBJ"/.config "$TMPDIR"/config.saved; then
  touch -r "$TMPDIR"/config.saved "$OBJ"/.config || die $?
fi
if grep -qx 'CONFIG_MODULE_SIG=y' "$OBJ"/.config; then
  die 1 "the kernel configuration sets CONFIG_MODULE_SIG=y, and module signing is not built: the kernel module packaging strips the modules again, which drops appended signatures. ` +
		`Turn CONFIG_MODULE_SIG off in a fragment of ` + s.names.of(propConfigFragments) + `."
fi
`)
	return b.String()
}

// buildSteps writes the build steps: the image, the device tree files, the in-tree modules and,
// after the removal of dropped external module entries, the external modules.
func (s *kbuildScript) buildSteps() string {
	var b strings.Builder
	w := func(format string, args ...interface{}) { fmt.Fprintf(&b, format, args...) }
	w("mk -C \"$K\" O=\"$OBJ\" %s || die $?\n", shq(s.image()))
	if s.dtImages() {
		w("if [ -d %s ]; then find %s -type f \\( -name '*.dtb' -o -name '*.dtbo' \\) -delete || die $?; fi\n", s.dts(), s.dts())
		b.WriteString("mk -C \"$K\" O=\"$OBJ\" dtbs || die $?\n")
	}
	// The in-tree modules are built for a module option of the configuration (a line
	// CONFIG_<name>=m), or for external modules, whose build needs the in-tree modules installed
	// whenever the kernel enables modules.
	inTreeModules := "grep -q '^CONFIG_[A-Za-z0-9_]*=m$' \"$OBJ\"/.config"
	if s.externalRoot != "" {
		inTreeModules += " || grep -qx 'CONFIG_MODULES=y' \"$OBJ\"/.config"
	}
	w(`{ rm -rf "$STG" && mkdir -p "$STG"; } || die $?
if %s; then
  mk -C "$K" O="$OBJ" modules || die $?
  mk -C "$K" O="$OBJ" INSTALL_MOD_PATH="$STG" INSTALL_MOD_STRIP=1 modules_install || die $?
fi
`, inTreeModules)
	// The removal of the object directories of external module entries that the last list named
	// and the current one does not, then the current list, before any external module is built.
	dirs := s.externalDirs()
	b.WriteString(externalRemovalScript(s.kernelDir(), s.externalRoot, dirs))
	if s.externalRoot != "" {
		b.WriteString(`if ! grep -qx 'CONFIG_MODULES=y' "$OBJ"/.config; then
  die 1 "` + s.names.of(propExternalModules) + ` is set, and the kernel configuration does not set CONFIG_MODULES=y, so no external module can be built. ` +
			`Set CONFIG_MODULES=y in a fragment of ` + s.names.of(propConfigFragments) + `, or remove ` + s.names.of(propExternalModules) + `."
fi
`)
		rel := relTo(s.kernelDir(), s.externalRoot)
		for _, d := range dirs {
			ext := []string{"-C", treeWord(path.Join(s.externalRoot, d)), "M=" + shq(path.Join(rel, d)), `KERNEL_SRC="$K"`, `O="$OBJ"`, `OUT_DIR="$OBJ"`}
			w("mk %s || die $?\n", words(ext...))
			w("mk %s INSTALL_MOD_PATH=\"$STG\" INSTALL_MOD_STRIP=1 KERNEL_UAPI_HEADERS_DIR=\"$OBJ\" modules_install || die $?\n", words(ext...))
		}
	}
	return b.String()
}

// modulesAndOutputs writes the collection of the modules and the device tree files, the check of
// every read kbuild recorded, the depfile with the check for inputs changed during the run, the
// outputs and the marker's deletion.
func (s *kbuildScript) modulesAndOutputs() string {
	var b strings.Builder
	w := func(format string, args ...interface{}) { fmt.Fprintf(&b, format, args...) }
	w("\"$KOF\" modules -staging \"$STG\" -out \"$MOD\"/modules_flat -renames-name %s", shq(s.names.of(propModuleRenames)))
	for _, r := range s.moduleRenames() {
		b.WriteString(" -rename " + shq(r))
	}
	b.WriteString(` || die $?
mkdir -p "$MOD"/zip || die $?
"$SZ" -o "$MOD"/zip/modules.zip -C "$MOD"/modules_flat -D "$MOD"/modules_flat || die $?
`)
	if s.dtImages() {
		w("if [ -d %s ]; then find %s -type f \\( -name '*.dtb' -o -name '*.dtbo' \\) > \"$TMPDIR\"/dtbs.list || die $?; else : > \"$TMPDIR\"/dtbs.list; fi\n", s.dts(), s.dts())
		w("\"$SZ\" -o \"$MOD\"/zip/dtbs.zip -C %s -l \"$TMPDIR\"/dtbs.list || die $?\n", s.dts())
	}

	// The check of every read kbuild recorded.
	allow := []string{`"$MOD"`, treeWord(buildTools), `"$DTC"`, treeWord(s.depmod)}
	for _, d := range s.sourceDirs {
		allow = append(allow, treeWord(d))
	}
	for _, l := range s.libraries {
		allow = append(allow, treeWord(l))
	}
	b.WriteString(`"$KIC" check -make "$MAKE" -objdir "$OBJ" -srctree "$K" -scan "$MOD"/kbuild -top "$T" -reads "$MOD"/toolchain_reads.txt`)
	w(" -next-step %s", shq(s.addSourceDir))
	for _, a := range allow {
		b.WriteString(" -allow " + a)
	}
	w(" -toolchain \"$CLANG\" -toolchain \"$SYSROOT\" -toolchain \"$T\"/%s || die $?\n", boringsslHeader)

	// The depfile, then the check for inputs changed during the run.
	deps := append(quoteAll(s.sourceEntries), shq(s.clangDir), buildToolsBin, buildToolsPath, bisonData, perlBase, glibcSysroot)
	w("set -- %s\n", words(deps...))
	w("write_depfile \"$MOD\"/kbuild.d %s -list \"$MOD\"/toolchain_reads.txt \"$@\" || die $?\n", treeWord(s.imageOut))
	b.WriteString(`mapfile -t reads < "$MOD"/toolchain_reads.txt
check_unchanged "$@" "${reads[@]}" || die 1
`)

	// The outputs, once every check has passed, then the marker's deletion.
	w("stage \"$OBJ\"/arch/%s/boot/%s %s || die $?\n", shq(s.arch()), shq(s.image()), treeWord(s.imageOut))
	w("stage \"$OBJ\"/System.map %s || die $?\n", treeWord(s.systemMapOut))
	w("stage \"$MOD\"/zip/modules.zip %s || die $?\n", treeWord(s.modulesOut))
	if s.dtImages() {
		w("stage \"$MOD\"/zip/dtbs.zip %s || die $?\n", treeWord(s.dtbsOut))
	}
	b.WriteString("rm -f \"$MOD\"/kbuild.started\n")
	return b.String()
}

// externalRemovalScript is the part of the kbuild rule's inner script that handles the list of
// external module object directories, <module out>/external_modules.txt. Each line of the list the
// last run wrote that is no current object directory is dropped: its directory below
// <module out>/kbuild is deleted, or only its Module.symvers when a current directory lies inside it
// or contains it, and nothing when it is, contains or lies inside the kernel's object directory.
// Then the current list is staged, before any external module is built.
func externalRemovalScript(kernelDir, root string, dirs []string) string {
	var current []string
	for _, d := range dirs {
		current = append(current, shq(path.Join(root, d)))
	}
	return "CUR=(" + strings.Join(current, " ") + ")\n" +
		"KDIR=" + shq(kernelDir) + "\n" +
		`over() { case "$1" in "$2"|"$2"/*) return 0 ;; esac; case "$2" in "$1"/*) return 0 ;; esac; return 1; }
if [ -f "$MOD"/external_modules.txt ]; then
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    keep=0
    for d in "${CUR[@]}"; do [ "$d" = "$line" ] && keep=1; done
    [ $keep = 1 ] && continue
    over "$line" "$KDIR" && continue
    near=0
    for d in "${CUR[@]}"; do over "$d" "$line" && near=1; done
    if [ $near = 1 ]; then
      rm -f "$MOD/kbuild/$line/Module.symvers" || die $?
    else
      rm -rf "$MOD/kbuild/$line" || die $?
    fi
  done < "$MOD"/external_modules.txt
fi
{ for d in "${CUR[@]}"; do printf '%s\n' "$d"; done; } > "$MOD"/external_modules.txt.tmp || die $?
stage "$MOD"/external_modules.txt.tmp "$MOD"/external_modules.txt || die $?
`
}

// buildDtRule writes the sandboxed rule that makes dtb.img and dtbo.img from dtbs.zip.
func (k *kernelBuild) buildDtRule(ctx android.ModuleContext, outs kernelOutputs) {
	qcom := k.qcomMerge()
	pageSize := proptools.Int(k.properties.Dt_images.Dtbo_page_size)
	tools := []string{"mkdtboimg", "zipsync", "kernel_output_files"}
	if qcom {
		tools = append(tools, "merge_dtbs")
		tools = append(tools, qcomDtTools...)
	}
	// The sandbox gets every tool: the packaging specs with their libraries and required tools, and
	// the plain tools.
	specs, plain := hostToolDeps(ctx, tools)

	rule := android.NewRuleBuilder(pctx, ctx).
		Sbox(android.PathForModuleOut(ctx, "dt_images"), android.PathForModuleOut(ctx, "dt_images.sbox.textproto")).
		Restat()
	cmd := rule.Command()
	cmd.ImplicitPackagedTools(specs)
	for _, t := range plain {
		cmd.ImplicitTool(t)
	}
	zip := cmd.PathForInput(outs.dtbs)
	cmd.Implicit(outs.dtbs)
	dtb := cmd.PathForOutput(outs.dtb)
	dtbo := cmd.PathForOutput(outs.dtbo)
	cmd.ImplicitOutput(outs.dtb).ImplicitOutput(outs.dtbo)

	// merge_dtbs runs its device tree programs by name, so the sandbox's tool directory comes
	// first on PATH. cp, mkdir, mv and cat come from the PATH the sandboxed command inherits.
	// sbox replaces the sandbox directory placeholder with `.`, so the PATH entry and the output
	// paths hold only in the sandbox directory: the command never changes its directory and names
	// the extracted files below dts.
	var b strings.Builder
	b.WriteString("export PATH=__SBOX_SANDBOX_DIR__/tools/out/bin:$PATH && export LC_ALL=C && ")
	fmt.Fprintf(&b, "zipsync -d dts %s && ", zip)
	// The directory the images are made from: the merged files, or the extracted ones as they
	// are.
	imageDir := "dts"
	if qcom {
		imageDir = "dts/out"
		// noFiles is the failure for a missing kind of base file, named with the merge value.
		noFiles := func(ext string) string {
			msg := fmt.Sprintf("dt_images: merge: %q: no *%s file directly inside vendor/*/. "+
				"Build the base device trees of the device below arch/%s/boot/dts/vendor, or turn off the qcom merge in %s.",
				mergeQcom, ext, proptools.String(k.properties.Kernel_arch), k.name(propDtImagesMerge))
			return fmt.Sprintf("{ echo %s >&2; exit 1; }", shq(msg))
		}
		b.WriteString(`mkdir -p dts/base dts/out && shopt -s nullglob && ` +
			`dtbs=(dts/vendor/*/*.dtb) && ` +
			`{ [ ${#dtbs[@]} -gt 0 ] || ` + noFiles(".dtb") + `; } && ` +
			`mv "${dtbs[@]}" dts/base/ && ` +
			`dtbos=(dts/vendor/*/*.dtbo) && ` +
			`{ [ ${#dtbos[@]} -gt 0 ] || ` + noFiles(".dtbo") + `; } && ` +
			`mv "${dtbos[@]}" dts/base/ && ` +
			`merge_dtbs --base dts/base --techpack dts/vendor/qcom --out dts/out && `)
	}
	noDeviceTrees := shq(fmt.Sprintf("Enable the device trees of the device in the kernel configuration, or remove %s.",
		k.name(propDtImages)))
	fmt.Fprintf(&b, "kernel_output_files dt -dir %s -suffix .dtb -o dtb.list -next-step %s && "+
		"kernel_output_files dt -dir %s -suffix .dtbo -o dtbo.list -next-step %s && "+
		"{ while read -r f; do cat \"$f\"; done < dtb.list; } > %s && "+
		"mkdtboimg create %s --page_size=%d $(cat dtbo.list)", imageDir, noDeviceTrees, imageDir, noDeviceTrees, dtb, dtbo, pageSize)
	cmd.Text(b.String())
	rule.Build("dt_images", "device tree images of "+ctx.ModuleName())
}
