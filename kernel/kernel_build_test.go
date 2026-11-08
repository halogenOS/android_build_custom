// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"android/soong/android"
	"android/soong/cc"

	"github.com/google/blueprint/proptools"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// testHostTool stands for a host tool module: it installs one file into the host bin directory,
// so it has the packaging specs and the host tool path a real tool has.
type testHostTool struct {
	android.ModuleBase
	path android.OptionalPath
}

func testHostToolFactory() android.Module {
	m := &testHostTool{}
	android.InitAndroidArchModule(m, android.HostSupported, android.MultilibFirst)
	return m
}

func (m *testHostTool) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	out := android.PathForModuleOut(ctx, ctx.ModuleName())
	android.WriteFileRule(ctx, out, "")
	m.path = android.OptionalPathForPath(ctx.InstallFile(android.PathForModuleInstall(ctx, "bin"), ctx.ModuleName(), out))
}

func (m *testHostTool) HostToolPath() android.OptionalPath { return m.path }

// The host tools the kernel module types depend on.
var allHostTools = []string{"dtc", "depmod", "kbuild_input_check", "kernel_output_files", "soong_zip", "mkdtboimg",
	"zipsync", "merge_dtbs", "fdtget", "fdtoverlay", "fdtoverlaymerge", "fdtput", "ufdt_apply_overlay",
	"kernel_partition_modules_builder", "kernel_modules_builder", "merge_zips"}

func hostToolsFixture(tools []string) android.FixturePreparer {
	var bp strings.Builder
	for _, tool := range tools {
		bp.WriteString("test_host_tool { name: \"" + tool + "\" }\n")
	}
	return android.GroupFixturePreparers(
		android.FixtureRegisterWithContext(func(ctx android.RegistrationContext) {
			ctx.RegisterModuleType("test_host_tool", testHostToolFactory)
		}),
		android.FixtureAddTextFile("host_tools/Android.bp", bp.String()),
	)
}

var kernelMockFS = android.MockFS{
	"kernel/k/Makefile":                                  nil,
	"kernel/k/.scmversion":                               nil,
	"kernel/k/drivers/a.c":                               nil,
	"kernel/k/arch/arm64/configs/gki_defconfig":          nil,
	"kernel/k/arch/arm64/configs/one.config":             nil,
	"kernel/k/arch/arm64/configs/two.config":             nil,
	"kernel/dts/qcom/Makefile":                           nil,
	"kernel/mods/a/Makefile":                             nil,
	"kernel/mods/b/c/Makefile":                           nil,
	"prebuilts/tools-lineage/linux-x86/bin/perl":         nil,
	"prebuilts/tools-lineage/common/perl-base/x":         nil,
	"prebuilts/clang/host/linux-x86/clang-old/bin/clang": nil,
	"prebuilts/clang/kernel/clang-k/bin/clang":           nil,
}

func prepareKernelWithFS(tools []string, fs android.MockFS, extra ...android.FixturePreparer) android.FixturePreparer {
	return android.GroupFixturePreparers(append([]android.FixturePreparer{
		cc.PrepareForTestWithCcDefaultModules,
		hostToolsFixture(tools),
		android.FixtureRegisterWithContext(registerBuildComponents),
		android.FixtureAddTextFile("libcrypto/Android.bp", `cc_library { name: "libcrypto", host_supported: true }`),
		fs.AddToFixture(),
	}, extra...)...)
}

func prepareKernel(tools []string, extra ...android.FixturePreparer) android.FixturePreparer {
	return prepareKernelWithFS(tools, kernelMockFS, extra...)
}

const pongLike = `
kernel_build {
    name: "k",
    kernel_dir: "kernel/k",
    extra_source_dirs: ["kernel/dts"],
    kernel_arch: "arm64",
    image: "Image",
    defconfig: "gki_defconfig",
    config_fragments: ["one.config", "two.config"],
    external_modules: {
        root: "kernel/mods",
        dirs: ["a", "b/c"],
    },
    module_renames: ["wlan.ko:qca.ko"],
    dt_images: {
        merge: "qcom",
        dtbo_page_size: 4096,
    },
}
`

var noDtImages = regexp.MustCompile(`(?s)\s*dt_images: \{.*?\},`).ReplaceAllString(pongLike, "")

func namedAs(ref string) android.FixturePreparer {
	return android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) {
		v.BoardKernelBuildModule = proptools.StringPtr(ref)
	})
}

// ruleScript gives the inner script of a rule of def-rule-session, the content of the file
// <name>.sh that the module's write-file rule writes and the rule's step shell runs.
func ruleScript(t *testing.T, result *android.TestResult, m android.TestingModule, name string) string {
	t.Helper()
	return android.ContentFromFileRuleForTests(t, result.TestContext, m.Output(name+".sh"))
}

// kbuildScriptOf gives the inner script of a kernel_build module's kbuild rule.
func kbuildScriptOf(t *testing.T, result *android.TestResult, name string) string {
	t.Helper()
	return ruleScript(t, result, result.ModuleForTests(t, name, "android_common"), "kbuild")
}

// checkShellSyntax runs `bash -n` over a rule's command, the one line the build runs, with the
// Ninja escape of `$` undone, and over its inner script.
func checkShellSyntax(t *testing.T, what, command, script string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("no bash: %v", err)
	}
	if strings.Contains(command, "\n") {
		t.Errorf("%s: the command holds a newline, which Ninja removes: %q", what, command)
	}
	for _, c := range []struct{ name, text string }{{"command", strings.ReplaceAll(command, "$$", "$")}, {"script", script}} {
		if out, err := exec.Command(bash, "-n", "-c", c.text).CombinedOutput(); err != nil {
			t.Errorf("%s: bash -n of the %s: %v\n%s", what, c.name, err, out)
		}
	}
}

// inOrder checks that each part occurs in s after the one before.
func inOrder(t *testing.T, what, s string, parts ...string) {
	t.Helper()
	at := 0
	for _, p := range parts {
		i := strings.Index(s[at:], p)
		if i < 0 {
			t.Errorf("%s: %q not found after position %d", what, p, at)
			return
		}
		at += i + len(p)
	}
}

func containsAll(t *testing.T, what, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("%s lacks %q", what, p)
		}
	}
}

