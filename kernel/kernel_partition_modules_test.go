// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"regexp"
	"strings"
	"testing"

	"android/soong/android"

	"github.com/google/blueprint/proptools"
)

var partitionModulesFS = android.MockFS{
	"mods/modules.load":             nil,
	"mods/modules.load.vendor_boot": nil,
	"mods/modules.load.recovery":    nil,
	"mods/modules.blocklist":        nil,
	"mods/modules.boot":             nil,
}

const partitionModules = `
kernel_partition_modules {
    name: "k-vendor_dlkm",
    vendor_dlkm_specific: true,
    zip: {
        src: ":k{.modules}",
        install_all: true,
    },
    load_list: "modules.load",
    blocklist_file: "modules.blocklist",
    system_map: ":k{.system_map}",
}

kernel_partition_modules {
    name: "k-vendor_ramdisk",
    vendor_ramdisk: true,
    zip: {
        src: ":k{.modules}",
    },
    recovery_load_list: "modules.load.recovery",
    install_list: "modules.boot",
    system_map: ":k{.system_map}",
}
`

func preparePartitionModules(extra ...android.FixturePreparer) android.FixturePreparer {
	return android.GroupFixturePreparers(prepareKernel(allHostTools, extra...),
		android.FixtureMergeMockFs(partitionModulesFS),
		android.FixtureAddTextFile("mods/Android.bp", partitionModules))
}

func sboxCommand(t *testing.T, result *android.TestResult, name string) string {
	t.Helper()
	m := result.ModuleForTests(t, name, "android_arm64_armv8-a")
	manifest := android.RuleBuilderSboxProtoForTests(t, result.TestContext, m.Output("sbox.manifest"))
	return manifest.Commands[0].GetCommand()
}

func TestPartitionModulesRule(t *testing.T) {
	result := preparePartitionModules().RunTestWithBp(t, pongLike)
	cmd := sboxCommand(t, result, "k-vendor_dlkm")
	inOrder(t, "vendor_dlkm", cmd,
		"kernel_partition_modules_builder select", "-zip __SBOX_SANDBOX_DIR__/out/soong/.intermediates/k/android_common/modules.zip",
		"-system-map __SBOX_SANDBOX_DIR__/out/soong/.intermediates/k/android_common/System.map", "-depmod", "bin/depmod",
		"-temp __SBOX_SANDBOX_DIR__/out/select", "-o __SBOX_SANDBOX_DIR__/out/selected.zip", "-load-list mods/modules.load",
		"-load-list-name load_list", "-install-all",
		" && ", "kernel_modules_builder --soong_zip", "--zipsync", "--merge_zips", "--depmod", "--llvm-strip",
		"bin/llvm-strip", "props.json vendor_dlkm __SBOX_SANDBOX_DIR__/out/temp",
		"__SBOX_SANDBOX_DIR__/out/builder/modules.load __SBOX_SANDBOX_DIR__/out/builder/installs.zip",
		" && ", "kernel_partition_modules_builder finish -installs __SBOX_SANDBOX_DIR__/out/builder/installs.zip",
		"-o __SBOX_SANDBOX_DIR__/out/installs.zip -primary __SBOX_SANDBOX_DIR__/out/modules.load",
		"-load-list mods/modules.load")
	if strings.Contains(cmd, "-recovery-load-list") || strings.Contains(cmd, "-allow-empty") {
		t.Errorf("the vendor_dlkm module passes a recovery list or allows an empty zip")
	}

	m := result.ModuleForTests(t, "k-vendor_dlkm", "android_arm64_armv8-a")
	props := android.ContentFromFileRuleForTests(t, result.TestContext, m.Output("props.json"))
	for _, want := range []string{`"Src":"out/selected.zip"`, `"Load_file":"modules.install"`, `"Blocklist_file":"mods/modules.blocklist"`} {
		if !strings.Contains(props, want) {
			t.Errorf("props.json %s lacks %s", props, want)
		}
	}

	cmd = sboxCommand(t, result, "k-vendor_ramdisk")
	inOrder(t, "vendor_ramdisk", cmd, "select", "-recovery-load-list mods/modules.load.recovery",
		"-recovery-load-list-name recovery_load_list", "-install-list mods/modules.boot", "-install-list-name install_list",
		"kernel_modules_builder", "vendor_ramdisk", "finish", "-primary __SBOX_SANDBOX_DIR__/out/modules.dep",
		"-recovery-load-list mods/modules.load.recovery")
	if strings.Contains(cmd, "-install-all") || strings.Contains(cmd, "-load-list mods/modules.load ") {
		t.Errorf("the vendor_ramdisk module passes install_all or a load list")
	}
	// Only the selection reads the install list, since its modules are not written to modules.load.
	if n := strings.Count(cmd, "-install-list "); n != 1 {
		t.Errorf("the vendor_ramdisk rule passes the install list %d times, want once", n)
	}
}

