// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"android/soong/android"

	"github.com/google/blueprint/proptools"
)

const adapterBp = `
board_kernel_adapter {
    name: "board_kernel_adapter",
}
`

// boardFS is the kernel of kernelMockFS, version 5.10, with a device tree directory below
// arch/arm64/boot/dts and a link from there into another directory of the kernel source, and the
// blocklist of a device. The mock file system of a fixture takes no link that leads out of its
// directory, which TestLinkTargetsOutside covers.
var boardFS = func() android.MockFS {
	fs := android.MockFS{}
	for k, v := range kernelMockFS {
		fs[k] = v
	}
	for k, v := range map[string][]byte{
		"kernel/k/Makefile":                           []byte("VERSION = 5\nPATCHLEVEL = 10\nSUBLEVEL = 1\n"),
		"kernel/k/arch/arm64/boot/dts/Makefile":       nil,
		"kernel/k/arch/arm64/boot/dts/qcom/a.dts":     nil,
		"kernel/k/arch/arm64/boot/dts/inside -> qcom": nil,
		"device/d/modules.blocklist":                  nil,
	} {
		fs[k] = v
	}
	return fs
}()

// withKernelVersion gives the kernel of boardFS the version.
func withKernelVersion(version, patchLevel string) android.FixturePreparer {
	return android.FixtureOverrideTextFile("kernel/k/Makefile", "VERSION = "+version+"\nPATCHLEVEL = "+patchLevel+"\n")
}

// pongBoard is the namespace that BoardConfigKernel.mk writes for a board that sets the kernel
// variables as Pong does.
func pongBoard() map[string]string {
	return map[string]string{
		"TARGET_KERNEL_SOURCE":                               "kernel/k",
		"TARGET_KERNEL_CONFIG":                               "gki_defconfig one.config two.config",
		"TARGET_KERNEL_ARCH":                                 "arm64",
		"BOARD_KERNEL_IMAGE_NAME":                            "Image",
		"TARGET_KERNEL_EXT_MODULE_ROOT":                      "kernel/mods",
		"TARGET_KERNEL_EXT_MODULES":                          "a b/c",
		"BOARD_USES_QCOM_MERGE_DTBS_SCRIPT":                  "true",
		"TARGET_NEEDS_DTBOIMAGE":                             "true",
		"BOARD_KERNEL_PAGESIZE":                              "4096",
		"BOARD_VENDOR_KERNEL_MODULES_LOAD":                   "a.ko b.ko",
		"BOARD_VENDOR_KERNEL_MODULES_BLOCKLIST_FILE":         "device/d/modules.blocklist",
		"BOARD_VENDOR_RAMDISK_KERNEL_MODULES_LOAD":           "a.ko",
		"BOARD_VENDOR_RAMDISK_RECOVERY_KERNEL_MODULES_LOAD":  "a.ko c.ko",
		"BOARD_VENDOR_RAMDISK_KERNEL_MODULES_BLOCKLIST_FILE": "device/d/modules.blocklist",
		"BOOT_KERNEL_MODULES":                                "a.ko c.ko d.ko",
	}
}

// withBoard sets the namespace and the module name that BoardConfigKernel.mk sets for a board with
// the kernel variables. The namespace lists every board variable of the adapter as passed, unless
// vars lists others.
func withBoard(vars map[string]string) android.FixturePreparer {
	ns := map[string]string{passedVariablesKey: strings.Join(boardVarNames, " ")}
	for k, v := range vars {
		ns[k] = v
	}
	return android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) {
		v.VendorVars = map[string]map[string]string{boardKernelNamespace: ns}
		v.BoardKernelBuildModule = proptools.StringPtr(":" + boardKernelModule)
	})
}

var withVendorDlkm = withPartitionVars(func(v *android.PartitionVariables) { v.BuildingVendorDlkmImage = true })

// withBootHeader sets the boot header version, from which a board builds a vendor_boot image.
func withBootHeader(version string) android.FixturePreparer {
	return withPartitionVars(func(v *android.PartitionVariables) { v.BoardBootHeaderVersion = version })
}

// prepareBoard prepares a board with the namespace of vars and a vendor_boot image, which the extra
// preparers can change.
func prepareBoard(vars map[string]string, extra ...android.FixturePreparer) android.FixturePreparer {
	return prepareKernelWithFS(allHostTools, boardFS, append(append([]android.FixturePreparer{withBootHeader("4")}, extra...),
		withBoard(vars))...)
}

func createdKernel(t *testing.T, result *android.TestResult) *kernelBuild {
	t.Helper()
	return result.ModuleForTests(t, boardKernelModule, "android_common").Module().(*kernelBuild)
}