func TestKbuildRule(t *testing.T) {
	result := prepareKernel(allHostTools).RunTestWithBp(t, pongLike)
	m := result.ModuleForTests(t, "k", "android_common")
	rule := m.Rule("kbuild")
	cmd := rule.RuleParams.Command

	if rule.RuleParams.Pool == nil || !strings.HasSuffix(rule.RuleParams.Pool.String(), "highmem_pool") {
		t.Errorf("the kbuild rule is not in the highmem pool: %v", rule.RuleParams.Pool)
	}
	if !rule.RuleParams.Restat {
		t.Errorf("the kbuild rule has no restat")
	}
	if !rule.RuleParams.SandboxDisabled {
		t.Errorf("the kbuild rule is not SandboxDisabled")
	}
	if !strings.HasSuffix(rule.Depfile.String(), "/kbuild.d") {
		t.Errorf("depfile: %v", rule.Depfile)
	}
	outputs := strings.Join(append(rule.ImplicitOutputs.Strings(), rule.Output.String()), " ")
	containsAll(t, "outputs", outputs, "/Image", "/System.map", "/modules.zip", "/dtbs.zip")
	containsAll(t, "inputs", strings.Join(rule.Implicits.Strings(), " "),
		"git_state.txt", "/config", "prebuilts/tools-lineage/linux-x86/bin/perl", "/bin/clang",
		"prebuilts/build-tools/linux-x86/bin/make")
	// The Go host tools BuiltTool declares, the two tools kbuild calls and the host libcrypto.
	containsAll(t, "tools", strings.Join(rule.RuleParams.CommandDeps, " "),
		"bin/kbuild_input_check", "bin/kernel_output_files", "bin/soong_zip", "bin/dtc", "bin/depmod",
		"libcrypto/linux_glibc_x86_64_shared/libcrypto.so")

	// The step's shell reads the inner script from kbuild.sh, which is an input of the rule, and
	// starts it in the kbuild rule's own session with the environment: the fixed base environment of
	// the rule's shell with the kbuild additions BISON_PKGDATADIR and PERL5LIB.
	containsAll(t, "inputs", strings.Join(rule.Implicits.Strings(), " "), "/kbuild.sh")
	containsAll(t, "command", cmd, "{ T=$$PWD; script=$$(cat ", "/kbuild.sh) || exit $$?; env -i PATH=",
		"/tools:\"$$T\"/prebuilts/clang/host/linux-x86/clang-r",
		"/bin:\"$$T\"/prebuilts/build-tools/path/linux-x86:\"$$PATH\" ",
		"HOME=", "/home ", "TMPDIR=", "/tmp/kbuild ", "LC_ALL=C", "TZ=UTC", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0='*'", "GIT_CONFIG_KEY_1=core.checkStat", "GIT_CONFIG_VALUE_1=minimal",
		"GIT_CONFIG_KEY_2=core.trustctime", "GIT_CONFIG_VALUE_2=false",
		"BISON_PKGDATADIR=\"$$T\"/prebuilts/build-tools/common/bison", "PERL5LIB=\"$$T\"/prebuilts/tools-lineage/common/perl-base",
		`"$$T"/prebuilts/build-tools/path/linux-x86/setsid bash -c "$$script"`,
		"& p=$$!; trap 'kill -TERM -$$p 2>/dev/null' INT TERM; while :; do wait $$p;")
	if strings.Contains(strings.Split(cmd, "setsid bash")[0], "prebuilts/build-tools/linux-x86/bin") {
		t.Errorf("prebuilts/build-tools/linux-x86/bin is on the rule's PATH")
	}
	script := ruleScript(t, result, m, "kbuild")
	checkShellSyntax(t, "kbuild", cmd, script)
	containsAll(t, "script", script, "NAME=kbuild", `LOCK="$MOD/$NAME.lock"`, `PGIDF="$MOD/$NAME.pgid"`)

	// The order of the inner script: the lock, the wait for it with the process group kill, the
	// process group file, TMPDIR, the start time, the marker, the links, the identity, the reset,
	// the configuration and its check, the build steps, the collection, the input check, the
	// depfile, the check for changed inputs, the staging and the marker deletion.
	inOrder(t, "kbuild steps", script,
		"read -r stat < /proc/$$/stat", "PGID=$3", `exec 9>>"$LOCK"`,
		`group_procs() {`, `holds_lock() {`, `for p in $(group_procs "$1")`,
		`"$TOYBOX" flock -n 9`, `g=$(cat "$PGIDF"`, `holds_lock "$g"`, `kill -KILL -"$g"`, "tries -ge 60",
		`for p in $(group_procs "$g")`,
		`echo "$PGID" > "$PGIDF"`,
		`mkdir -p "$HOME" "$MOD/tools" && rm -rf "$TMPDIR" && mkdir -p "$TMPDIR"`, "START=$(date +%s.%N)",
		`if [ -e "$MOD/kbuild.started" ]`, `-newercc "$MOD/kbuild.started" -delete`, `touch "$MOD/kbuild.started"`,
		"marked=1", "kbuild.probe", "tries -ge 100",
		"read -r commit ctime _ <", `date -u -d "@$ctime"`,
		"KBUILD_BUILD_USER=build-user KBUILD_BUILD_HOST=build-host KBUILD_BUILD_VERSION=1", `KBUILD_BUILD_TIMESTAMP="$timestamp"`,
		`"$CLANG"/bin/clang --version && echo arm64 && echo "$T" && echo kernel/k && echo kernel/dts && echo kernel/mods`,
		`"$KIC" identity -cache "$MOD"/toolchain.hashes -link-targets "$T"/prebuilts/build-tools/path/linux-x86 "$CLANG" "$MAKE"`,
		"bin/flex", "bin/bison", "bin/m4", "prebuilts/tools-lineage/linux-x86/bin/perl", "prebuilts/build-tools/common/bison",
		"prebuilts/tools-lineage/common/perl-base", `"$SYSROOT"; } > "$TMPDIR"/toolchain.id`,
		`rm -rf "$MOD"/kbuild "$MOD"/modules_staging`, `stage "$TMPDIR"/toolchain.id "$MOD"/toolchain.id || die $?`,
		"J=$(getconf _NPROCESSORS_ONLN)",
		`"$MOD"/tools/dtc`, `"$MOD"/tools/depmod`, `"$MOD"/tools/perl`,
		"MV=(ARCH=arm64 LLVM=1 LLVM_IAS=1 LEX=", "YACC=", "M4=", `DTC_EXT="$DTC"`, `"HOSTCFLAGS=--sysroot=$SYSROOT"`,
		`"HOSTLDFLAGS=--sysroot=$SYSROOT -fuse-ld=lld --rtlib=compiler-rt"`, `"CRYPTO_CFLAGS=-I$T/external/boringssl/src/include"`,
		`"CRYPTO_LIBS=$LIBCRYPTO -Wl,-rpath,$LIBCRYPTO_DIR" PAHOLE=)`, `"$MAKE" -j"$J"`,
		`mkdir -p "$OBJ" || die $?`, `/config "$OBJ"/.config || die $?`,
		`O="$OBJ" Image`, "-delete", `O="$OBJ" dtbs`,
		`rm -rf "$STG" && mkdir -p "$STG"`, "^CONFIG_[A-Za-z0-9_]*=m$", `O="$OBJ" modules `,
		`INSTALL_MOD_PATH="$STG" INSTALL_MOD_STRIP=1 modules_install`,
		"CUR=(kernel/mods/a kernel/mods/b/c)", "external_modules.txt", "Module.symvers", "external_modules.txt.tmp",
		"if ! grep -qx ", "external_modules is set, and the kernel configuration does not set CONFIG_MODULES=y",
		`mk -C "$T"/kernel/mods/a M=../mods/a KERNEL_SRC="$K" O="$OBJ" OUT_DIR="$OBJ"`,
		`KERNEL_UAPI_HEADERS_DIR="$OBJ" modules_install`, "M=../mods/b/c",
		`"$KOF" modules -staging "$STG" -out "$MOD"/modules_flat -renames-name module_renames -rename`, "wlan.ko:qca.ko", "zip/modules.zip", "zip/dtbs.zip",
		`"$KIC" check -make "$MAKE" -objdir "$OBJ" -srctree "$K" -scan "$MOD"/kbuild`,
		"-next-step 'Add the directory that contains these files to extra_source_dirs.'", `-allow "$T"/kernel/k -allow "$T"/kernel/dts -allow "$T"/kernel/mods`,
		`-toolchain "$CLANG" -toolchain "$SYSROOT" -toolchain "$T"/external/boringssl/src/include`,
		`write_depfile "$MOD"/kbuild.d`, `-list "$MOD"/toolchain_reads.txt`,
		`check_unchanged "$@" "${reads[@]}"`, `stage "$OBJ"/arch/arm64/boot/Image`, `stage "$OBJ"/System.map`,
		`stage "$MOD"/zip/modules.zip`, `stage "$MOD"/zip/dtbs.zip`, `rm -f "$MOD"/kbuild.started`)

	// Modules are built only for a module option of the configuration, not for a string value
	// that holds `=m`.
	if strings.Contains(script, "'=m'") {
		t.Errorf("the module steps look for =m anywhere in a line of .config")
	}

	// The depfile is written to its .tmp file and renamed; staging keeps an equal output untouched.
	containsAll(t, "script", script, `"$KIC" depfile -o "$d.tmp" -t "$t"`, `mv -f "$d.tmp" "$d"`,
		`cmp -s "$1" "$2"`, `cp "$1" "$2.tmp"`, `mv -f "$2.tmp" "$2"`,
		`-newerct "@$START"`, "__pycache__", "*.pyc")

	// The depfile's entries: the top-level entries of each source directory, a dot entry included,
	// and the toolchain with the glibc sysroot.
	set := regexp.MustCompile(`\nset -- ([^\n]*)\nwrite_depfile "\$MOD"/kbuild.d`).FindStringSubmatch(script)
	if set == nil {
		t.Fatalf("no depfile entries in the script")
	}
	entries := strings.Fields(set[1])
	for _, e := range []string{"kernel/k/.scmversion", "kernel/k/Makefile", "kernel/k/drivers", "kernel/k/arch", "kernel/dts/qcom",
		"kernel/mods/a", "kernel/mods/b", "prebuilts/build-tools/linux-x86/bin", "prebuilts/build-tools/path/linux-x86",
		"prebuilts/build-tools/common/bison", "prebuilts/tools-lineage/common/perl-base",
		"prebuilts/gcc/linux-x86/host/x86_64-linux-glibc2.17-4.8/sysroot"} {
		if !android.InList(e, entries) {
			t.Errorf("the depfile entries lack %s: %v", e, entries)
		}
	}
	if android.InList("kernel/k/.git", entries) {
		t.Errorf("the depfile entries name .git")
	}
	// The marker is deleted after a failure with a status from 1 to 127 when no other process of
	// the session is left.
	containsAll(t, "script", script, `[ "$st" -ge 1 ] && [ "$st" -le 127 ] && ! session_has_others`, `pgrep -s "$SID"`,
		`rm -f "$MOD/kbuild.started"`)
	// die calls the last failure_hook defined before it runs: the kbuild body's hook has to follow
	// the shared no-op one and come before the first call of die, or a failure keeps the marker.
	inOrder(t, "failure hooks", script, "failure_hook() { :; }", "\nmarked=0\nfailure_hook() {\n")
	hook := strings.Index(script, "\nmarked=0\nfailure_hook() {\n")
	if firstDie := regexp.MustCompile(`\bdie (\$\?|[0-9])`).FindStringIndex(script); firstDie == nil || hook < 0 || firstDie[0] < hook {
		t.Errorf("die is called before the kbuild body defines its failure hook: hook at %d, first die at %v", hook, firstDie)
	}

	// The kconfig rule makes the configuration in an empty object tree and stages it at the output
	// that the kbuild rule reads.
	kconfig := m.Rule("kconfig")
	containsAll(t, "kconfig inputs", strings.Join(kconfig.Implicits.Strings(), " "),
		"/kconfig.sh", "/bin/clang", "prebuilts/build-tools/linux-x86/bin/make",
		"configs/gki_defconfig", "configs/one.config", "configs/two.config")
	if outputs := append(kconfig.ImplicitOutputs.Strings(), kconfig.Output.String()); !strings.HasSuffix(strings.Join(outputs, " "), "/config") {
		t.Errorf("kconfig outputs: %v", outputs)
	}
	kconfigScript := ruleScript(t, result, m, "kconfig")
	checkShellSyntax(t, "kconfig", kconfig.RuleParams.Command, kconfigScript)
	inOrder(t, "kconfig steps", kconfigScript,
		"NAME=kconfig", `OBJ="$MOD"/kconfig`, "J=$(getconf _NPROCESSORS_ONLN)", `"$MOD"/tools/perl`, "MV=(ARCH=arm64",
		`{ rm -rf "$OBJ" && mkdir -p "$OBJ"; }`, `configs/gki_defconfig "$OBJ"/.config`, "olddefconfig",
		`( cd "$TMPDIR" && "$K"/scripts/kconfig/merge_config.sh -m -O "$OBJ" "$OBJ"/.config "$K"/arch/arm64/configs/one.config )`,
		"olddefconfig", "configs/two.config", "olddefconfig", "CONFIG_MODULE_SIG=y",
		`write_depfile "$MOD"/kconfig.d`, `check_unchanged "$@"`, `stage "$OBJ"/.config`)

	// No kernel_build module has a checkbuild file, and each has one variant.
	if files := android.GetInstallFiles(result.OtherModuleProviderAdaptor(), m.Module()).CheckbuildFiles; len(files) != 0 {
		t.Errorf("checkbuild files: %v", files)
	}
	if v := result.ModuleVariantsForTests("k"); len(v) != 1 {
		t.Errorf("variants: %v", v)
	}
}