func primaryInstall(t *testing.T, result *android.TestResult, name string) string {
	t.Helper()
	installs := installsOfVariant(t, result, name, "android_arm64_armv8-a").InstallFiles
	if len(installs) != 1 {
		t.Fatalf("%s installs %v, want one file", name, installs)
	}
	return installs[0].String()
}

func installsOfVariant(t *testing.T, result *android.TestResult, name, variant string) android.InstallFilesInfo {
	t.Helper()
	return android.GetInstallFiles(result.OtherModuleProviderAdaptor(), result.ModuleForTests(t, name, variant).Module())
}

func TestPartitionModulesInstall(t *testing.T) {
	result := preparePartitionModules().RunTestWithBp(t, pongLike)
	if got := primaryInstall(t, result, "k-vendor_dlkm"); !strings.HasSuffix(got, "/vendor_dlkm/lib/modules/modules.load") {
		t.Errorf("primary install with a load list: %s", got)
	}
	if got := primaryInstall(t, result, "k-vendor_ramdisk"); !strings.HasSuffix(got, "/vendor_ramdisk/lib/modules/modules.dep") {
		t.Errorf("primary install without a load list: %s", got)
	}
	for _, name := range []string{"k-vendor_dlkm", "k-vendor_ramdisk"} {
		if files := installsOfVariant(t, result, name, "android_arm64_armv8-a").CheckbuildFiles; len(files) != 0 {
			t.Errorf("%s has checkbuild files: %v", name, files)
		}
	}

	// Without Kati the install rule replaces the directory: zipsync without
	// --keep-existing-files, then the primary file through a temporary file.
	m := result.ModuleForTests(t, "k-vendor_dlkm", "android_arm64_armv8-a")
	install := m.Output(primaryInstall(t, result, "k-vendor_dlkm"))
	if install.Rule.String() != "android.CpReplacingDirWithBash" && !strings.HasSuffix(install.Rule.String(), "CpReplacingDirWithBash") {
		t.Errorf("install rule: %v", install.Rule)
	}
	zipSync := install.Args["zipSyncCmd"]
	if !strings.Contains(zipSync, "zipsync -d ") || strings.Contains(zipSync, "--keep-existing-files") ||
		!strings.Contains(zipSync, "sbox/installs.zip") {
		t.Errorf("zipsync command: %q", zipSync)
	}

	// With kernel_version the install directory is a directory of its own.
	bp := strings.Replace(partitionModules, `recovery_load_list: "modules.load.recovery",`,
		`recovery_load_list: "modules.load.recovery", kernel_version: "kv",`, 1)
	result = android.GroupFixturePreparers(prepareKernel(allHostTools), android.FixtureMergeMockFs(partitionModulesFS),
		android.FixtureAddTextFile("mods/Android.bp", bp)).RunTestWithBp(t, pongLike)
	if got := primaryInstall(t, result, "k-vendor_ramdisk"); !strings.HasSuffix(got, "/vendor_ramdisk/lib/modules/kv/modules.dep") {
		t.Errorf("install with kernel_version: %s", got)
	}
}