// adapterMakeEntry gives the Make entry of the adapter.
func adapterMakeEntry(t *testing.T, result *android.TestResult) string {
	t.Helper()
	m := result.ModuleForTests(t, "board_kernel_adapter", "android_common").Module()
	data := android.AndroidMkDataForTest(t, result.TestContext, m)
	var entry strings.Builder
	data.Custom(&entry, "board_kernel_adapter", "", "", data)
	return entry.String()
}

// TestBoardKernelModules checks the modules created for a board like Pong and what they install:
// the kernel_build module that BOARD_KERNEL_BUILD_MODULE is set to, which takes no source directory
// from a link inside the kernel source, and the partition modules that read their lists from the
// lists modules.
func TestBoardKernelModules(t *testing.T) {
	kati := android.FixtureModifyConfig(android.SetKatiEnabledForTests)
	result := prepareBoard(pongBoard(), withVendorDlkm, dtboSize, withDtbFlag, kati).RunTestWithBp(t, adapterBp+globalHeaderModules)

	want := kernelBuildProperties{
		Kernel_dir:       proptools.StringPtr("kernel/k"),
		Kernel_arch:      proptools.StringPtr("arm64"),
		Image:            proptools.StringPtr("Image"),
		Defconfig:        proptools.StringPtr("gki_defconfig"),
		Config_fragments: []string{"one.config", "two.config"},
	}
	want.External_modules.Root = proptools.StringPtr("kernel/mods")
	want.External_modules.Dirs = []string{"a", "b/c"}
	want.Dt_images.Merge = proptools.StringPtr("qcom")
	want.Dt_images.Dtbo_page_size = proptools.Int64Ptr(4096)
	if got := createdKernel(t, result).properties; !reflect.DeepEqual(got, want) {
		t.Errorf("kernel_build properties:\n got %+v\nwant %+v", got, want)
	}
	// The next steps for a directory outside the source directories refer to the links of the dts
	// directory, which the board sets in place of extra_source_dirs.
	if script := kbuildScriptOf(t, result, boardKernelModule); !strings.Contains(script, "-next-step "+
		"'Add a link to the directory that contains these files directly inside arch/arm64/boot/dts of TARGET_KERNEL_SOURCE.'") {
		t.Errorf("the read check of the created kernel does not refer to the links of the dts directory")
	}
	if got := createdKernel(t, result).removeSourceDirStep(); got != "Remove the link to it from arch/arm64/boot/dts of TARGET_KERNEL_SOURCE." {
		t.Errorf("the created kernel removes an entry of extra_source_dirs with %q", got)
	}
	if got := installsOf(t, result, boardKernelModule).InstallFiles; len(got) != 2 {
		t.Errorf("the named kernel module installs %v, want the kernel and dtb.img", got)
	}

	// A link in the device tree directory that is pointed elsewhere makes Soong analyse again.
	if !android.InList("kernel/k/arch/arm64/boot/dts", result.NinjaDeps) {
		t.Errorf("the dependencies of the analysis lack the device tree directory: %v", result.NinjaDeps)
	}

	// The partition modules install from the lists that the lists modules write, one word per line.
	for _, c := range []struct{ module, file, content string }{
		{"board_kernel_module_lists_vendor_dlkm", "modules.load", "a.ko\nb.ko\n"},
		{"board_kernel_module_lists_vendor_ramdisk", "modules.load", "a.ko\n"},
		{"board_kernel_module_lists_vendor_ramdisk", "modules.load.recovery", "a.ko\nc.ko\n"},
		{"board_kernel_module_lists_vendor_ramdisk", "install.list", "a.ko\nc.ko\nd.ko\n"},
	} {
		lists := result.ModuleForTests(t, c.module, "android_common")
		if got := android.ContentFromFileRuleForTests(t, result.TestContext, lists.Output(c.file)); got != c.content {
			t.Errorf("%s %s: %q, want %q", c.module, c.file, got, c.content)
		}
	}
	for _, c := range []struct{ name, primary string }{
		{"board_kernel_modules_vendor_dlkm", "/vendor_dlkm/lib/modules/modules.load"},
		{"board_kernel_modules_vendor_ramdisk", "/vendor_ramdisk/lib/modules/modules.load"},
	} {
		if got := primaryInstall(t, result, c.name); !strings.HasSuffix(got, c.primary) {
			t.Errorf("%s installs %s, want %s", c.name, got, c.primary)
		}
	}
	inOrder(t, "vendor_dlkm", sboxCommand(t, result, "board_kernel_modules_vendor_dlkm"),
		"select", "/board_kernel/android_common/modules.zip", "/board_kernel/android_common/System.map",
		"-load-list __SBOX_SANDBOX_DIR__/out/soong/.intermediates/board_kernel_module_lists_vendor_dlkm/android_common/modules.load",
		"-load-list-name BOARD_VENDOR_KERNEL_MODULES_LOAD", "-install-all", "-allow-empty")
	inOrder(t, "vendor_ramdisk", sboxCommand(t, result, "board_kernel_modules_vendor_ramdisk"),
		"-load-list __SBOX_SANDBOX_DIR__/out/soong/.intermediates/board_kernel_module_lists_vendor_ramdisk/android_common/modules.load",
		"-load-list-name BOARD_VENDOR_RAMDISK_KERNEL_MODULES_LOAD",
		"-recovery-load-list __SBOX_SANDBOX_DIR__/out/soong/.intermediates/board_kernel_module_lists_vendor_ramdisk/android_common/modules.load.recovery",
		"-recovery-load-list-name BOARD_VENDOR_RAMDISK_RECOVERY_KERNEL_MODULES_LOAD",
		"-install-list __SBOX_SANDBOX_DIR__/out/soong/.intermediates/board_kernel_module_lists_vendor_ramdisk/android_common/install.list",
		"-install-list-name BOOT_KERNEL_MODULES")
	for _, name := range []string{"board_kernel_modules_vendor_dlkm", "board_kernel_modules_vendor_ramdisk"} {
		m := result.ModuleForTests(t, name, "android_arm64_armv8-a")
		if props := android.ContentFromFileRuleForTests(t, result.TestContext, m.Output("props.json")); !strings.Contains(props, `"Blocklist_file":"device/d/modules.blocklist"`) {
			t.Errorf("%s: props.json %s contains another blocklist", name, props)
		}
	}

	// The Make entry of the adapter is a phony package that requires the partition modules.
	containsAll(t, "Make entry of the adapter", adapterMakeEntry(t, result),
		"LOCAL_REQUIRED_MODULES := board_kernel_modules_vendor_dlkm board_kernel_modules_vendor_ramdisk",
		"include $(BUILD_PHONY_PACKAGE)")

	// The header module follows the created kernel_build module, whose type name is that of a
	// module a load hook creates.
	script := ruleScript(t, result, result.ModuleForTests(t, "generated_kernel_includes", ""), "headers")
	if !strings.Contains(script, `K="$T"/kernel/k`) {
		t.Errorf("the header rule does not export the headers of the created kernel")
	}
}