// TestModuleSteps checks when the module steps run: the in-tree modules for a module option
// `=m`, or under CONFIG_MODULES=y when external modules are set, which then fail without
// CONFIG_MODULES=y.
func TestModuleSteps(t *testing.T) {
	result := prepareKernel(allHostTools).RunTestWithBp(t, pongLike)
	script := kbuildScriptOf(t, result, "k")
	inOrder(t, "module steps with external modules", script,
		"if grep -q '^CONFIG_[A-Za-z0-9_]*=m$' \"$OBJ\"/.config || grep -qx 'CONFIG_MODULES=y' \"$OBJ\"/.config; then",
		`O="$OBJ" modules `, "external_modules.txt",
		"if ! grep -qx 'CONFIG_MODULES=y' \"$OBJ\"/.config; then", "die 1", "external_modules is set", "CONFIG_MODULES=y",
		`mk -C "$T"/kernel/mods/a`, `mk -C "$T"/kernel/mods/b/c`)

	noExternal := regexp.MustCompile(`(?s)\s*external_modules: \{.*?\},`).ReplaceAllString(pongLike, "")
	result = prepareKernel(allHostTools).RunTestWithBp(t, noExternal)
	script = kbuildScriptOf(t, result, "k")
	inOrder(t, "module steps without external modules", script,
		"if grep -q '^CONFIG_[A-Za-z0-9_]*=m$' \"$OBJ\"/.config; then", `O="$OBJ" modules `, "CUR=()")
	if strings.Contains(script, "CONFIG_MODULES") {
		t.Errorf("a module without external modules names CONFIG_MODULES: %s", script)
	}
}

// TestModuleRefs checks the one condition for the two forms of a module reference and the
// qualified name a module is referred to by.
func TestModuleRefs(t *testing.T) {
	for ref, want := range map[string]string{
		":k":                 "k",
		"//device/x:k":       "//device/x:k",
		"//device/x/sub:k-1": "//device/x/sub:k-1",
		"k":                  "",
		":":                  "",
		"::k":                "",
		":k/x":               "",
		"//:k":               "",
		"//device/x":         "",
		"//device/x:k:l":     "",
		"//device/x:k/l":     "",
		"//device:x/sub:k":   "",
	} {
		got, ok := parseModuleRef(ref)
		if ok != (want != "") || got != want {
			t.Errorf("parseModuleRef(%q) = %q, %v; want %q", ref, got, ok, want)
		}
	}
	if got := qualifiedName(".", "k"); got != ":k" {
		t.Errorf("qualified name in the root namespace: %q", got)
	}
	if got := qualifiedName("device/x", "k"); got != "//device/x:k" {
		t.Errorf("qualified name in a namespace: %q", got)
	}
}