// TestPartitionModulesBootRamdisk checks that a module with ramdisk: true installs into lib/modules
// at the top of the boot ramdisk, where core/Makefile installs the modules of the boot ramdisk, and
// that AOSP's builder writes its modules.dep with the paths of a ramdisk.
func TestPartitionModulesBootRamdisk(t *testing.T) {
	bp := `kernel_partition_modules { name: "k-ramdisk", ramdisk: true, zip: { src: ":k{.modules}" },
		load_list: "modules.load.vendor_boot", system_map: ":k{.system_map}" }`
	result := android.GroupFixturePreparers(prepareKernel(allHostTools), android.FixtureMergeMockFs(partitionModulesFS),
		android.FixtureAddTextFile("mods/Android.bp", bp)).RunTestWithBp(t, pongLike)
	if got := primaryInstall(t, result, "k-ramdisk"); !strings.HasSuffix(got, "/target/product/test_device/ramdisk/lib/modules/modules.load") {
		t.Errorf("the boot ramdisk module installs %s", got)
	}
	inOrder(t, "ramdisk", sboxCommand(t, result, "k-ramdisk"), "kernel_modules_builder", "props.json vendor_ramdisk ")
}

func TestPartitionModulesErrors(t *testing.T) {
	cases := []struct{ bp, pattern string }{
		{`kernel_partition_modules { name: "x", vendor_dlkm_specific: true, zip: { src: ":k{.modules}" },
			recovery_load_list: "modules.load.recovery", system_map: ":k{.system_map}" }`,
			"recovery_load_list: is only for a module with vendor_ramdisk: true"},
		{`kernel_partition_modules { name: "x", vendor_dlkm_specific: true, zip: { src: ":k{.modules}" },
			system_map: ":k{.system_map}" }`,
			"zip.install_all: none of zip.install_all, load_list, recovery_load_list and install_list is set"},
		{`kernel_partition_modules { name: "x", vendor_dlkm_specific: true, zip: { src: ":k{.modules}", install_all: true } }`,
			"system_map: is required"},
		{`kernel_partition_modules { name: "x", vendor_dlkm_specific: true, zip: { install_all: true },
			system_map: ":k{.system_map}" }`,
			"zip.src: is required"},
	}
	for _, c := range cases {
		android.GroupFixturePreparers(prepareKernel(allHostTools), android.FixtureMergeMockFs(partitionModulesFS),
			android.FixtureAddTextFile("mods/Android.bp", c.bp)).
			ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(c.pattern))).
			RunTestWithBp(t, pongLike)
	}
}

// TestPartitionModulesPathsOutside checks that each path property that leads out of the module's
// directory is reported with the property and the next step, before Soong reads the path.
func TestPartitionModulesPathsOutside(t *testing.T) {
	const bp = `
kernel_partition_modules {
    name: "x",
    vendor_ramdisk: true,
    zip: { src: ":k{.modules}" },
    load_list: "modules.load.vendor_boot",
    recovery_load_list: "modules.load.recovery",
    blocklist_file: "modules.blocklist",
    system_map: ":k{.system_map}",
}
`
	// Each case replaces the value of one property, which occurs once in the text, with a path
	// outside the module's directory.
	for _, c := range []struct{ prop, value, outside string }{
		{"zip.src", `":k{.modules}"`, `"../k/modules.zip"`},
		{"system_map", `":k{.system_map}"`, `"../k/System.map"`},
		{"load_list", `"modules.load.vendor_boot"`, `"sub/../../modules.load"`},
		{"recovery_load_list", `"modules.load.recovery"`, `"/device/modules.load.recovery"`},
		{"blocklist_file", `"modules.blocklist"`, `"../../device/nothing/Pong/modules.blocklist"`},
	} {
		pattern := c.prop + ": " + c.outside + " is outside the directory of this Android.bp. " +
			`Move the file into the directory of this Android.bp, or refer to a module that outputs the file with ":<module>".`
		android.GroupFixturePreparers(prepareKernel(allHostTools), android.FixtureMergeMockFs(partitionModulesFS),
			android.FixtureAddTextFile("mods/Android.bp", strings.Replace(bp, c.value, c.outside, 1))).
			ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(pattern))).
			RunTestWithBp(t, pongLike)
	}

	for value, leaves := range map[string]bool{
		"modules.load":        false,
		"./sub/modules.load":  false,
		"sub/../modules.load": false,
		":k{.modules}":        false,
		"//device/x:k":        false,
		"..":                  true,
		"../modules.load":     true,
		"/modules.load":       true,
	} {
		if got := leavesModuleDir(value); got != leaves {
			t.Errorf("leavesModuleDir(%q) = %v, want %v", value, got, leaves)
		}
	}
}

