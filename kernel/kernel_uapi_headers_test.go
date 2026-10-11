// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"regexp"
	"strings"
	"testing"

	"android/soong/android"
)

const pongHeaders = `
kernel_uapi_headers {
    name: "k-headers",
    kernel_build: ":k",
}
`

// The three global modules of the header export, as build/custom/kernel/Android.bp defines them
// without their license modules.
const globalHeaderModules = `
kernel_uapi_headers {
    name: "generated_kernel_includes",
}

cc_defaults {
    name: "generated_kernel_header_defaults",
    generated_headers: ["generated_kernel_includes"],
    export_generated_headers: ["generated_kernel_includes"],
    vendor_available: true,
    recovery_available: true,
}

cc_library_headers {
    name: "generated_kernel_headers",
    defaults: ["generated_kernel_header_defaults"],
}
`

func headersModule(t *testing.T, result *android.TestResult, name string) *kernelUapiHeaders {
	t.Helper()
	return result.ModuleForTests(t, name, "").Module().(*kernelUapiHeaders)
}

func TestHeaderRule(t *testing.T) {
	result := prepareKernel(allHostTools).RunTestWithBp(t, pongLike+pongHeaders)
	m := result.ModuleForTests(t, "k-headers", "")
	rule := m.Rule("kernel_headers")
	cmd := rule.RuleParams.Command
	if !rule.RuleParams.Restat || !rule.RuleParams.SandboxDisabled {
		t.Errorf("the header rule lacks Restat or SandboxDisabled")
	}
	if !strings.HasSuffix(rule.Output.String(), "k-headers/gen/headers.stamp") {
		t.Errorf("output: %v", rule.Output)
	}
	if !strings.HasSuffix(rule.Depfile.String(), "k-headers/gen.d") {
		t.Errorf("depfile: %v", rule.Depfile)
	}
	containsAll(t, "inputs", strings.Join(rule.Implicits.Strings(), " "), "build/custom/kernel/tools/clean_headers.sh",
		"prebuilts/build-tools/linux-x86/bin/make")

	if strings.Contains(strings.Split(cmd, "setsid bash")[0], "/tools:") {
		t.Errorf("the header rule has a tool directory of its own")
	}
	containsAll(t, "command", cmd, "TMPDIR=", "/tmp/headers", "/headers.sh)")
	containsAll(t, "inputs", strings.Join(rule.Implicits.Strings(), " "), "k-headers/headers.sh")
	script := ruleScript(t, result, m, "headers")
	checkShellSyntax(t, "header rule", cmd, script)
	inOrder(t, "header rule", script, "NAME=headers", `LOCK="$MOD/$NAME.lock"`,
		`PGIDF="$MOD/$NAME.pgid"`, `"$TOYBOX" flock -n 9`, `echo "$PGID" > "$PGIDF"`, "START=",
		`rm -rf "$MOD"/staging && mkdir -p "$MOD"/staging`, "J=$(getconf _NPROCESSORS_ONLN)",
		`"$MAKE" -j"$J" -C "$K" O="$MOD"/staging ARCH=arm64 LLVM=1 LLVM_IAS=1`, `"HOSTCFLAGS=--sysroot=$SYSROOT"`,
		`"HOSTLDFLAGS=--sysroot=$SYSROOT -fuse-ld=lld --rtlib=compiler-rt" PAHOLE= headers_install`,
		`"$KIC" headers -make "$MAKE" -staging "$MOD"/staging -next-step 'Add the directory that contains these files to extra_source_dirs.' `+
			`-allow "$T"/kernel/k -allow "$T"/kernel/dts -allow "$T"/kernel/mods`,
		`build/custom/kernel/tools/clean_headers.sh "$MOD"/staging`,
		`mkdir -p "$MOD"/gen/usr/include`, "rsync -rcn --delete --itemize-changes", `: > "$MOD"/gen.changed`,
		`rsync -rc --delete "$MOD"/staging/usr/include/ "$MOD"/gen/usr/include/`,
		`"$KIC" links -next-step 'Add the directory that contains these files to extra_source_dirs.' `+
			`-allow "$T"/kernel/k -allow "$T"/kernel/dts -allow "$T"/kernel/mods kernel/k/.scmversion`,
		`write_depfile "$MOD"/gen.d "$STAMP"`, "check_unchanged",
		`if [ ! -e "$STAMP" ] || [ -e "$MOD"/gen.changed ]; then touch "$STAMP"`, `rm -f "$MOD"/gen.changed`)
	containsAll(t, "script", script, `"$KIC" depfile -o "$d.tmp" -t "$t"`, `mv -f "$d.tmp" "$d"`)
	set := regexp.MustCompile(`\nset -- ([^\n]*)\nwrite_depfile "\$MOD"/gen.d`).FindStringSubmatch(script)
	if set == nil {
		t.Fatalf("no depfile entries")
	}
	entries := strings.Fields(set[1])
	for _, e := range []string{"kernel/k/.scmversion", "kernel/k/Makefile", "kernel/dts/qcom", "kernel/mods/a",
		"bionic/libc/kernel/tools", "prebuilts/clang/host/linux-x86/clang-stable",
		"prebuilts/clang/host/linux-x86/clang-r584948", "prebuilts/build-tools/linux-x86/bin",
		"prebuilts/build-tools/path/linux-x86", "prebuilts/gcc/linux-x86/host/x86_64-linux-glibc2.17-4.8/sysroot"} {
		if !android.InList(e, entries) {
			t.Errorf("the depfile entries lack %s: %v", e, entries)
		}
	}

	h := headersModule(t, result, "k-headers")
	var dirs []string
	for _, d := range h.GeneratedHeaderDirs() {
		dirs = append(dirs, strings.TrimPrefix(d.String(), h.stamp.String()[:strings.Index(h.stamp.String(), "k-headers/")]))
	}
	if strings.Join(dirs, " ") != "k-headers/gen/usr/include k-headers/gen/usr/include/audio" {
		t.Errorf("include directories: %v", dirs)
	}
	if deps := h.GeneratedTriggerDeps(); len(deps) != 1 || deps[0].String() != h.stamp.String() {
		t.Errorf("trigger dependencies: %v", deps)
	}
	if deps := h.GeneratedDeps(); len(deps) != 1 || deps[0].String() != h.stamp.String() {
		t.Errorf("generated dependencies: %v", deps)
	}
}