// TestBoardKernelPartitions checks which partition modules a board gets: vendor in place of
// vendor_dlkm without a vendor_dlkm image, a vendor module that installs every module of the kernel
// whatever the board lists, and a vendor ramdisk module for any one of its three lists.
func TestBoardKernelPartitions(t *testing.T) {
	vars := pongBoard()
	delete(vars, "BOARD_VENDOR_KERNEL_MODULES_BLOCKLIST_FILE")
	delete(vars, "BOARD_VENDOR_RAMDISK_KERNEL_MODULES_LOAD")
	delete(vars, "BOARD_VENDOR_RAMDISK_KERNEL_MODULES_BLOCKLIST_FILE")
	delete(vars, "BOOT_KERNEL_MODULES")
	result := prepareBoard(vars, dtboSize).RunTestWithBp(t, adapterBp)
	if got := primaryInstall(t, result, "board_kernel_modules_vendor"); !strings.HasSuffix(got, "/vendor/lib/modules/modules.load") {
		t.Errorf("the vendor module installs %s", got)
	}
	if got := primaryInstall(t, result, "board_kernel_modules_vendor_ramdisk"); !strings.HasSuffix(got, "/vendor_ramdisk/lib/modules/modules.dep") {
		t.Errorf("the vendor ramdisk module with a recovery list alone installs %s", got)
	}

	// BOOT_KERNEL_MODULES alone gives a vendor ramdisk module that installs its modules and writes
	// no modules.load.
	delete(vars, "BOARD_VENDOR_RAMDISK_RECOVERY_KERNEL_MODULES_LOAD")
	vars["BOOT_KERNEL_MODULES"] = "a.ko"
	result = prepareBoard(vars, dtboSize).RunTestWithBp(t, adapterBp)
	if got := primaryInstall(t, result, "board_kernel_modules_vendor_ramdisk"); !strings.HasSuffix(got, "/vendor_ramdisk/lib/modules/modules.dep") {
		t.Errorf("the vendor ramdisk module with BOOT_KERNEL_MODULES alone installs %s", got)
	}
	inOrder(t, "vendor_ramdisk", sboxCommand(t, result, "board_kernel_modules_vendor_ramdisk"),
		"-install-list ", "-install-list-name BOOT_KERNEL_MODULES")

	// A board without module lists and without external modules gets a vendor module that installs
	// every module of the kernel, and no vendor ramdisk module.
	delete(vars, "BOOT_KERNEL_MODULES")
	delete(vars, "BOARD_VENDOR_KERNEL_MODULES_LOAD")
	delete(vars, "TARGET_KERNEL_EXT_MODULE_ROOT")
	delete(vars, "TARGET_KERNEL_EXT_MODULES")
	result = prepareBoard(vars, dtboSize).RunTestWithBp(t, adapterBp)
	if got := primaryInstall(t, result, "board_kernel_modules_vendor"); !strings.HasSuffix(got, "/vendor/lib/modules/modules.dep") {
		t.Errorf("the vendor module of a board without module lists installs %s", got)
	}
	inOrder(t, "vendor", sboxCommand(t, result, "board_kernel_modules_vendor"), "select", "-install-all", "-allow-empty")
	if v := result.ModuleVariantsForTests("board_kernel_modules_vendor_ramdisk"); len(v) != 0 {
		t.Errorf("a board without vendor ramdisk lists has a vendor ramdisk module: %v", v)
	}
	if got := createdKernel(t, result).properties.External_modules.Root; got != nil {
		t.Errorf("external_modules.root without external modules: %q", *got)
	}

	// A load list without a word is no list.
	vars["BOARD_VENDOR_KERNEL_MODULES_LOAD"] = " "
	result = prepareBoard(vars, dtboSize).RunTestWithBp(t, adapterBp)
	if got := primaryInstall(t, result, "board_kernel_modules_vendor"); !strings.HasSuffix(got, "/vendor/lib/modules/modules.dep") {
		t.Errorf("the vendor module of a board with a load list without a word installs %s", got)
	}
}

