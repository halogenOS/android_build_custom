// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"encoding/json"
	"reflect"
	"regexp"
	"testing"

	"android/soong/android"
	aospkernel "android/soong/kernel"
)

const replacingModule = `
kernel_partition_modules {
    name: "k-vendor_dlkm",
    vendor_dlkm_specific: true,
    zip: {
        src: ":k{.modules}",
        install_all: true,
    },
    system_map: ":k{.system_map}",
}
`

func runReplaced(t *testing.T, modsBp, pattern string, extra ...android.FixturePreparer) *android.TestResult {
	t.Helper()
	preparer := android.GroupFixturePreparers(append([]android.FixturePreparer{
		prepareKernel(allHostTools),
		aospkernel.PrepareForTestWithPrebuiltKernelModules,
		android.PrepareForTestWithNamespace,
		android.FixtureMergeMockFs(android.MockFS{"mods/a.ko": nil}),
		android.FixtureAddTextFile("mods/Android.bp", modsBp),
	}, extra...)...)
	if pattern != "" {
		preparer = preparer.ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(pattern))
	}
	return preparer.RunTestWithBp(t, pongLike)
}

func TestReplacedInstallDirs(t *testing.T) {
	// Two kernel_partition_modules modules on different partitions.
	runReplaced(t, replacingModule+`
kernel_partition_modules {
    name: "k-vendor_ramdisk",
    vendor_ramdisk: true,
    zip: {
        src: ":k{.modules}",
        install_all: true,
    },
    system_map: ":k{.system_map}",
}
`, "")

	// A second kernel_partition_modules module with the same install directory.
	runReplaced(t, replacingModule+`
kernel_partition_modules {
    name: "k-vendor_dlkm-again",
    vendor_dlkm_specific: true,
    zip: {
        src: ":k{.modules}",
        install_all: true,
    },
    system_map: ":k{.system_map}",
}
`, `k-vendor_dlkm .* replaces the install directory .*/vendor_dlkm/lib/modules as a whole, and k-vendor_dlkm-again .* extracts its install zip into .*/vendor_dlkm/lib/modules`)

	// A prebuilt_kernel_modules module with the same install directory.
	runReplaced(t, replacingModule+`
prebuilt_kernel_modules {
    name: "aosp-modules",
    vendor_dlkm_specific: true,
    srcs: ["a.ko"],
}
`, `k-vendor_dlkm .* replaces the install directory .*/vendor_dlkm/lib/modules as a whole, and aosp-modules .* extracts its install zip into`)

	// A PRODUCT_COPY_FILES destination below the replaced directory.
	copyBlocklist := android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) {
		v.PartitionVarsForSoongMigrationOnlyDoNotUse.ProductCopyFiles = []string{"device/x/modules.blocklist:vendor_dlkm/lib/modules/modules.blocklist"}
	})
	runReplaced(t, replacingModule, regexp.QuoteMeta(`the PRODUCT_COPY_FILES entry "device/x/modules.blocklist:vendor_dlkm/lib/modules/modules.blocklist" copies a file there`),
		copyBlocklist)

	// A prebuilt kernel module copied below the replaced directory gets the next step to build it
	// from the kernel source.
	copyModule := android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) {
		v.PartitionVarsForSoongMigrationOnlyDoNotUse.ProductCopyFiles = []string{"device/x/a.ko:vendor_dlkm/lib/modules/a.ko"}
	})
	runReplaced(t, replacingModule, regexp.QuoteMeta(`the PRODUCT_COPY_FILES entry "device/x/a.ko:vendor_dlkm/lib/modules/a.ko" `+
		"copies a file there. Remove the entry. Build the kernel module from the kernel source instead: enable it in the kernel "+
		"configuration, or build it as an external module."), copyModule)
	runReplaced(t, replacingModule, regexp.QuoteMeta("copies a file there. Remove the entry, or copy the file into another directory."),
		copyBlocklist)

	// The same destination and a module in a namespace that is not exported: the module does not
	// install, so nothing is replaced.
	runReplaced(t, "soong_namespace { imports: [\".\"] }\n"+replacingModule, "", copyBlocklist,
		android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) { v.NamespacesToExport = []string{} }))
}