func TestHeaderErrorRules(t *testing.T) {
	// Without BOARD_KERNEL_BUILD_MODULE the global module fails when it is built.
	result := prepareKernel(allHostTools).RunTestWithBp(t, globalHeaderModules)
	p := result.ModuleForTests(t, "generated_kernel_includes", "").Output("gen/headers.stamp")
	if !android.IsErrorRule(p.Rule) || !strings.Contains(p.Args["error"], "no kernel module is configured") ||
		!strings.Contains(p.Args["error"], "Set TARGET_KERNEL_SOURCE and TARGET_KERNEL_CONFIG in BoardConfig.mk, "+
			"or write a kernel_build module and set BOARD_KERNEL_BUILD_MODULE to it.") {
		t.Errorf("without a named module: %v %q", p.Rule, p.Args["error"])
	}

	// A kernel_build module with a missing path gives the header module its message.
	bp := strings.Replace(pongLike, `kernel_dir: "kernel/k",`, `kernel_dir: "kernel/missing",`, 1) + pongHeaders
	result = prepareKernel(allHostTools).RunTestWithBp(t, bp)
	p = result.ModuleForTests(t, "k-headers", "").Output("gen/headers.stamp")
	if !android.IsErrorRule(p.Rule) || !strings.Contains(p.Args["error"], "kernel_dir: kernel/missing does not exist") {
		t.Errorf("with a missing path: %v %q", p.Rule, p.Args["error"])
	}

	// A missing perl reaches the kernel module and not the header module.
	noPerl := android.MockFS{}
	for k, v := range kernelMockFS {
		if !strings.HasPrefix(k, "prebuilts/tools-lineage") {
			noPerl[k] = v
		}
	}
	result = prepareKernelWithFS(allHostTools, noPerl).RunTestWithBp(t, pongLike+pongHeaders)
	if p := result.ModuleForTests(t, "k-headers", "").Output("gen/headers.stamp"); android.IsErrorRule(p.Rule) {
		t.Errorf("a missing perl reached the header module: %q", p.Args["error"])
	}

	// The kernel_build property naming a module of another type.
	prepareKernel(allHostTools).
		ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(
			regexp.QuoteMeta(`kernel_build: ":generated_kernel_headers": leads to a module that is not a kernel_build module`))).
		RunTestWithBp(t, globalHeaderModules+`
kernel_uapi_headers {
    name: "wrong",
    kernel_build: ":generated_kernel_headers",
}
`)
}

// testCommonModule is a module of another type with the one variant a kernel_build module has.
type testCommonModule struct {
	android.ModuleBase
}

func testCommonModuleFactory() android.Module {
	m := &testCommonModule{}
	android.InitAndroidArchModule(m, android.DeviceSupported, android.MultilibCommon)
	return m
}

func (m *testCommonModule) GenerateAndroidBuildActions(ctx android.ModuleContext) {}