// TestRuleEnvRelative checks the environment of a rule whose module out directory Soong gives
// relative to the tree top: every tree path is made absolute from $PWD.
func TestRuleEnvRelative(t *testing.T) {
	cmd := shellRule{name: "git_state", modOut: "out/soong/.intermediates/k/android_common", toolDir: true,
		clangDir: "prebuilts/clang/host/linux-x86/clang-r1", body: ":\n"}.command("out/soong/.intermediates/k/android_common/git_state.sh")
	containsAll(t, "command", cmd,
		`script=$(cat "$T"/out/soong/.intermediates/k/android_common/git_state.sh) || exit $?;`,
		`PATH="$T"/out/soong/.intermediates/k/android_common/tools:"$T"/prebuilts/clang/host/linux-x86/clang-r1/bin:"$T"/prebuilts/build-tools/path/linux-x86:"$PATH" `,
		`HOME="$T"/out/soong/.intermediates/k/android_common/home `,
		`TMPDIR="$T"/out/soong/.intermediates/k/android_common/tmp/git_state `,
		"GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_COUNT=3 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0='*' "+
			"GIT_CONFIG_KEY_1=core.checkStat GIT_CONFIG_VALUE_1=minimal GIT_CONFIG_KEY_2=core.trustctime GIT_CONFIG_VALUE_2=false")
	headers := shellRule{name: "headers", modOut: "out/x", clangDir: "c", body: ":\n"}.command("out/x/headers.sh")
	if strings.Contains(headers, "/tools:") {
		t.Errorf("a rule without a tool directory has one on PATH: %s", headers)
	}
}

// TestExternalModuleRemoval runs the part of the inner script that removes the object directories
// of dropped external module entries.
func TestExternalModuleRemoval(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("no bash: %v", err)
	}
	type setup struct {
		kernelDir, root string
		dirs            []string
		oldList         *string
		existing        []string
	}
	run := func(t *testing.T, s setup) string {
		t.Helper()
		mod := t.TempDir()
		for _, d := range s.existing {
			if err := os.MkdirAll(filepath.Join(mod, "kbuild", d), 0777); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(mod, "kbuild", d, "Module.symvers"), nil, 0666); err != nil {
				t.Fatal(err)
			}
		}
		if s.oldList != nil {
			if err := os.WriteFile(filepath.Join(mod, "external_modules.txt"), []byte(*s.oldList), 0666); err != nil {
				t.Fatal(err)
			}
		}
		script := `MOD="$1"
die() { exit "$1"; }
stage() { mv -f "$1" "$2"; }
` + externalRemovalScript(s.kernelDir, s.root, s.dirs)
		out, err := exec.Command(bash, "-c", script, "bash", mod).CombinedOutput()
		if err != nil {
			t.Fatalf("script failed: %v\n%s", err, out)
		}
		return mod
	}
	exists := func(mod, rel string) bool {
		_, err := os.Stat(filepath.Join(mod, "kbuild", rel))
		return err == nil
	}
	list := func(lines ...string) *string {
		s := strings.Join(lines, "\n") + "\n"
		return &s
	}
	all := []string{"k", "m/a", "m/b", "m/b/c", "m/d", "m/e/f", "other/x"}

	// A dropped entry's directory is deleted, one around or inside a current entry keeps all
	// but its Module.symvers, and one at or around the kernel's object directory is untouched.
	mod := run(t, setup{kernelDir: "k", root: "m", dirs: []string{"a", "b/c", "e/f/g"},
		oldList: list("m/a", "m/d", "m/b", "m/e/f", "k"), existing: all})
	if !exists(mod, "m/a/Module.symvers") {
		t.Errorf("a current entry lost its Module.symvers")
	}
	if exists(mod, "m/d") {
		t.Errorf("the directory of a dropped entry stayed")
	}
	if !exists(mod, "m/b") || exists(mod, "m/b/Module.symvers") || !exists(mod, "m/b/c/Module.symvers") {
		t.Errorf("a dropped entry that contains a current one was not reduced to losing its Module.symvers")
	}
	if !exists(mod, "m/e/f") || exists(mod, "m/e/f/Module.symvers") {
		t.Errorf("a dropped entry inside which a current one lies was not reduced to losing its Module.symvers")
	}
	if !exists(mod, "k/Module.symvers") {
		t.Errorf("a line naming the kernel's object directory deleted something")
	}
	data, _ := os.ReadFile(filepath.Join(mod, "external_modules.txt"))
	if string(data) != "m/a\nm/b/c\nm/e/f/g\n" {
		t.Errorf("the new list: %q", data)
	}

	// Without the list nothing is deleted.
	mod = run(t, setup{kernelDir: "k", root: "m", dirs: []string{"a"}, existing: all})
	for _, d := range all {
		if !exists(mod, d+"/Module.symvers") {
			t.Errorf("without the list %s was deleted", d)
		}
	}

	// After a change of root every old line is dropped, and after external_modules is removed too.
	mod = run(t, setup{kernelDir: "k", root: "other", dirs: []string{"x"}, oldList: list("m/a", "m/d"), existing: all})
	if exists(mod, "m/a") || exists(mod, "m/d") || !exists(mod, "other/x/Module.symvers") {
		t.Errorf("after a change of root the old entries stayed")
	}
	mod = run(t, setup{kernelDir: "k", oldList: list("m/a", "m/d"), existing: all})
	if exists(mod, "m/a") || exists(mod, "m/d") {
		t.Errorf("after external_modules was removed the old entries stayed")
	}
	data, _ = os.ReadFile(filepath.Join(mod, "external_modules.txt"))
	if len(data) != 0 {
		t.Errorf("the list without external_modules: %q", data)
	}
}

// TestGroupProcs runs the lock preamble's group_procs, which lists the processes of a process group
// by the process group field of /proc/<pid>/stat: it lists a background job of its own process
// group and not the shell that started it.
func TestGroupProcs(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("no bash: %v", err)
	}
	fn := regexp.MustCompile(`(?s)\ngroup_procs\(\) \{\n.*?\n\}\n`).FindString(sessionPreamble)
	if fn == "" {
		t.Fatalf("no group_procs in the preamble")
	}
	script := fn + `set -m
sleep 30 & p=$!
list=" $(group_procs "$p" | tr '\n' ' ')"
kill $p
case "$list" in *" $p "*) ;; *) echo "the job $p is not listed: $list"; exit 1 ;; esac
case "$list" in *" $$ "*) echo "the shell $$ of another group is listed: $list"; exit 1 ;; esac
`
	if out, err := exec.Command(bash, "-c", script).CombinedOutput(); err != nil {
		t.Errorf("group_procs: %v\n%s", err, out)
	}
	if strings.Contains(sessionPreamble, "pgrep -g") {
		t.Errorf("the preamble lists a process group with pgrep -g, which matches group IDs")
	}
}

func TestClangDir(t *testing.T) {
	result := prepareKernel(allHostTools).RunTestWithBp(t, pongLike)
	script := kbuildScriptOf(t, result, "k")
	// Without clang_dir the directory cc/config's ClangPath resolves.
	if !strings.Contains(script, `CLANG="$T"/prebuilts/clang/host/linux-x86/clang-r584948`) {
		t.Errorf("without clang_dir the rule does not use the platform clang directory")
	}
	// clang_dir takes a directory anywhere below prebuilts/clang.
	for _, dir := range []string{"prebuilts/clang/host/linux-x86/clang-old", "prebuilts/clang/kernel/clang-k"} {
		result = prepareKernel(allHostTools).RunTestWithBp(t, strings.Replace(pongLike, `kernel_arch: "arm64",`,
			`kernel_arch: "arm64", clang_dir: "`+dir+`",`, 1))
		if cmd := result.ModuleForTests(t, "k", "android_common").Rule("kbuild").RuleParams.Command; !strings.Contains(cmd, dir+"/bin") {
			t.Errorf("clang_dir %s is not used", dir)
		}
	}
}