// TestBoardKernelBootRamdisk checks that a board without a vendor_boot image gets its ramdisk
// module in the boot ramdisk, and that a recovery load list, which only the vendor ramdisk has,
// stops the build there.
func TestBoardKernelBootRamdisk(t *testing.T) {
	vars := pongBoard()
	delete(vars, "BOARD_VENDOR_RAMDISK_RECOVERY_KERNEL_MODULES_LOAD")
	result := prepareBoard(vars, dtboSize, withBootHeader("2")).RunTestWithBp(t, adapterBp)
	if got := primaryInstall(t, result, "board_kernel_modules_ramdisk"); !strings.HasSuffix(got, "/ramdisk/lib/modules/modules.load") ||
		strings.Contains(got, "vendor_ramdisk") {
		t.Errorf("the ramdisk module of a board without a vendor_boot image installs %s", got)
	}
	if v := result.ModuleVariantsForTests("board_kernel_modules_vendor_ramdisk"); len(v) != 0 {
		t.Errorf("a board without a vendor_boot image has a vendor ramdisk module: %v", v)
	}
	inOrder(t, "ramdisk", sboxCommand(t, result, "board_kernel_modules_ramdisk"),
		"-load-list-name BOARD_VENDOR_RAMDISK_KERNEL_MODULES_LOAD", "-install-list-name BOOT_KERNEL_MODULES")

	prepareBoard(pongBoard(), dtboSize, withBootHeader("2")).
		ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(
			"BOARD_VENDOR_RAMDISK_RECOVERY_KERNEL_MODULES_LOAD is set, and the board builds no vendor_boot image, the only image "+
				"with a modules.load.recovery. Remove BOARD_VENDOR_RAMDISK_RECOVERY_KERNEL_MODULES_LOAD, and add the modules that "+
				"recovery loads to BOARD_VENDOR_RAMDISK_KERNEL_MODULES_LOAD."))).
		RunTestWithBp(t, adapterBp)

	for _, c := range []struct {
		header, product string
		want            bool
	}{{"", "", false}, {"2", "", false}, {"3", "", true}, {"4", "true", true}, {"4", "false", false}} {
		v := android.PartitionVariables{BoardBootHeaderVersion: c.header, ProductBuildVendorBootImage: c.product}
		if got := buildsVendorBootImage(v); got != c.want {
			t.Errorf("buildsVendorBootImage with header %q and product %q = %v, want %v", c.header, c.product, got, c.want)
		}
	}
}