// TestPartitionModulesPathProperties checks that pathProperties gives the properties tagged
// android:"path" by their names in a Blueprint file, a property of a nested struct after the
// struct's name and a dot, each with its value.
func TestPartitionModulesPathProperties(t *testing.T) {
	var p kernelPartitionModulesProperties
	p.Zip.Src = proptools.StringPtr("z")
	p.Load_list = proptools.StringPtr("l")
	p.Kernel_version = proptools.StringPtr("k")
	got := make(map[string]*string)
	for _, prop := range p.pathProperties() {
		got[prop.name] = prop.value
	}
	if proptools.String(got["zip.src"]) != "z" || proptools.String(got["load_list"]) != "l" {
		t.Errorf("pathProperties gives zip.src %v and load_list %v", got["zip.src"], got["load_list"])
	}
	if v, ok := got["blocklist_file"]; !ok || v != nil {
		t.Errorf("pathProperties gives blocklist_file, which is not set, as %v, %v", v, ok)
	}
	for _, untagged := range []string{"kernel_version", "zip.install_all", "zip"} {
		if _, ok := got[untagged]; ok {
			t.Errorf("pathProperties gives %s, which is not tagged as a path", untagged)
		}
	}
}

func TestPartitionModulesMakeEntry(t *testing.T) {
	kati := android.FixtureModifyConfig(android.SetKatiEnabledForTests)
	result := preparePartitionModules(kati).RunTestWithBp(t, pongLike)
	for _, c := range []struct{ name, primary string }{
		{"k-vendor_dlkm", "/vendor_dlkm/lib/modules/modules.load"},
		{"k-vendor_ramdisk", "/vendor_ramdisk/lib/modules/modules.dep"},
	} {
		m := result.ModuleForTests(t, c.name, "android_arm64_armv8-a").Module()
		info := android.AndroidMkInfoForTest(t, result.TestContext, m)
		if info.PrimaryInfo.Class != "ETC" || len(info.ExtraInfo) != 0 {
			t.Errorf("%s: class %q, %d extra entries", c.name, info.PrimaryInfo.Class, len(info.ExtraInfo))
		}
		installed := info.PrimaryInfo.EntryMap["LOCAL_SOONG_INSTALLED_MODULE"]
		if len(installed) != 1 || !strings.HasSuffix(installed[0], c.primary) {
			t.Errorf("%s: LOCAL_SOONG_INSTALLED_MODULE %v, want the primary installed file %s", c.name, installed, c.primary)
		}
	}

	// A module whose install is no full install, in a namespace that is not exported, has no
	// Make entry.
	result = android.GroupFixturePreparers(prepareKernel(allHostTools, kati), android.PrepareForTestWithNamespace,
		android.FixtureMergeMockFs(partitionModulesFS),
		android.FixtureAddTextFile("mods/Android.bp", "soong_namespace { imports: [\".\"] }\n"+partitionModules)).
		RunTestWithBp(t, pongLike)
	m := result.ModuleForTests(t, "k-vendor_dlkm", "android_arm64_armv8-a").Module()
	if _, ok := android.OtherModuleProvider(result.TestContext.OtherModuleProviderAdaptor(), m, android.AndroidMkInfoProvider); ok {
		t.Errorf("a module whose install is no full install has a Make entry")
	}
}