func TestGlobsOfTopLevelEntries(t *testing.T) {
	result := prepareKernel(allHostTools).RunTestWithBp(t, pongLike)
	script := kbuildScriptOf(t, result, "k")
	for _, e := range []string{"kernel/k/.scmversion", "kernel/k/Makefile", "kernel/k/arch", "kernel/k/drivers"} {
		if !strings.Contains(script, e) {
			t.Errorf("the entries of kernel/k lack %s", e)
		}
	}
}

func TestGitStateRule(t *testing.T) {
	result := prepareKernel(allHostTools).RunTestWithBp(t, pongLike)
	m := result.ModuleForTests(t, "k", "android_common")
	rule := m.Rule("git_state")
	cmd := rule.RuleParams.Command
	if !rule.RuleParams.Restat || !rule.RuleParams.SandboxDisabled {
		t.Errorf("the git state rule lacks Restat or SandboxDisabled")
	}
	if !strings.HasSuffix(rule.Depfile.String(), "/git_state.d") {
		t.Errorf("depfile: %v", rule.Depfile)
	}
	if !strings.HasSuffix(rule.Output.String(), "/git_state.txt") {
		t.Errorf("output: %v", rule.Output)
	}
	containsAll(t, "command", cmd, "TMPDIR=", "/tmp/git_state", "/git_state.sh)")
	containsAll(t, "inputs", strings.Join(rule.Implicits.Strings(), " "), "/git_state.sh")
	script := ruleScript(t, result, m, "git_state")
	checkShellSyntax(t, "git state", cmd, script)
	inOrder(t, "git state", script, "NAME=git_state", `LOCK="$MOD/$NAME.lock"`,
		`PGIDF="$MOD/$NAME.pgid"`, "START=", "rev-parse --show-toplevel", "is not the top of a git work tree", "log -1 --format='%H %ct'",
		"describe --abbrev=12", `stage "$OUT.tmp" "$OUT"`, "--absolute-git-dir", "--path-format=absolute --git-common-dir",
		`"$gitdir/HEAD"`, "packed-refs", `"$common/refs/heads" "$common/refs/tags"`,
		`write_depfile "$MOD/git_state.d" "$OUT"`, "check_unchanged")
	// git_state.txt is an input of the kbuild rule.
	if !strings.Contains(strings.Join(m.Rule("kbuild").Implicits.Strings(), " "), "git_state.txt") {
		t.Errorf("git_state.txt is no input of the kbuild rule")
	}
}

func TestDtRule(t *testing.T) {
	result := prepareKernel(allHostTools).RunTestWithBp(t, pongLike)
	m := result.ModuleForTests(t, "k", "android_common")
	manifest := android.RuleBuilderSboxProtoForTests(t, result.TestContext, m.Output("dt_images.sbox.textproto"))
	cmd := manifest.Commands[0].GetCommand()
	inOrder(t, "qcom", cmd, "export PATH=__SBOX_SANDBOX_DIR__/tools/out/bin:$PATH", "LC_ALL=C", "zipsync -d dts",
		"mkdir -p dts/base dts/out", "shopt -s nullglob", "dtbs=(dts/vendor/*/*.dtb)", `merge: "qcom": no *.dtb file directly inside vendor/*/. `+
			"Build the base device trees of the device below arch/arm64/boot/dts/vendor, or turn off the qcom merge in dt_images.merge.",
		`mv "${dtbs[@]}" dts/base/`, "dtbos=(dts/vendor/*/*.dtbo)", `merge: "qcom": no *.dtbo`, `mv "${dtbos[@]}" dts/base/`,
		"merge_dtbs --base dts/base --techpack dts/vendor/qcom --out dts/out",
		"kernel_output_files dt -dir dts/out -suffix .dtb -o dtb.list "+
			"-next-step 'Enable the device trees of the device in the kernel configuration, or remove dt_images.'",
		"kernel_output_files dt -dir dts/out -suffix .dtbo",
		"__SBOX_SANDBOX_DIR__/out/dtb.img", "mkdtboimg create __SBOX_SANDBOX_DIR__/out/dtbo.img", "--page_size=4096")
	staysInSandboxDir(t, "qcom", cmd)
	var copied []string
	for _, c := range manifest.Commands[0].CopyBefore {
		copied = append(copied, c.GetTo())
	}
	for _, tool := range append([]string{"mkdtboimg", "zipsync", "kernel_output_files", "merge_dtbs"}, qcomDtTools...) {
		if !android.InList("tools/out/bin/"+tool, copied) {
			t.Errorf("the sandbox lacks %s: %v", tool, copied)
		}
	}
	rule := m.Output("dt_images/dtb.img")
	if !rule.RuleParams.Restat {
		t.Errorf("the device tree image rule has no restat")
	}

	result = prepareKernel(allHostTools).RunTestWithBp(t, strings.Replace(pongLike, `merge: "qcom"`, `merge: "none"`, 1))
	m = result.ModuleForTests(t, "k", "android_common")
	manifest = android.RuleBuilderSboxProtoForTests(t, result.TestContext, m.Output("dt_images.sbox.textproto"))
	cmd = manifest.Commands[0].GetCommand()
	inOrder(t, "none", cmd, "zipsync -d dts", "kernel_output_files dt -dir dts -suffix .dtb",
		"kernel_output_files dt -dir dts -suffix .dtbo", "mkdtboimg create", "--page_size=4096")
	if strings.Contains(cmd, "merge_dtbs") {
		t.Errorf("merge none runs merge_dtbs")
	}
	staysInSandboxDir(t, "none", cmd)
}

// staysInSandboxDir fails for a device tree image command that changes its directory. sbox
// replaces the sandbox directory placeholder with `.`, so the PATH entry of the packaged tools and
// the output paths point into the sandbox only from the sandbox directory.
func staysInSandboxDir(t *testing.T, name, cmd string) {
	t.Helper()
	if regexp.MustCompile(`(^|[\s;&|({])(cd|pushd)\s`).MatchString(cmd) {
		t.Errorf("%s: the command changes its directory after naming the sandbox directory placeholder:\n%s", name, cmd)
	}
	if !strings.HasPrefix(cmd, "export PATH=__SBOX_SANDBOX_DIR__/tools/out/bin:$PATH ") {
		t.Errorf("%s: the command does not start with the PATH entry of the packaged tools:\n%s", name, cmd)
	}
}