// TestBoardKernelConversions checks how the board variables become properties and the names of
// the properties in messages: the clang directory, the module renames and the device tree images of
// a board without the merge script, a board without device tree images, and the kernel
// architecture.
func TestBoardKernelConversions(t *testing.T) {
	vars := pongBoard()
	vars["TARGET_KERNEL_CLANG_VERSION"] = "old"
	vars["TARGET_MODULE_ALIASES"] = "wlan.ko:qca.ko"
	delete(vars, "BOARD_USES_QCOM_MERGE_DTBS_SCRIPT")
	result := prepareBoard(vars, dtboSize).RunTestWithBp(t, adapterBp)
	k := createdKernel(t, result)
	if got := proptools.String(k.properties.Clang_dir); got != "prebuilts/clang/host/linux-x86/clang-old" {
		t.Errorf("clang_dir from TARGET_KERNEL_CLANG_VERSION: %q", got)
	}
	if got := k.name(propClangDir); got != "TARGET_KERNEL_CLANG_VERSION" {
		t.Errorf("name of clang_dir from TARGET_KERNEL_CLANG_VERSION: %q", got)
	}
	if !reflect.DeepEqual(k.properties.Module_renames, []string{"wlan.ko:qca.ko"}) {
		t.Errorf("module_renames: %v", k.properties.Module_renames)
	}
	// The messages of the module collection refer to the renames as TARGET_MODULE_ALIASES.
	if script := kbuildScriptOf(t, result, boardKernelModule); !strings.Contains(script, "-renames-name TARGET_MODULE_ALIASES -rename") {
		t.Errorf("the module collection of the created kernel does not refer to TARGET_MODULE_ALIASES")
	}
	if got := proptools.String(k.properties.Dt_images.Merge); got != mergeNone {
		t.Errorf("dt_images.merge without the merge script: %q", got)
	}

	// TARGET_KERNEL_CLANG_PATH wins over TARGET_KERNEL_CLANG_VERSION.
	vars["TARGET_KERNEL_CLANG_PATH"] = "prebuilts/clang/kernel/clang-k"
	k = createdKernel(t, prepareBoard(vars, dtboSize).RunTestWithBp(t, adapterBp))
	if got := proptools.String(k.properties.Clang_dir); got != "prebuilts/clang/kernel/clang-k" {
		t.Errorf("clang_dir from TARGET_KERNEL_CLANG_PATH: %q", got)
	}
	if got := k.name(propClangDir); got != "TARGET_KERNEL_CLANG_PATH" {
		t.Errorf("name of clang_dir from TARGET_KERNEL_CLANG_PATH: %q", got)
	}

	// Without BOARD_INCLUDE_DTB_IN_BOOTIMG, TARGET_NEEDS_DTBOIMAGE and BOARD_KERNEL_SEPARATED_DTBO
	// the kernel makes no device tree images.
	delete(vars, "TARGET_NEEDS_DTBOIMAGE")
	k = createdKernel(t, prepareBoard(vars).RunTestWithBp(t, adapterBp))
	if k.dtImagesSet() {
		t.Errorf("dt_images is set for a board without device tree images: %+v", k.properties.Dt_images)
	}

	// Without TARGET_KERNEL_ARCH the kernel takes the architecture of the device.
	delete(vars, "TARGET_KERNEL_ARCH")
	k = createdKernel(t, prepareBoard(vars).RunTestWithBp(t, adapterBp))
	if got := proptools.String(k.properties.Kernel_arch); got != "arm64" {
		t.Errorf("kernel_arch without TARGET_KERNEL_ARCH: %q", got)
	}

	// A device architecture becomes its kernel architecture, the value of a board without
	// TARGET_KERNEL_ARCH included, and any other value is used as it is.
	for _, c := range []struct {
		value  string
		device android.ArchType
		kernel string
	}{
		{"x86_64", android.Arm64, "x86"},
		{"riscv64", android.Arm64, "riscv"},
		{"x86", android.Arm64, "x86"},
		{"arm", android.Arm64, "arm"},
		{"riscv", android.Arm64, "riscv"},
		{"mips", android.Arm64, "mips"},
		{"", android.Riscv64, "riscv"},
		{"", android.X86_64, "x86"},
		{"", android.Arm, "arm"},
	} {
		if got := kernelArch(c.value, c.device); got != c.kernel {
			t.Errorf("kernelArch(%q, %s) = %q, want %q", c.value, c.device, got, c.kernel)
		}
	}
}

// TestBoardKernelAdapterOff checks that the adapter creates nothing and requires nothing, while its
// name has a Make entry, when BOARD_KERNEL_BUILD_MODULE is not set to the created module: on a board
// without the kernel variables, and on a board whose namespace has values but whose
// BOARD_KERNEL_BUILD_MODULE is set to its own module.
func TestBoardKernelAdapterOff(t *testing.T) {
	kati := android.FixtureModifyConfig(android.SetKatiEnabledForTests)
	ownModule := android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) {
		v.VendorVars = map[string]map[string]string{boardKernelNamespace: pongBoard()}
		v.BoardKernelBuildModule = proptools.StringPtr("//device/d:k")
	})
	for _, board := range []android.FixturePreparer{android.NullFixturePreparer, ownModule} {
		result := prepareKernelWithFS(allHostTools, boardFS, kati, board).RunTestWithBp(t, adapterBp)
		for _, name := range []string{boardKernelModule, "board_kernel_module_lists_vendor_dlkm", "board_kernel_module_lists_vendor",
			"board_kernel_module_lists_vendor_ramdisk", "board_kernel_modules_vendor_dlkm", "board_kernel_modules_vendor",
			"board_kernel_modules_vendor_ramdisk"} {
			if v := result.ModuleVariantsForTests(name); len(v) != 0 {
				t.Errorf("%s exists without the adapter: %v", name, v)
			}
		}
		entry := adapterMakeEntry(t, result)
		if !strings.Contains(entry, "include $(BUILD_PHONY_PACKAGE)") || strings.Contains(entry, "LOCAL_REQUIRED_MODULES") {
			t.Errorf("Make entry of the adapter without the adapter:\n%s", entry)
		}
	}
}

