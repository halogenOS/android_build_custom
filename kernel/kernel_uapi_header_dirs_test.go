// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"android/soong/android"
)

// pongHeaderDirs exports two folders of k-headers, the one outside usr/include first.
const pongHeaderDirs = `
kernel_uapi_header_dirs {
    name: "k-dirs",
    headers: "k-headers",
    dirs: ["extra/include", "include"],
}
`

// TestHeaderDirs checks that the module exports the listed folders below usr of the header module
// in the order listed, depends on the stamp of the header module and does not define a header rule.
// It also checks that a vendor module using the folders through a cc_library_headers module
// compiles against them, with the stamp as an implicit input of its compile rule.
func TestHeaderDirs(t *testing.T) {
	bp := pongLike + pongHeaders + pongHeaderDirs + `
cc_library_headers {
    name: "k-dirs-headers",
    generated_headers: ["k-dirs"],
    export_generated_headers: ["k-dirs"],
    vendor_available: true,
}

cc_library_static {
    name: "libvendor_user",
    vendor: true,
    srcs: ["user.c"],
    header_libs: ["k-dirs-headers"],
}
`
	result := android.GroupFixturePreparers(prepareKernel(allHostTools),
		android.FixtureMergeMockFs(android.MockFS{"user.c": nil})).RunTestWithBp(t, bp)
	h := headersModule(t, result, "k-headers")
	m := result.ModuleForTests(t, "k-dirs", "")
	d := m.Module().(*kernelUapiHeaderDirs)
	if got, want := exportedDirs(d, h), []string{"extra/include", "include"}; !reflect.DeepEqual(got, want) {
		t.Errorf("include directories: %q, want %q", got, want)
	}
	for name, deps := range map[string]android.Paths{"generated": d.GeneratedDeps(), "trigger": d.GeneratedTriggerDeps()} {
		if len(deps) != 1 || deps[0].String() != h.stamp.String() {
			t.Errorf("%s dependencies: %v", name, deps)
		}
	}
	if rule := m.MaybeRule("kernel_headers"); rule.Rule != nil {
		t.Errorf("the module installs the headers again: %v", rule.Output)
	}

	checkCompileUses(t, result, "libvendor_user", "android_vendor_arm64_armv8-a_static", "k-headers/gen", "extra/include")
}

// TestHeaderDirsGoalPhony checks that, with the Make-based build (Kati), the module registers a make
// target named after itself for the stamp of the header module.
func TestHeaderDirsGoalPhony(t *testing.T) {
	kati := android.FixtureModifyConfig(android.SetKatiEnabledForTests)
	result := prepareKernel(allHostTools, kati).RunTestWithBp(t, pongLike+pongHeaders+pongHeaderDirs)
	if deps := phonyDeps(t, result, "k-dirs", "", "k-dirs"); !hasFile(deps, "/gen/headers.stamp") {
		t.Errorf("with Kati the make target k-dirs does not depend on the stamp of the header module: %v", deps)
	}
}

func TestHeaderDirsErrors(t *testing.T) {
	cases := []struct{ from, to, pattern string }{
		{`dirs: ["extra/include", "include"],`, `dirs: ["include", "include"],`, `dirs: "include" is listed twice. Remove the second entry from dirs.`},
		{`dirs: ["extra/include", "include"],`, `dirs: ["include", "../include"],`, `dirs: "../include" is not a clean relative path`},
		{`dirs: ["extra/include", "include"],`, `dirs: [],`, `dirs: is empty`},
		{`headers: "k-headers",`, ``, `headers: is required`},
		{`headers: "k-headers",`, `headers: "other-dirs",`,
			`headers: "other-dirs" is a kernel_uapi_header_dirs module, not a kernel_uapi_headers module`},
		{`headers: "k-headers",`, `headers: "k",`, `headers: "k" is not a kernel_uapi_headers module`},
		{`headers: "k-headers",`, `headers: "undefined",`, `headers: "undefined" leads to no module`},
	}
	otherDirs := strings.Replace(pongHeaderDirs, `name: "k-dirs",`, `name: "other-dirs",`, 1)
	for _, c := range cases {
		t.Run(c.to, func(t *testing.T) {
			bp := pongLike + pongHeaders + otherDirs + strings.Replace(pongHeaderDirs, c.from, c.to, 1)
			prepareKernel(allHostTools).
				ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(c.pattern))).
				RunTestWithBp(t, bp)
		})
	}
}