func TestPropertyErrors(t *testing.T) {
	cases := []struct{ change, to, pattern string }{
		{`kernel_dir: "kernel/k",`, `kernel_dir: "kernel/k/",`, `kernel_dir: "kernel/k/" is not a clean relative path`},
		{`kernel_dir: "kernel/k",`, `kernel_dir: "../k",`, `kernel_dir: "../k" is not a clean relative path`},
		{`kernel_dir: "kernel/k",`, ``, `kernel_dir: is required`},
		{`kernel_arch: "arm64",`, `kernel_arch: "arm64x",`, `kernel_arch: "arm64x" is none of`},
		{`kernel_arch: "arm64",`, ``, `kernel_arch: is required`},
		{`image: "Image",`, `image: "../Image",`, `image: "../Image" is not a single path component`},
		{`image: "Image",`, `image: "boot/Image",`, `image: "boot/Image" is not a single path component`},
		{`image: "Image",`, ``, `image: is required`},
		{`kernel_arch: "arm64",`, `kernel_arch: "arm64", clang_dir: "../clang",`, `clang_dir: "../clang" is not a directory below prebuilts/clang. Set clang_dir`},
		{`kernel_arch: "arm64",`, `kernel_arch: "arm64", clang_dir: "prebuilts/clang",`, `clang_dir: "prebuilts/clang" is not a directory below prebuilts/clang`},
		{`kernel_arch: "arm64",`, `kernel_arch: "arm64", clang_dir: "prebuilts/clang-tools",`, `clang_dir: "prebuilts/clang-tools" is not a directory below prebuilts/clang`},
		{`kernel_arch: "arm64",`, `kernel_arch: "arm64", clang_dir: "prebuilts/clang/../gcc",`, `clang_dir: "prebuilts/clang/../gcc" is not a directory below prebuilts/clang`},
		{`defconfig: "gki_defconfig",`, `defconfig: "/gki",`, `defconfig: "/gki" is not a clean relative path`},
		{`defconfig: "gki_defconfig",`, ``, `defconfig: is required`},
		{`"two.config"`, `"one.config"`, `config_fragments: "one.config" is listed twice`},
		{`"two.config"`, `"./two.config"`, `config_fragments: "./two.config" is not a clean relative path`},
		{`extra_source_dirs: ["kernel/dts"],`, `extra_source_dirs: ["kernel"],`,
			`extra_source_dirs: "kernel" is, contains or lies inside kernel_dir "kernel/k". Remove it from extra_source_dirs.`},
		{`extra_source_dirs: ["kernel/dts"],`, `extra_source_dirs: ["kernel/mods/a"],`,
			`extra_source_dirs: "kernel/mods/a" is, contains or lies inside external_modules.root "kernel/mods". Remove it from extra_source_dirs.`},
		{`extra_source_dirs: ["kernel/dts"],`, `extra_source_dirs: ["kernel/dts", "kernel/dts/qcom"],`,
			`extra_source_dirs: "kernel/dts/qcom" lies inside "kernel/dts". Remove it from extra_source_dirs.`},
		{`extra_source_dirs: ["kernel/dts"],`, `extra_source_dirs: ["kernel/dts/qcom", "kernel/dts"],`,
			`extra_source_dirs: "kernel/dts/qcom" lies inside "kernel/dts". Remove it from extra_source_dirs.`},
		{`extra_source_dirs: ["kernel/dts"],`, `extra_source_dirs: ["kernel/dts", "kernel/dts"],`, `extra_source_dirs: "kernel/dts" is listed twice`},
		{`root: "kernel/mods",`, `root: "kernel/k/mods",`, `external_modules.root: "kernel/k/mods" is, contains or lies inside kernel_dir`},
		{`root: "kernel/mods",`, ``, `external_modules.root: is required`},
		{`dirs: ["a", "b/c"],`, `dirs: [],`, `external_modules.dirs: must not be empty`},
		{`dirs: ["a", "b/c"],`, `dirs: ["a", "a"],`, `external_modules.dirs: "a" is listed twice`},
		{`dirs: ["a", "b/c"],`, `dirs: ["a", "../b"],`, `external_modules.dirs: "../b" is not a clean relative path`},
		{`["wlan.ko:qca.ko"]`, `["wlan:qca.ko"]`, `module_renames: "wlan:qca.ko" is not <name>.ko:<name>.ko`},
		{`["wlan.ko:qca.ko"]`, `["a.ko:b.ko:c.ko"]`, `module_renames: "a.ko:b.ko:c.ko" is not <name>.ko:<name>.ko`},
		{`["wlan.ko:qca.ko"]`, `["a.ko:b.ko", "a.ko:c.ko"]`, `module_renames: "a.ko" is renamed twice`},
		{`merge: "qcom",`, `merge: "",`, `dt_images.merge: "" is neither`},
		{`merge: "qcom",`, `merge: "all",`, `dt_images.merge: "all" is neither`},
		{`merge: "qcom",`, ``, `dt_images.merge: is required`},
		{`dtbo_page_size: 4096,`, `dtbo_page_size: 0,`, `dt_images.dtbo_page_size: 0 is not a positive integer`},
		{`dtbo_page_size: 4096,`, ``, `dt_images.dtbo_page_size: is required`},
	}
	for _, named := range []bool{false, true} {
		for _, c := range cases {
			bp := strings.Replace(pongLike, c.change, c.to, 1)
			extra := []android.FixturePreparer{}
			if named {
				extra = append(extra, namedAs(":k"))
			}
			prepareKernel(allHostTools, extra...).
				ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(c.pattern))).
				RunTestWithBp(t, bp)
		}
	}
}

func TestMissingPaths(t *testing.T) {
	check := func(bp, want string, fs android.MockFS) {
		t.Helper()
		result := prepareKernelWithFS(allHostTools, fs).RunTestWithBp(t, bp)
		m := result.ModuleForTests(t, "k", "android_common")
		for _, out := range []string{"Image", "System.map", "modules.zip", "dtbs.zip", "dt_images/dtb.img", "dt_images/dtbo.img"} {
			p := m.Output(out)
			if !android.IsErrorRule(p.Rule) || !strings.Contains(p.Args["error"], want) {
				t.Errorf("%s: %v %q, want an ErrorRule naming %q", out, p.Rule, p.Args["error"], want)
			}
		}
		prepareKernelWithFS(allHostTools, fs, namedAs(":k")).
			ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(want))).
			RunTestWithBp(t, bp)
	}
	check(strings.Replace(pongLike, `kernel_dir: "kernel/k",`, `kernel_dir: "kernel/missing",`, 1),
		"kernel_dir: kernel/missing does not exist. Sync the kernel repository to that path, or correct kernel_dir.", kernelMockFS)
	check(strings.Replace(pongLike, `defconfig: "gki_defconfig",`, `defconfig: "missing_defconfig",`, 1),
		"defconfig: kernel/k/arch/arm64/configs/missing_defconfig does not exist", kernelMockFS)
	check(strings.Replace(pongLike, `extra_source_dirs: ["kernel/dts"],`, `extra_source_dirs: ["kernel/nodts"],`, 1),
		"extra_source_dirs: kernel/nodts does not exist", kernelMockFS)
	check(strings.Replace(pongLike, `dirs: ["a", "b/c"],`, `dirs: ["a", "z"],`, 1),
		"external_modules.dirs: kernel/mods/z does not exist", kernelMockFS)
	missingClang := strings.Replace(pongLike, `kernel_arch: "arm64",`, `kernel_arch: "arm64", clang_dir: "prebuilts/clang/kernel/clang-none",`, 1)
	check(missingClang, "clang_dir: prebuilts/clang/kernel/clang-none does not exist", kernelMockFS)
	// A module that is not named leaves a missing clang_dir to its error rules also where Soong
	// reports a source path that does not exist.
	strictPaths := android.FixtureModifyConfig(func(c android.Config) { c.TestAllowNonExistentPaths = false })
	prepareKernel(allHostTools, strictPaths).RunTestWithBp(t, missingClang)

	// A missing perl of prebuilts/tools-lineage.
	noPerl := android.MockFS{}
	for k, v := range kernelMockFS {
		if !strings.HasPrefix(k, "prebuilts/tools-lineage") {
			noPerl[k] = v
		}
	}
	check(pongLike, "prebuilts/tools-lineage is not in the tree", noPerl)
}