// TestBoardKernelErrors checks that a problem with a board value is reported with the board
// variable and the next step, whether kernel_build or the adapter finds it.
func TestBoardKernelErrors(t *testing.T) {
	const dtImagesVars = "BOARD_INCLUDE_DTB_IN_BOOTIMG, TARGET_NEEDS_DTBOIMAGE or BOARD_KERNEL_SEPARATED_DTBO"
	prebuiltDtbo := withPartitionVars(func(v *android.PartitionVariables) { v.BoardPrebuiltDtboImage = "device/d/dtbo.img" })
	cases := []struct {
		change  func(map[string]string)
		extra   []android.FixturePreparer
		pattern string
	}{
		{func(v map[string]string) { v["TARGET_KERNEL_SOURCE"] = "kernel/missing" }, nil,
			"TARGET_KERNEL_SOURCE: kernel/missing does not exist. Sync the kernel repository to that path, or correct TARGET_KERNEL_SOURCE."},
		{func(v map[string]string) { v["TARGET_KERNEL_CONFIG"] = "missing_defconfig one.config" }, nil,
			"TARGET_KERNEL_CONFIG: kernel/k/arch/arm64/configs/missing_defconfig does not exist. " +
				"Correct TARGET_KERNEL_CONFIG, or check TARGET_KERNEL_ARCH."},
		{func(v map[string]string) { v["TARGET_KERNEL_CONFIG"] = "gki_defconfig three.config" }, nil,
			"TARGET_KERNEL_CONFIG: kernel/k/arch/arm64/configs/three.config does not exist. " +
				"Add the fragment to the kernel source, or correct TARGET_KERNEL_CONFIG."},
		{func(v map[string]string) { delete(v, "TARGET_KERNEL_CONFIG") }, nil,
			"TARGET_KERNEL_CONFIG: is required. Set TARGET_KERNEL_CONFIG to the base configuration"},
		{func(v map[string]string) { delete(v, "BOARD_KERNEL_IMAGE_NAME") }, nil,
			"BOARD_KERNEL_IMAGE_NAME: is required. Set BOARD_KERNEL_IMAGE_NAME to the image target of the kernel, such as Image."},
		{func(v map[string]string) { v["TARGET_KERNEL_ARCH"] = "x86_64" }, []android.FixturePreparer{dtboSize, android.FixtureMergeMockFs(android.MockFS{
			"kernel/k/arch/x86/configs/gki_defconfig": nil,
			"kernel/k/arch/x86/configs/one.config":    nil,
			"kernel/k/arch/x86/configs/two.config":    nil,
		})},
			`TARGET_KERNEL_ARCH: "x86" does not fit the device's primary architecture arm64. Set TARGET_KERNEL_ARCH to arm64.`},
		{func(v map[string]string) { v["TARGET_KERNEL_EXT_MODULE_ROOT"] = "kernel/nomods" }, nil,
			"TARGET_KERNEL_EXT_MODULE_ROOT: kernel/nomods does not exist. " +
				"Sync the module repository to that path, or correct TARGET_KERNEL_EXT_MODULE_ROOT."},
		{func(v map[string]string) { v["TARGET_KERNEL_EXT_MODULES"] = "a z" }, nil,
			"TARGET_KERNEL_EXT_MODULES: kernel/mods/z does not exist. Sync the module repository, or correct TARGET_KERNEL_EXT_MODULES."},
		{func(v map[string]string) { delete(v, "TARGET_KERNEL_EXT_MODULE_ROOT") }, nil,
			"TARGET_KERNEL_EXT_MODULE_ROOT: is required when TARGET_KERNEL_EXT_MODULES is set. " +
				"Set TARGET_KERNEL_EXT_MODULE_ROOT to the external module tree"},
		{func(v map[string]string) { v["TARGET_KERNEL_CLANG_PATH"] = "prebuilts/clang/kernel/none" }, nil,
			"TARGET_KERNEL_CLANG_PATH: prebuilts/clang/kernel/none does not exist. Add the clang directory at that path, " +
				"correct TARGET_KERNEL_CLANG_PATH, or remove TARGET_KERNEL_CLANG_PATH to use the platform clang."},
		{func(v map[string]string) { v["TARGET_KERNEL_CLANG_PATH"] = "prebuilts/clang/../gcc" }, nil,
			`TARGET_KERNEL_CLANG_PATH: "prebuilts/clang/../gcc" is not a directory below prebuilts/clang. ` +
				"Set TARGET_KERNEL_CLANG_PATH to a clang directory below prebuilts/clang"},
		{func(v map[string]string) { v["TARGET_KERNEL_CLANG_VERSION"] = "none" }, nil,
			"TARGET_KERNEL_CLANG_VERSION: prebuilts/clang/host/linux-x86/clang-none does not exist."},
		{func(v map[string]string) {
			v["BOARD_VENDOR_RAMDISK_KERNEL_MODULES_BLOCKLIST_FILE"] = "device/d/missing.blocklist"
		}, nil,
			"BOARD_VENDOR_RAMDISK_KERNEL_MODULES_BLOCKLIST_FILE is set to device/d/missing.blocklist, which does not exist. " +
				"Add the file to the device tree, or correct BOARD_VENDOR_RAMDISK_KERNEL_MODULES_BLOCKLIST_FILE."},
		{func(v map[string]string) {}, nil,
			dtImagesVars + ": is set, but the board sets no BOARD_DTBOIMG_PARTITION_SIZE. " +
				"Set BOARD_DTBOIMG_PARTITION_SIZE to the size of the dtbo partition."},
		{func(v map[string]string) { delete(v, "BOARD_KERNEL_PAGESIZE") }, []android.FixturePreparer{dtboSize},
			"BOARD_KERNEL_PAGESIZE: is required when " + dtImagesVars + " is set. " +
				"Set BOARD_KERNEL_PAGESIZE to the page size of the device in bytes, such as 4096."},
		{func(v map[string]string) { v["BOARD_KERNEL_PAGESIZE"] = "4k" }, []android.FixturePreparer{dtboSize},
			`BOARD_KERNEL_PAGESIZE is "4k", which is not a number. Set BOARD_KERNEL_PAGESIZE to the page size`},
		{func(v map[string]string) { v["BOARD_KERNEL_PAGESIZE"] = "0" }, []android.FixturePreparer{dtboSize},
			"BOARD_KERNEL_PAGESIZE: 0 is not a positive integer. Set BOARD_KERNEL_PAGESIZE to the page size"},
		{func(v map[string]string) {}, []android.FixturePreparer{dtboSize, prebuiltDtbo},
			"BOARD_PREBUILT_DTBOIMAGE (device/d/dtbo.img) makes a second dtbo.img. Remove BOARD_PREBUILT_DTBOIMAGE."},
		{func(v map[string]string) { delete(v, "TARGET_NEEDS_DTBOIMAGE") }, []android.FixturePreparer{dtboSize, prebuiltDtbo},
			dtImagesVars + ": is not set, and the board sets BOARD_PREBUILT_DTBOIMAGE (device/d/dtbo.img), but the dtbo partition " +
				"gets dtbo.img only from this module. Remove BOARD_PREBUILT_DTBOIMAGE, and set " + dtImagesVars + "."},
		{func(v map[string]string) {}, []android.FixturePreparer{dtboSize, withDtbFlag, withPartitionVars(func(v *android.PartitionVariables) {
			v.BoardPrebuiltDtbDir = "device/d/dtbs"
		})}, "BOARD_PREBUILT_DTBIMAGE_DIR (device/d/dtbs) makes a second dtb.img. Remove BOARD_PREBUILT_DTBIMAGE_DIR."},
		{func(v map[string]string) {}, []android.FixturePreparer{dtboSize, withKernelVersion("6", "11")},
			"TARGET_KERNEL_SOURCE kernel/k contains Linux 6.11, and kernels 6.11 and newer are not supported, since their host " +
				"programs need the host sysroot. Use a kernel source below 6.11."},
		{func(v map[string]string) { v["TARGET_KERNEL_SOURCE"] = "kernel/dts" }, nil,
			"TARGET_KERNEL_SOURCE kernel/dts contains no Makefile. Sync the kernel repository to kernel/dts, or correct TARGET_KERNEL_SOURCE."},
		{func(v map[string]string) {}, []android.FixturePreparer{dtboSize, android.FixtureOverrideTextFile("kernel/k/Makefile", "VERSION = 5\n")},
			"TARGET_KERNEL_SOURCE kernel/k: the Makefile assigns no number to PATCHLEVEL. Set TARGET_KERNEL_SOURCE to a Linux kernel source."},
		{func(v map[string]string) {}, []android.FixturePreparer{dtboSize, android.FixtureMergeMockFs(android.MockFS{
			"kernel/k/arch/arm64/boot/dts/gone -> missing": nil,
		})}, "The link kernel/k/arch/arm64/boot/dts/gone leads to missing, which does not exist. " +
			"Sync the repository to kernel/k/arch/arm64/boot/dts/missing."},
	}
	for _, c := range cases {
		vars := pongBoard()
		c.change(vars)
		prepareBoard(vars, c.extra...).
			ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(c.pattern))).
			RunTestWithBp(t, adapterBp)
	}
}