// replacingTestModule is a module of another type than kernel_partition_modules whose install
// replaces its directory as a whole, so that the singleton is seen to go by the install and not by
// the module type.
type replacingTestModule struct {
	android.ModuleBase
}

func replacingTestModuleFactory() android.Module {
	m := &replacingTestModule{}
	android.InitAndroidArchModule(m, android.DeviceSupported, android.MultilibCommon)
	return m
}

func (m *replacingTestModule) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	primary := android.PathForModuleOut(ctx, "primary.txt")
	android.WriteFileRule(ctx, primary, "primary")
	extraZip := android.PathForModuleOut(ctx, "extra.zip")
	android.WriteFileRule(ctx, extraZip, "zip")
	ctx.InstallFileWithExtraFilesZipReplacingDir(android.PathForModuleInPartitionInstall(ctx, "vendor_dlkm", "lib", "modules"),
		"primary.txt", primary, extraZip)
}

func TestReplacedInstallDirsOtherType(t *testing.T) {
	withType := android.FixtureRegisterWithContext(func(ctx android.RegistrationContext) {
		ctx.RegisterModuleType("replacing_test_module", replacingTestModuleFactory)
	})
	// A module of another type that replaces the same directory is reported like a second
	// kernel_partition_modules module.
	runReplaced(t, replacingModule+`
replacing_test_module {
    name: "other-replacing",
}
`, `k-vendor_dlkm .* replaces the install directory .*/vendor_dlkm/lib/modules as a whole, and other-replacing .* extracts its install zip into .*/vendor_dlkm/lib/modules`,
		withType)

	// Alone, it enters the list.
	result := runReplaced(t, `
replacing_test_module {
    name: "other-replacing",
}
`, "", withType)
	s := result.SingletonForTests(t, "replaced_install_dirs").Singleton().(*replacedInstallDirs)
	want := []replacedDirEntry{{Primary: "vendor_dlkm/lib/modules/primary.txt", Dir: "vendor_dlkm/lib/modules"}}
	if !reflect.DeepEqual(s.entriesForTesting, want) {
		t.Errorf("entries of the singleton: %v", s.entriesForTesting)
	}
}

func TestReplacedDirEntries(t *testing.T) {
	out := "out/target/product/dev"
	got := replacedDirEntries(out, []replacedDir{
		{module: "b", primary: out + "/vendor_ramdisk/lib/modules/modules.dep", dir: out + "/vendor_ramdisk/lib/modules"},
		{module: "a", primary: out + "/vendor_dlkm/lib/modules/modules.load", dir: out + "/vendor_dlkm/lib/modules"},
		{module: "c", primary: "out/host/linux-x86/lib/modules/modules.load", dir: "out/host/linux-x86/lib/modules"},
	})
	want := []replacedDirEntry{
		{Primary: "vendor_dlkm/lib/modules/modules.load", Dir: "vendor_dlkm/lib/modules"},
		{Primary: "vendor_ramdisk/lib/modules/modules.dep", Dir: "vendor_ramdisk/lib/modules"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// The JSON soong_ui reads: an array of objects with the keys primary and dir, and an empty
	// array without an entry.
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if wantJSON := `[{"primary":"vendor_dlkm/lib/modules/modules.load","dir":"vendor_dlkm/lib/modules"},` +
		`{"primary":"vendor_ramdisk/lib/modules/modules.dep","dir":"vendor_ramdisk/lib/modules"}]`; string(data) != wantJSON {
		t.Errorf("JSON: %s", data)
	}
	if data, _ := json.Marshal(replacedDirEntries(out, nil)); string(data) != "[]" {
		t.Errorf("JSON without an entry: %s", data)
	}

	// The singleton keeps the entries of the product's replaced directories.
	result := runReplaced(t, replacingModule, "")
	s := result.SingletonForTests(t, "replaced_install_dirs").Singleton().(*replacedInstallDirs)
	if !reflect.DeepEqual(s.entriesForTesting, []replacedDirEntry{{Primary: "vendor_dlkm/lib/modules/modules.dep", Dir: "vendor_dlkm/lib/modules"}}) {
		t.Errorf("entries of the singleton: %v", s.entriesForTesting)
	}
}