// Pong's device tree text, whose partition modules are kernel_partition_modules modules.
const pongDeviceBp = `
soong_namespace {}

kernel_build {
    name: "Pong-kernel",
    kernel_dir: "kernel/nothing/sm8475",
    extra_source_dirs: ["kernel/nothing/sm8475-devicetrees"],
    kernel_arch: "arm64",
    image: "Image",
    defconfig: "gki_defconfig",
    config_fragments: [
        "vendor/waipio_GKI.config",
        "vendor/nothing/waipio_GKI.config",
        "vendor/debugfs.config",
        "astatine.config",
    ],
    licenses: ["linux_kernel_license"],
    external_modules: {
        root: "kernel/nothing/sm8475-modules",
        dirs: [
            "qcom/opensource/mmrm-driver",
            "qcom/opensource/audio-kernel",
            "qcom/opensource/camera-kernel",
            "qcom/opensource/cvp-kernel",
            "qcom/opensource/dataipa/drivers/platform/msm",
            "qcom/opensource/datarmnet/core",
            "qcom/opensource/datarmnet-ext/aps",
            "qcom/opensource/datarmnet-ext/offload",
            "qcom/opensource/datarmnet-ext/shs",
            "qcom/opensource/datarmnet-ext/perf",
            "qcom/opensource/datarmnet-ext/perf_tether",
            "qcom/opensource/datarmnet-ext/sch",
            "qcom/opensource/datarmnet-ext/wlan",
            "qcom/opensource/display-drivers/msm",
            "qcom/opensource/eva-kernel",
            "qcom/opensource/video-driver",
            "qcom/opensource/wlan/qcacld-3.0/.qca6490",
        ],
    },
    dt_images: {
        merge: "qcom",
        dtbo_page_size: 4096,
    },
}

kernel_partition_modules {
    name: "Pong-kernel-modules-vendor_dlkm",
    licenses: ["linux_kernel_license"],
    vendor_dlkm_specific: true,
    zip: {
        src: ":Pong-kernel{.modules}",
        install_all: true,
    },
    load_list: "modules.load",
    blocklist_file: "modules.blocklist",
    system_map: ":Pong-kernel{.system_map}",
}

kernel_partition_modules {
    name: "Pong-kernel-modules-vendor_ramdisk",
    licenses: ["linux_kernel_license"],
    vendor_ramdisk: true,
    zip: {
        src: ":Pong-kernel{.modules}",
    },
    load_list: "modules.load.vendor_boot",
    recovery_load_list: "modules.load.recovery",
    blocklist_file: "modules.blocklist",
    system_map: ":Pong-kernel{.system_map}",
}
`

const spacewarDeviceBp = `
soong_namespace {}

kernel_build {
    name: "Spacewar-kernel",
    kernel_dir: "kernel/nothing/sm7325",
    kernel_arch: "arm64",
    image: "Image",
    defconfig: "vendor/lahaina-qgki_defconfig",
    config_fragments: ["vendor/debugfs.config"],
    licenses: ["linux_kernel_license"],
    module_renames: ["wlan.ko:qca_cld3_qca6750.ko"],
    dt_images: {
        merge: "none",
        dtbo_page_size: 4096,
    },
}

kernel_partition_modules {
    name: "Spacewar-kernel-modules-vendor_dlkm",
    licenses: ["linux_kernel_license"],
    vendor_dlkm_specific: true,
    zip: {
        src: ":Spacewar-kernel{.modules}",
        install_all: true,
    },
    load_list: "modules.load",
    blocklist_file: "modules.blocklist",
    system_map: ":Spacewar-kernel{.system_map}",
}

kernel_partition_modules {
    name: "Spacewar-kernel-modules-vendor_ramdisk",
    licenses: ["linux_kernel_license"],
    vendor_ramdisk: true,
    zip: {
        src: ":Spacewar-kernel{.modules}",
    },
    recovery_load_list: "modules.load.recovery",
    system_map: ":Spacewar-kernel{.system_map}",
}
`