// TestPassedVariables checks that a difference between the variables that BoardConfigKernel.mk
// lists as passed and those that the adapter reads stops the build with each variable that differs.
func TestPassedVariables(t *testing.T) {
	passed := append([]string{"TARGET_KERNEL_EXTRA"}, boardVarNames[1:]...)
	vars := pongBoard()
	vars[passedVariablesKey] = strings.Join(passed, " ")
	prepareBoard(vars).
		ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(
			"BoardConfigKernel.mk passes TARGET_KERNEL_EXTRA, which board_kernel_adapter does not read, and board_kernel_adapter "+
				"reads "+boardVarNames[0]+", which BoardConfigKernel.mk does not pass. Add each of these variables to the list that "+
				"lacks it: the variables that BoardConfigKernel.mk passes, or the board variables of board_kernel_adapter."))).
		RunTestWithBp(t, adapterBp)

	// A namespace without the list differs in every variable.
	delete(vars, passedVariablesKey)
	withoutList := android.FixtureModifyProductVariables(func(v android.FixtureProductVariables) {
		v.VendorVars = map[string]map[string]string{boardKernelNamespace: vars}
		v.BoardKernelBuildModule = proptools.StringPtr(":" + boardKernelModule)
	})
	prepareKernelWithFS(allHostTools, boardFS, withoutList).
		ExtendWithErrorHandler(android.FixtureExpectsAtLeastOneErrorMatchingPattern(regexp.QuoteMeta(
			"board_kernel_adapter reads "+strings.Join(boardVarNames, ", ")+", which BoardConfigKernel.mk does not pass."))).
		RunTestWithBp(t, adapterBp)
}