// TestSourceDirSteps checks that a module with next steps for a directory outside its source
// directories gives them in the read check of its kbuild rule, in its provider and in the messages
// about an entry of extra_source_dirs.
func TestSourceDirSteps(t *testing.T) {
	const add, remove = "Add a link to it.", "Remove the link to it."
	withSteps := android.FixtureRegisterWithContext(func(ctx android.RegistrationContext) {
		ctx.RegisterModuleType("kernel_build_with_steps",
			kernelBuildFactoryWithNames(settingNames{propExtraSourceDirs: "the links"}, sourceDirSteps{add: add, remove: remove}))
	})
	bp := strings.Replace(pongLike, "kernel_build {", "kernel_build_with_steps {", 1)
	result := prepareKernel(allHostTools, withSteps).RunTestWithBp(t, bp)
	m := result.ModuleForTests(t, "k", "android_common")
	if script := ruleScript(t, result, m, "kbuild"); !strings.Contains(script, "-next-step '"+add+"'") {
		t.Errorf("the read check of the kbuild rule does not get the next step %q", add)
	}
	info, _ := android.OtherModuleProvider(result.OtherModuleProviderAdaptor(), m.Module(), KernelSourcesInfoProvider)
	if info.AddSourceDirStep != add {
		t.Errorf("the provider gives the next step %q, want %q", info.AddSourceDirStep, add)
	}
	prepareKernel(allHostTools, withSteps).
		ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(
			regexp.QuoteMeta(`the links: "kernel/dts/qcom" lies inside "kernel/dts". `+remove))).
		RunTestWithBp(t, strings.Replace(bp, `extra_source_dirs: ["kernel/dts"],`, `extra_source_dirs: ["kernel/dts", "kernel/dts/qcom"],`, 1))
}

// TestSettingNames checks that a module with names reports a property with a name under that name,
// in a property check and as a missing path, and any other property under its own name.
func TestSettingNames(t *testing.T) {
	withNames := android.FixtureRegisterWithContext(func(ctx android.RegistrationContext) {
		ctx.RegisterModuleType("kernel_build_with_names",
			kernelBuildFactoryWithNames(settingNames{propKernelDir: "KERNEL_SOURCE", propImage: "IMAGE_NAME"}, sourceDirSteps{}))
	})
	bp := strings.Replace(pongLike, "kernel_build {", "kernel_build_with_names {", 1)
	for _, c := range []struct{ change, to, pattern string }{
		{`image: "Image",`, `image: "../Image",`, `IMAGE_NAME: "../Image" is not a single path component`},
		{`kernel_dir: "kernel/k",`, `kernel_dir: "kernel/missing",`, `KERNEL_SOURCE: kernel/missing does not exist`},
		{`defconfig: "gki_defconfig",`, `defconfig: "/gki",`, `defconfig: "/gki" is not a clean relative path`},
	} {
		prepareKernel(allHostTools, withNames, namedAs(":k")).
			ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(c.pattern))).
			RunTestWithBp(t, strings.Replace(bp, c.change, c.to, 1))
	}
}

func TestQcomToolsMissing(t *testing.T) {
	var tools []string
	for _, tool := range allHostTools {
		if tool != "fdtput" && tool != "fdtoverlaymerge" {
			tools = append(tools, tool)
		}
	}
	result := prepareKernel(tools).RunTestWithBp(t, pongLike)
	p := result.ModuleForTests(t, "k", "android_common").Output("Image")
	if !android.IsErrorRule(p.Rule) || !strings.Contains(p.Args["error"], `merge: "qcom" needs the host tools fdtoverlaymerge, fdtput`) ||
		!strings.Contains(p.Args["error"], "external/dtc fork of android_external_dtc") {
		t.Errorf("missing device tree tools: %q", p.Args["error"])
	}
	prepareKernel(tools, namedAs(":k")).
		ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(`merge: "qcom" needs the host tools fdtoverlaymerge, fdtput`)).
		RunTestWithBp(t, pongLike)
	// merge none needs none of them.
	prepareKernel(tools).RunTestWithBp(t, strings.Replace(pongLike, `merge: "qcom"`, `merge: "none"`, 1))
}

func withPartitionVars(f func(v *android.PartitionVariables)) android.FixturePreparer {
	return android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) {
		f(&v.PartitionVarsForSoongMigrationOnlyDoNotUse)
	})
}

var dtboSize = withPartitionVars(func(v *android.PartitionVariables) { v.BoardDtboPartitionSize = "25165824" })

// withDtbFlag is the bool Soong receives for BOARD_INCLUDE_DTB_IN_BOOTIMG: true for any value that
// is not empty once stripped, `1` included.
var withDtbFlag = withPartitionVars(func(v *android.PartitionVariables) { v.BoardIncludeDtbInBootimg = true })

func installsOf(t *testing.T, result *android.TestResult, name string) android.InstallFilesInfo {
	t.Helper()
	return android.GetInstallFiles(result.OtherModuleProviderAdaptor(), result.ModuleForTests(t, name, "android_common").Module())
}

func makeVarsOf(t *testing.T, result *android.TestResult, name string) []android.ModuleMakeVarsValue {
	t.Helper()
	return android.OtherModuleProviderOrDefault(result.OtherModuleProviderAdaptor(),
		result.ModuleForTests(t, name, "android_common").Module(), android.ModuleMakeVarsInfoProvider)
}

func TestNamedModule(t *testing.T) {
	result := prepareKernel(allHostTools, namedAs(":k"), dtboSize, withDtbFlag).RunTestWithBp(t, pongLike)
	installs := installsOf(t, result, "k")
	var got []string
	for _, f := range installs.InstallFiles {
		got = append(got, filepath.Base(f.String())+" in "+filepath.Base(filepath.Dir(f.String())))
	}
	sort.Strings(got)
	if strings.Join(got, ", ") != "dtb.img in test_device, kernel in test_device" {
		t.Errorf("installs: %v", installs.InstallFiles)
	}
	if len(installs.CheckbuildFiles) != 0 {
		t.Errorf("checkbuild files of the named module: %v", installs.CheckbuildFiles)
	}
	vars := makeVarsOf(t, result, "k")
	if len(vars) != 1 || vars[0].Name != "BOARD_PREBUILT_DTBOIMAGE" || !strings.HasSuffix(vars[0].Value, "dt_images/dtbo.img") {
		t.Errorf("make variables: %v", vars)
	}

	// Without BOARD_INCLUDE_DTB_IN_BOOTIMG no dtb.img is installed.
	result = prepareKernel(allHostTools, namedAs(":k"), dtboSize).RunTestWithBp(t, pongLike)
	if got := installsOf(t, result, "k").InstallFiles; len(got) != 1 || filepath.Base(got[0].String()) != "kernel" {
		t.Errorf("installs without the dtb flag: %v", got)
	}

	// Another module gets no install and no make variable.
	result = prepareKernel(allHostTools, namedAs(":other"), dtboSize).RunTestWithBp(t, pongLike)
	if got := installsOf(t, result, "k").InstallFiles; len(got) != 0 {
		t.Errorf("installs of an unnamed module: %v", got)
	}
	if vars := makeVarsOf(t, result, "k"); len(vars) != 0 {
		t.Errorf("make variables of an unnamed module: %v", vars)
	}

	// No dtbo make variable without dt_images.
	result = prepareKernel(allHostTools, namedAs(":k")).RunTestWithBp(t, noDtImages)
	if vars := makeVarsOf(t, result, "k"); len(vars) != 0 {
		t.Errorf("make variables without dt_images: %v", vars)
	}
}