// TestDeviceBpText analyses each device tree's kernel parts as the device trees give them, with
// the board variables of the device, and checks what the named module and the partition modules
// install.
func TestDeviceBpText(t *testing.T) {
	fs := android.MockFS{
		"prebuilts/tools-lineage/linux-x86/bin/perl": nil,
		"prebuilts/tools-lineage/common/perl-base/x": nil,
	}
	for _, f := range []string{"Makefile", "arch/arm64/configs/gki_defconfig", "arch/arm64/configs/vendor/waipio_GKI.config",
		"arch/arm64/configs/vendor/nothing/waipio_GKI.config", "arch/arm64/configs/vendor/debugfs.config",
		"arch/arm64/configs/astatine.config"} {
		fs["kernel/nothing/sm8475/"+f] = nil
	}
	fs["kernel/nothing/sm8475-devicetrees/qcom/Makefile"] = nil
	for _, d := range regexp.MustCompile(`"(qcom/opensource/[^"]*)"`).FindAllStringSubmatch(pongDeviceBp, -1) {
		fs["kernel/nothing/sm8475-modules/"+d[1]+"/Makefile"] = nil
	}
	for _, f := range []string{"Makefile", "arch/arm64/configs/vendor/lahaina-qgki_defconfig", "arch/arm64/configs/vendor/debugfs.config"} {
		fs["kernel/nothing/sm7325/"+f] = nil
	}
	for _, device := range []string{"Pong", "Spacewar"} {
		for _, f := range []string{"modules.load", "modules.load.vendor_boot", "modules.load.recovery", "modules.blocklist"} {
			fs["device/nothing/"+device+"/"+f] = nil
		}
	}
	fs["device/nothing/Pong/Android.bp"] = []byte(pongDeviceBp)
	fs["device/nothing/Spacewar/Android.bp"] = []byte(spacewarDeviceBp)

	for _, c := range []struct{ device, kernel string }{{"Pong", "Pong-kernel"}, {"Spacewar", "Spacewar-kernel"}} {
		result := android.GroupFixturePreparers(
			prepareKernelWithFS(allHostTools, fs, namedAs("//device/nothing/"+c.device+":"+c.kernel), dtboSize, withDtbFlag),
			android.PrepareForTestWithNamespace,
			android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) {
				v.NamespacesToExport = []string{"device/nothing/" + c.device}
			})).RunTest(t)
		var got []string
		for _, f := range installsOf(t, result, c.kernel).InstallFiles {
			got = append(got, f.String()[strings.LastIndex(f.String(), "/")+1:])
		}
		if strings.Join(got, " ") != "kernel dtb.img" {
			t.Errorf("%s installs %v", c.kernel, got)
		}
		if vars := makeVarsOf(t, result, c.kernel); len(vars) != 1 || vars[0].Name != "BOARD_PREBUILT_DTBOIMAGE" {
			t.Errorf("%s make variables: %v", c.kernel, vars)
		}
		if got := primaryInstall(t, result, c.kernel+"-modules-vendor_dlkm"); !strings.HasSuffix(got, "/vendor_dlkm/lib/modules/modules.load") {
			t.Errorf("%s vendor_dlkm installs %s", c.device, got)
		}
		ramdisk := "/vendor_ramdisk/lib/modules/modules.load"
		if c.device == "Spacewar" {
			ramdisk = "/vendor_ramdisk/lib/modules/modules.dep"
		}
		if got := primaryInstall(t, result, c.kernel+"-modules-vendor_ramdisk"); !strings.HasSuffix(got, ramdisk) {
			t.Errorf("%s vendor_ramdisk installs %s", c.device, got)
		}
	}
}