// TestKernelVersion checks that the version of a kernel source comes from the first assignments of
// VERSION and PATCHLEVEL in its Makefile, and that a kernel older than 5.10 builds.
func TestKernelVersion(t *testing.T) {
	v, err := readKernelVersion("Makefile", strings.NewReader("# SPDX\nVERSION = 6\nPATCHLEVEL = 1\nSUBLEVEL = 57\n"+
		"x: PATCHLEVEL = 9\nPATCHLEVEL := $(if $(PATCHLEVEL), $(PATCHLEVEL), 0)\n"))
	if err != nil || v != (kernelVersion{6, 1}) {
		t.Errorf("readKernelVersion gives %v, %v, want 6.1", v, err)
	}
	for _, text := range []string{"VERSION = 6\n", "VERSION = six\nPATCHLEVEL = 1\n", "VERSION = $(V)\nPATCHLEVEL = 1\n"} {
		if _, err := readKernelVersion("Makefile", strings.NewReader(text)); err == nil {
			t.Errorf("readKernelVersion of %q gives no error", text)
		}
	}
	for _, c := range []struct {
		v, o kernelVersion
		want bool
	}{{kernelVersion{5, 10}, kernelVersion{5, 10}, true}, {kernelVersion{5, 4}, kernelVersion{5, 10}, false},
		{kernelVersion{6, 1}, kernelVersion{5, 10}, true}, {kernelVersion{4, 19}, kernelVersion{5, 10}, false}} {
		if got := c.v.atLeast(c.o); got != c.want {
			t.Errorf("%s.atLeast(%s) = %v, want %v", c.v, c.o, got, c.want)
		}
	}

	prepareBoard(pongBoard(), dtboSize, withKernelVersion("5", "4")).RunTestWithBp(t, adapterBp)
}

// TestLinkTargetsOutside checks which link targets become source directories, those outside the
// kernel source, relative targets resolved against the directory of the link, each once, and that
// a link to an absolute path, out of the source tree or to nothing is reported with the link and the
// next step. A link to a file is no source directory.
func TestLinkTargetsOutside(t *testing.T) {
	dts := "kernel/k/arch/arm64/boot/dts/"
	dirs, problems := linkTargetsOutside("kernel/k", map[string]dtsLink{
		dts + "vendor":  {"../../../../../k-devicetrees", true},
		dts + "vendor2": {"../../../../../k-devicetrees/", true},
		dts + "inside":  {"qcom", true},
		dts + "up":      {"../../../..", true},
		dts + "sibling": {"../../../../../kx", true},
		dts + "abs":     {"/kernel/abs", true},
		dts + "out":     {"../../../../../../../../out", true},
		dts + "file":    {"../../../../../k-files/a.dtsi", false},
		dts + "gone":    {"../../../../../k-missing", false},
	}, func(p string) bool { return p == "kernel/k-files/a.dtsi" })
	if want := []string{"kernel/k-devicetrees", "kernel/kx"}; !reflect.DeepEqual(dirs, want) {
		t.Errorf("linkTargetsOutside gives the directories %q, want %q", dirs, want)
	}
	if want := []string{
		"The link " + dts + "abs leads to the absolute path /kernel/abs. Replace it with a link relative to its own directory.",
		"The link " + dts + "gone leads to ../../../../../k-missing, which does not exist. Sync the repository to kernel/k-missing.",
		"The link " + dts + "out leads to ../../../../../../../../out, outside the source tree. " +
			"Move the directory into the source tree, and link to it there.",
	}; !reflect.DeepEqual(problems, want) {
		t.Errorf("linkTargetsOutside reports %q, want %q", problems, want)
	}
}