func TestNamedModuleErrors(t *testing.T) {
	onX86 := android.FixtureModifyConfig(func(c android.Config) {
		c.Targets[android.Android] = []android.Target{{Os: android.Android, Arch: android.Arch{ArchType: android.X86_64, ArchVariant: "silvermont"}}}
	})
	cases := []struct {
		bp      string
		extra   []android.FixturePreparer
		pattern string
	}{
		{pongLike, nil, "dt_images: is set, but the board sets no BOARD_DTBOIMG_PARTITION_SIZE"},
		{noDtImages, []android.FixturePreparer{withDtbFlag}, "dt_images: is not set, but the board sets BOARD_INCLUDE_DTB_IN_BOOTIMG"},
		{noDtImages, []android.FixturePreparer{dtboSize}, "dt_images: is not set, but the board sets BOARD_DTBOIMG_PARTITION_SIZE, " +
			"which needs dtbo.img. Set dt_images, or remove BOARD_DTBOIMG_PARTITION_SIZE."},
		{pongLike, []android.FixturePreparer{dtboSize, onX86}, `kernel_arch: "arm64" does not fit the device's primary architecture x86_64`},
		{pongLike, []android.FixturePreparer{dtboSize, android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) {
			v.BoardKernelBinaries = []string{"kernel-5.10"}
		})}, "BOARD_KERNEL_BUILD_MODULE is set to this module, and BOARD_KERNEL_BINARIES"},
		{pongLike, []android.FixturePreparer{dtboSize, withPartitionVars(func(v *android.PartitionVariables) {
			v.ProductCopyFiles = []string{"device/k/kernel:kernel"}
		})}, `BOARD_KERNEL_BUILD_MODULE is set to this module, and the PRODUCT_COPY_FILES entry "device/k/kernel:kernel"`},
		{pongLike, []android.FixturePreparer{dtboSize, withDtbFlag, withPartitionVars(func(v *android.PartitionVariables) {
			v.ProductCopyFiles = []string{"device/k/dtb.img:dtb.img"}
		})}, `BOARD_KERNEL_BUILD_MODULE is set to this module, and the PRODUCT_COPY_FILES entry "device/k/dtb.img:dtb.img"`},
		{pongLike, []android.FixturePreparer{dtboSize, withDtbFlag, withPartitionVars(func(v *android.PartitionVariables) {
			v.BoardPrebuiltDtbDir = "device/k/dtbs"
		})}, "BOARD_KERNEL_BUILD_MODULE is set to this module, and BOARD_PREBUILT_DTBIMAGE_DIR (device/k/dtbs) makes a second dtb.img. " +
			"Remove BOARD_PREBUILT_DTBIMAGE_DIR."},
		{pongLike, []android.FixturePreparer{dtboSize, withPartitionVars(func(v *android.PartitionVariables) {
			v.BoardPrebuiltDtboImage = "device/k/dtbo.img"
		})}, "BOARD_KERNEL_BUILD_MODULE is set to this module, and BOARD_PREBUILT_DTBOIMAGE (device/k/dtbo.img) makes a second dtbo.img. " +
			"Remove BOARD_PREBUILT_DTBOIMAGE."},
		{noDtImages, []android.FixturePreparer{dtboSize, withPartitionVars(func(v *android.PartitionVariables) {
			v.BoardPrebuiltDtboImage = "device/k/dtbo.img"
		})}, "dt_images: is not set, and the board sets BOARD_PREBUILT_DTBOIMAGE (device/k/dtbo.img), but the dtbo partition " +
			"gets dtbo.img only from this module. Remove BOARD_PREBUILT_DTBOIMAGE, and set dt_images."},
	}
	for _, c := range cases {
		prepareKernel(allHostTools, append(c.extra, namedAs(":k"))...).
			ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(c.pattern))).
			RunTestWithBp(t, c.bp)
	}

	// An arm64 kernel fits a device with a 32-bit arm user space.
	onArm := android.FixtureModifyConfig(func(c android.Config) {
		c.Targets[android.Android] = []android.Target{{Os: android.Android, Arch: android.Arch{ArchType: android.Arm, ArchVariant: "armv7-a-neon"}}}
	})
	prepareKernel(allHostTools, namedAs(":k"), dtboSize, onArm).RunTestWithBp(t, pongLike)

	// A board without a dtbo partition can use a prebuilt dtbo.img next to a module without dt_images.
	prepareKernel(allHostTools, namedAs(":k"), withPartitionVars(func(v *android.PartitionVariables) {
		v.BoardPrebuiltDtboImage = "device/k/dtbo.img"
	})).RunTestWithBp(t, noDtImages)
}

func TestNamedModuleInNamespace(t *testing.T) {
	bp := android.MockFS{"device/x/Android.bp": []byte(`soong_namespace {}` + pongLike)}
	android.GroupFixturePreparers(prepareKernel(allHostTools, namedAs("//device/x:k"), dtboSize),
		android.PrepareForTestWithNamespace, bp.AddToFixture()).
		ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern("PRODUCT_SOONG_NAMESPACES")).
		RunTest(t)

	result := android.GroupFixturePreparers(prepareKernel(allHostTools, namedAs("//device/x:k"), dtboSize),
		android.PrepareForTestWithNamespace, bp.AddToFixture(),
		android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) { v.NamespacesToExport = []string{"device/x"} })).
		RunTest(t)
	if got := installsOf(t, result, "k").InstallFiles; len(got) != 1 {
		t.Errorf("installs of the named module in its namespace: %v", got)
	}
}

// phonyDeps gives the files a phony of the module names directly, as against Soong's own phonies,
// which name phony targets.
func phonyDeps(t *testing.T, result *android.TestResult, name, variant, phony string) []string {
	t.Helper()
	info := android.OtherModulePointerProviderOrDefault(result.OtherModuleProviderAdaptor(),
		result.ModuleForTests(t, name, variant).Module(), android.CommonModuleInfoProvider)
	if info.Phonies == nil {
		return nil
	}
	var deps []string
	for _, p := range info.Phonies.Phonies[phony] {
		deps = append(deps, p.String())
	}
	return deps
}

func hasFile(deps []string, suffix string) bool {
	for _, d := range deps {
		if strings.HasSuffix(d, suffix) {
			return true
		}
	}
	return false
}

func TestGoalPhonies(t *testing.T) {
	kati := android.FixtureModifyConfig(android.SetKatiEnabledForTests)
	result := prepareKernel(allHostTools, kati).RunTestWithBp(t, pongLike)
	deps := phonyDeps(t, result, "k", "android_common", "k")
	for _, out := range []string{"/Image", "/System.map", "/modules.zip", "/dtbs.zip", "/dt_images/dtb.img", "/dt_images/dtbo.img"} {
		if !hasFile(deps, out) {
			t.Errorf("with Kati the phony k lacks %s: %v", out, deps)
		}
	}

	result = prepareKernel(allHostTools).RunTestWithBp(t, pongLike)
	if deps := phonyDeps(t, result, "k", "android_common", "k"); hasFile(deps, "/Image") {
		t.Errorf("without Kati the module registers a phony k of its outputs: %v", deps)
	}

	bp := android.MockFS{"device/x/Android.bp": []byte(`soong_namespace {}` + pongLike)}
	result = android.GroupFixturePreparers(prepareKernel(allHostTools, kati), android.PrepareForTestWithNamespace, bp.AddToFixture()).RunTest(t)
	if deps := phonyDeps(t, result, "k", "android_common", "device.x-k"); !hasFile(deps, "/Image") {
		t.Errorf("with Kati a module in a namespace has no phony device.x-k: %v", deps)
	}
	if deps := phonyDeps(t, result, "k", "android_common", "k"); hasFile(deps, "/Image") {
		t.Errorf("a module in a namespace that is not exported registers the phony k: %v", deps)
	}
}