func TestBoardKernelBuildModuleChecks(t *testing.T) {
	inNamespace := android.MockFS{
		"device/x/Android.bp": []byte("soong_namespace {}\n" + pongLike),
		"device/x/sub/Android.bp": []byte(`
cc_library_headers {
    name: "not_a_kernel",
}
`),
	}
	exported := android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) {
		v.NamespacesToExport = []string{"device/x"}
	})
	run := func(ref, pattern string, extraBp string) {
		t.Helper()
		preparer := android.GroupFixturePreparers(prepareKernel(allHostTools, namedAs(ref), dtboSize, withDtbFlag, exported),
			android.PrepareForTestWithNamespace, inNamespace.AddToFixture(),
			android.FixtureRegisterWithContext(func(ctx android.RegistrationContext) {
				ctx.RegisterModuleType("test_common_module", testCommonModuleFactory)
			}))
		if pattern != "" {
			preparer = preparer.ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(pattern)))
		}
		preparer.RunTestWithBp(t, globalHeaderModules+extraBp)
	}
	run("device/x:k", `BOARD_KERNEL_BUILD_MODULE "device/x:k": is not of the form //<path>:<name> or :<name>`, "")
	run("//device/x:none", `BOARD_KERNEL_BUILD_MODULE "//device/x:none": leads to no module`, "")
	run(":generated_kernel_headers", `BOARD_KERNEL_BUILD_MODULE ":generated_kernel_headers": leads to a module that is not a kernel_build module`, "")
	// A module of another type that has the variant of a kernel_build module.
	run(":other_type", `BOARD_KERNEL_BUILD_MODULE ":other_type": leads to other_type, a test_common_module module, not a kernel_build module`,
		"test_common_module { name: \"other_type\" }\n")
	run(":off", `BOARD_KERNEL_BUILD_MODULE ":off": leads to the disabled module off`,
		strings.Replace(strings.Replace(pongLike, `name: "k",`, `name: "off", enabled: false,`, 1), "kernel_build {", "kernel_build {", 1))
	// A path below the module's namespace reaches the module through the nearest namespace, and the
	// qualified name differs from the value.
	run("//device/x/sub:k", `BOARD_KERNEL_BUILD_MODULE "//device/x/sub:k": leads to the kernel_build module by another path than its qualified name //device/x:k`, "")

	// The qualified name passes, and the header module then has its rule.
	result := android.GroupFixturePreparers(prepareKernel(allHostTools, namedAs("//device/x:k"), dtboSize, withDtbFlag, exported),
		android.PrepareForTestWithNamespace, inNamespace.AddToFixture()).RunTestWithBp(t, globalHeaderModules)
	script := ruleScript(t, result, result.ModuleForTests(t, "generated_kernel_includes", ""), "headers")
	if !strings.Contains(script, `K="$T"/kernel/k`) {
		t.Errorf("the header rule does not export the named kernel's headers")
	}
}

// TestHeaderUsers checks the route of the headers to cc modules of every image: a vendor and a
// recovery module that use generated_kernel_headers compile against the export directory, with
// the stamp as an implicit input of their compile rules.
func TestHeaderUsers(t *testing.T) {
	bp := globalHeaderModules + `
cc_library_static {
    name: "libvendor_user",
    vendor: true,
    srcs: ["user.c"],
    header_libs: ["generated_kernel_headers"],
}

cc_library_static {
    name: "librecovery_user",
    recovery: true,
    srcs: ["user.c"],
    header_libs: ["generated_kernel_headers"],
}
`
	result := android.GroupFixturePreparers(prepareKernel(allHostTools, namedAs(":k"), dtboSize, withDtbFlag),
		android.FixtureMergeMockFs(android.MockFS{"user.c": nil})).RunTestWithBp(t, pongLike+bp)
	for _, v := range []struct{ name, variant string }{
		{"libvendor_user", "android_vendor_arm64_armv8-a_static"},
		{"librecovery_user", "android_recovery_arm64_armv8-a_static"},
	} {
		compile := result.ModuleForTests(t, v.name, v.variant).Output("obj/user.o")
		if !strings.Contains(strings.Join(compile.Implicits.Strings(), " "), "generated_kernel_includes/gen/headers.stamp") {
			t.Errorf("%s: the stamp is no implicit input of the compile rule: %v", v.name, compile.Implicits)
		}
		if !strings.Contains(compile.Args["cFlags"], "generated_kernel_includes/gen/usr/include") {
			t.Errorf("%s: the export directory is no include directory: %s", v.name, compile.Args["cFlags"])
		}
	}
}

func TestHeaderGoalPhonies(t *testing.T) {
	kati := android.FixtureModifyConfig(android.SetKatiEnabledForTests)
	result := prepareKernel(allHostTools, kati, namedAs(":k"), dtboSize, withDtbFlag).RunTestWithBp(t, pongLike+globalHeaderModules)
	if deps := phonyDeps(t, result, "generated_kernel_includes", "", "generated_kernel_includes"); !hasFile(deps, "/gen/headers.stamp") {
		t.Errorf("with Kati no phony generated_kernel_includes of the stamp: %v", deps)
	}
	result = prepareKernel(allHostTools, namedAs(":k"), dtboSize, withDtbFlag).RunTestWithBp(t, pongLike+globalHeaderModules)
	if deps := phonyDeps(t, result, "generated_kernel_includes", "", "generated_kernel_includes"); hasFile(deps, "/gen/headers.stamp") {
		t.Errorf("without Kati a phony generated_kernel_includes of the stamp: %v", deps)
	}
	// The stamp is no checkbuild file.
	m := result.ModuleForTests(t, "generated_kernel_includes", "").Module()
	if files := android.GetInstallFiles(result.OtherModuleProviderAdaptor(), m).CheckbuildFiles; len(files) != 0 {
		t.Errorf("checkbuild files of the header module: %v", files)
	}
}
