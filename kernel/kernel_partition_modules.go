// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"path"
	"reflect"
	"strings"

	"android/soong/android"
	"android/soong/cc/config"
	"android/soong/kernel/common"

	"github.com/google/blueprint/proptools"
)

type kernelPartitionModulesProperties struct {
	Zip struct {
		// The zip of kernel modules, the .modules output of a kernel_build module, which holds
		// every .ko at its top level.
		Src *string `android:"path"`

		// Install every module of the zip. Otherwise the installed modules are those that
		// load_list, recovery_load_list and install_list name, each once.
		Install_all *bool

		// With install_all, install nothing for a zip without modules. Without it, such a zip
		// fails the build.
		Allow_empty *bool
	}

	// The modules.load of the partition, in its order. Without it no modules.load is written.
	Load_list *string `android:"path"`

	// The modules.load.recovery of the partition, only for vendor_ramdisk.
	Recovery_load_list *string `android:"path"`

	// The modules to install in addition to those of the load lists, in the form of a load list.
	// They are not written to modules.load.
	Install_list *string `android:"path"`

	// The System.map of the kernel whose modules the zip holds, the .system_map output of the
	// kernel_build module. Every installed module is checked against it for unknown symbols.
	System_map *string `android:"path"`

	// The modules.blocklist of the partition, handed to AOSP's kernel_modules_builder.
	Blocklist_file *string `android:"path"`

	// The kernel version directory below lib/modules that the modules are installed into.
	Kernel_version *string
}

// The list properties of kernel_partition_modules, which the messages of the build refer to.
var (
	propLoadList         = kernelProperty{"load_list"}
	propRecoveryLoadList = kernelProperty{"recovery_load_list"}
	propInstallList      = kernelProperty{"install_list"}
)

type kernelPartitionModules struct {
	android.ModuleBase

	properties kernelPartitionModulesProperties

	// names are the names that the messages of the build use for the list properties. The factory
	// sets them.
	names settingNames

	// primary is the file the rule declares as its output and the module installs, with the zip
	// of the other files: modules.load, or modules.dep without a load list.
	primary android.Path
	// fullInstall reports that the module's install was a full install, which Make then takes.
	fullInstall bool
}

// KernelPartitionModulesFactory makes a kernel_partition_modules module: it installs the kernel
// modules of one partition from the modules zip of a kernel_build module, with the partition's load
// lists, and replaces its install directory as a whole at every install.
func KernelPartitionModulesFactory() android.Module {
	return newKernelPartitionModules(nil)
}

// kernelPartitionModulesFactoryWithNames gives the factory of kernel_partition_modules modules whose
// messages use names for their list properties, for code that creates a module from settings of
// another form. Soong makes each variant of a module with its factory and copies only the
// properties, so the names have to come from the factory.
func kernelPartitionModulesFactoryWithNames(names settingNames) android.ModuleFactory {
	return func() android.Module { return newKernelPartitionModules(names) }
}

func newKernelPartitionModules(names settingNames) *kernelPartitionModules {
	module := &kernelPartitionModules{names: names}
	module.AddProperties(&module.properties)
	android.InitAndroidArchModule(module, android.DeviceSupported, android.MultilibFirst)
	return module
}

func (m *kernelPartitionModules) DepsMutator(ctx android.BottomUpMutatorContext) {
	ctx.AddHostToolDependencies("kernel_partition_modules_builder", "kernel_modules_builder", "zipsync", "soong_zip",
		"merge_zips", "depmod")
}

// kernelModulePartition is a partition that kernel_partition_modules installs kernel modules into.
type kernelModulePartition struct {
	// name is the name of the partition.
	name string
	// placement are the properties of the module base that place a module in the partition.
	placement placementProperties
	// installsIn reports whether a module installs into the partition.
	installsIn func(android.ModuleContext) bool
	// builderPartition is the partition argument of AOSP's kernel_modules_builder, on which the
	// paths in modules.dep depend.
	builderPartition string
	// ramdisk is the directory of the product out directory whose lib/modules the modules of a
	// ramdisk go into, and empty for a partition whose modules go into lib/modules of its own.
	ramdisk string
}

// placementProperties are the properties of the module base that place a module in a partition.
type placementProperties struct {
	System_dlkm_specific  *bool
	Vendor_dlkm_specific  *bool
	Odm_dlkm_specific     *bool
	Vendor_ramdisk        *bool
	Vendor_kernel_ramdisk *bool
	Ramdisk               *bool
	Vendor                *bool
}

var (
	systemDlkmPartition = kernelModulePartition{name: "system_dlkm",
		placement:  placementProperties{System_dlkm_specific: proptools.BoolPtr(true)},
		installsIn: android.ModuleContext.InstallInSystemDlkm, builderPartition: "system_dlkm"}
	vendorDlkmPartition = kernelModulePartition{name: "vendor_dlkm",
		placement:  placementProperties{Vendor_dlkm_specific: proptools.BoolPtr(true)},
		installsIn: android.ModuleContext.InstallInVendorDlkm, builderPartition: "vendor_dlkm"}
	odmDlkmPartition = kernelModulePartition{name: "odm_dlkm",
		placement:  placementProperties{Odm_dlkm_specific: proptools.BoolPtr(true)},
		installsIn: android.ModuleContext.InstallInOdmDlkm, builderPartition: "odm_dlkm"}
	vendorRamdiskPartition = kernelModulePartition{name: "vendor_ramdisk",
		placement:  placementProperties{Vendor_ramdisk: proptools.BoolPtr(true)},
		installsIn: android.ModuleContext.InstallInVendorRamdisk, builderPartition: "vendor_ramdisk", ramdisk: "vendor_ramdisk"}
	vendorKernelRamdiskPartition = kernelModulePartition{name: "vendor_kernel_ramdisk",
		placement:  placementProperties{Vendor_kernel_ramdisk: proptools.BoolPtr(true)},
		installsIn: android.ModuleContext.InstallInVendorKernelRamdisk, builderPartition: "vendor_kernel_ramdisk",
		ramdisk: "vendor_kernel_ramdisk"}
	// The modules.dep of the boot ramdisk has the paths that the builder writes for vendor_ramdisk,
	// as core/Makefile writes them for both ramdisks. AOSP's prebuilt_kernel_modules passes other.
	ramdiskPartition = kernelModulePartition{name: "ramdisk",
		placement:  placementProperties{Ramdisk: proptools.BoolPtr(true)},
		installsIn: android.ModuleContext.InstallInRamdisk, builderPartition: "vendor_ramdisk", ramdisk: "ramdisk"}
	vendorPartition = kernelModulePartition{name: "vendor",
		placement:  placementProperties{Vendor: proptools.BoolPtr(true)},
		installsIn: android.ModuleContext.InstallInVendor, builderPartition: "other"}
)

// kernelModulePartitions are the partitions in the order in which a module is matched against them.
var kernelModulePartitions = []kernelModulePartition{systemDlkmPartition, vendorDlkmPartition, odmDlkmPartition,
	vendorRamdiskPartition, vendorKernelRamdiskPartition, ramdiskPartition, vendorPartition}

// partitionOf gives the partition that the module installs into. A module of none of
// kernelModulePartitions installs into lib/modules of its partition, and AOSP's builder takes it as
// other.
func partitionOf(ctx android.ModuleContext) kernelModulePartition {
	for _, p := range kernelModulePartitions {
		if p.installsIn(ctx) {
			return p
		}
	}
	return kernelModulePartition{builderPartition: "other"}
}

// leavesModuleDir reports a path property value that is no module reference and that leads out of
// the module's directory, which Soong reports as an error for a source path.
func leavesModuleDir(value string) bool {
	if module, _ := android.SrcIsModuleWithTag(value); module != "" {
		return false
	}
	p := path.Clean(value)
	return p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p)
}

// pathProperty is a property whose value is a source path, with that value.
type pathProperty struct {
	name  string
	value *string
}

// pathProperties gives every property tagged android:"path", in the order of the struct.
func (p *kernelPartitionModulesProperties) pathProperties() []pathProperty {
	v := reflect.ValueOf(p).Elem()
	var props []pathProperty
	for _, index := range proptools.PropertyIndexesWithTag(p, "android", "path") {
		var names []string
		for i := range index {
			names = append(names, proptools.PropertyNameForField(v.Type().FieldByIndex(index[:i+1]).Name))
		}
		props = append(props, pathProperty{strings.Join(names, "."), v.FieldByIndex(index).Interface().(*string)})
	}
	return props
}

// installDir is the directory prebuilt_kernel_modules installs into: lib/modules of the partition,
// joined with kernel_version when that is set.
func (m *kernelPartitionModules) installDir(ctx android.ModuleContext, partition kernelModulePartition) android.InstallPath {
	dir := android.PathForModuleInstall(ctx, "lib", "modules")
	if partition.ramdisk != "" {
		dir = android.PathForModuleInPartitionInstall(ctx, partition.ramdisk, "lib", "modules")
	}
	if v := proptools.String(m.properties.Kernel_version); v != "" {
		dir = dir.Join(ctx, v)
	}
	return dir
}

func (m *kernelPartitionModules) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	p := &m.properties
	partition := partitionOf(ctx)
	if p.Zip.Src == nil {
		ctx.PropertyErrorf("zip.src", "is required. Set it to the .modules output of the kernel_build module, such as \":<kernel module>{.modules}\".")
	}
	if p.System_map == nil {
		ctx.PropertyErrorf("system_map", "is required: every installed module is checked against the kernel's System.map. "+
			"Set it to the .system_map output of the kernel_build module, such as \":<kernel module>{.system_map}\".")
	}
	if p.Recovery_load_list != nil && !m.InstallInVendorRamdisk() {
		ctx.PropertyErrorf("recovery_load_list", "is only for a module with vendor_ramdisk: true. "+
			"Remove recovery_load_list, or set vendor_ramdisk: true.")
	}
	installAll := proptools.Bool(p.Zip.Install_all)
	if !installAll && p.Load_list == nil && p.Recovery_load_list == nil && p.Install_list == nil {
		ctx.PropertyErrorf("zip.install_all", "none of zip.install_all, load_list, recovery_load_list and install_list is set, so the module would install nothing. "+
			"Set load_list, or set zip.install_all: true to install every module.")
	}
	for _, prop := range p.pathProperties() {
		if prop.value != nil && leavesModuleDir(*prop.value) {
			ctx.PropertyErrorf(prop.name, "%q is outside the directory of this Android.bp. "+
				"Move the file into the directory of this Android.bp, or refer to a module that outputs the file with \":<module>\".",
				*prop.value)
		}
	}
	if ctx.Failed() {
		return
	}

	zipSrc := android.PathForModuleSrc(ctx, *p.Zip.Src)
	systemMap := android.PathForModuleSrc(ctx, *p.System_map)
	loadList := android.OptionalPathForModuleSrc(ctx, p.Load_list)
	recoveryList := android.OptionalPathForModuleSrc(ctx, p.Recovery_load_list)
	installList := android.OptionalPathForModuleSrc(ctx, p.Install_list)
	blocklist := android.OptionalPathForModuleSrc(ctx, p.Blocklist_file)

	sboxDir := android.PathForModuleOut(ctx, "sbox")
	selected := sboxDir.Join(ctx, "selected.zip")
	builderLoad := sboxDir.Join(ctx, "builder", "modules.load")
	builderZip := sboxDir.Join(ctx, "builder", "installs.zip")
	installsZip := sboxDir.Join(ctx, "installs.zip")
	primaryName := "modules.load"
	if !loadList.Valid() {
		primaryName = "modules.dep"
	}
	primary := sboxDir.Join(ctx, primaryName)
	propsFile := android.PathForModuleOut(ctx, "props.json")

	rule := android.NewRuleBuilder(pctx, ctx).
		SandboxDisabled().
		Sbox(sboxDir, android.PathForModuleOut(ctx, "sbox.manifest"))

	// 1. The installed set, checked against the load lists and the kernel's System.map.
	sel := rule.Command()
	sel.BuiltTool("kernel_partition_modules_builder").Text("select").
		FlagWithInput("-zip ", zipSrc).
		FlagWithInput("-system-map ", systemMap).
		Flag("-depmod").BuiltTool("depmod").
		ImplicitTool(ctx.Config().HostCcSharedLibPath(ctx, "libc++")).
		FlagWithArg("-temp ", sel.PathForOutput(sboxDir.Join(ctx, "select"))).
		FlagWithOutput("-o ", selected)
	if loadList.Valid() {
		sel.FlagWithInput("-load-list ", loadList.Path()).
			FlagWithArg("-load-list-name ", proptools.ShellEscape(m.names.of(propLoadList)))
	}
	if recoveryList.Valid() {
		sel.FlagWithInput("-recovery-load-list ", recoveryList.Path()).
			FlagWithArg("-recovery-load-list-name ", proptools.ShellEscape(m.names.of(propRecoveryLoadList)))
	}
	if installList.Valid() {
		sel.FlagWithInput("-install-list ", installList.Path()).
			FlagWithArg("-install-list-name ", proptools.ShellEscape(m.names.of(propInstallList)))
	}
	if installAll {
		sel.Flag("-install-all")
	}
	if proptools.Bool(p.Zip.Allow_empty) {
		sel.Flag("-allow-empty")
	}

	// 2. AOSP's builder, unchanged, with the flags, tools and inputs prebuilt_kernel_modules
	// gives it: depmod for the partition, the blocklist, the stripping and the install zip.
	cmd := rule.Command()
	props := common.PrebuiltKernelModulesPropertiesJSON{
		Kernel_version: p.Kernel_version,
		Zip: common.ZipProperties{
			Src:       proptools.StringPtr(cmd.PathForInputFromFile(selected)),
			Load_file: proptools.StringPtr("modules.install"),
		},
	}
	if blocklist.Valid() {
		props.Blocklist_file = proptools.StringPtr(cmd.PathForInputFromFile(blocklist.Path()))
		cmd.Implicit(blocklist.Path())
	}
	android.WriteFileRule(ctx, propsFile, props.ToJSON())
	cmd.BuiltTool("kernel_modules_builder").
		Flag("--soong_zip").BuiltTool("soong_zip").
		Flag("--zipsync").BuiltTool("zipsync").
		Flag("--merge_zips").BuiltTool("merge_zips").
		Flag("--depmod").BuiltTool("depmod").
		Flag("--llvm-strip").Input(config.ClangPath(ctx, "bin/llvm-strip")).
		Implicit(config.ClangPath(ctx, "lib/x86_64-unknown-linux-gnu/libc++.so")).
		Implicit(config.ClangPath(ctx, "bin/llvm-objcopy")).
		ImplicitTool(ctx.Config().HostCcSharedLibPath(ctx, "libc++")).
		Input(propsFile).
		Text(partition.builderPartition).
		Text(cmd.PathForOutput(sboxDir.Join(ctx, "temp"))).
		Output(builderLoad).
		Output(builderZip)

	// 3. modules.load and modules.load.recovery from their lists, or modules.dep as the primary
	// file without a load list, and the final install zip.
	fin := rule.Command().BuiltTool("kernel_partition_modules_builder").Text("finish").
		FlagWithInput("-installs ", builderZip).
		FlagWithOutput("-o ", installsZip).
		FlagWithOutput("-primary ", primary)
	if loadList.Valid() {
		fin.FlagWithInput("-load-list ", loadList.Path())
	}
	if recoveryList.Valid() {
		fin.FlagWithInput("-recovery-load-list ", recoveryList.Path())
	}
	rule.Build("kernel_partition_modules", "kernel modules of "+ctx.ModuleName())

	ctx.InstallFileWithExtraFilesZipReplacingDir(m.installDir(ctx, partition), primaryName, primary, installsZip)
	m.primary = primary

	m.fullInstall = ctx.RequiresFullInstall()
}

// PrepareAndroidMKProviderInfo gives the module a Make entry when its install was a full install,
// so that a PRODUCT_PACKAGES name reaches its installed file, and the vendor_dlkm and vendor ramdisk
// file lists take its install zip.
func (m *kernelPartitionModules) PrepareAndroidMKProviderInfo(config android.Config) *android.AndroidMkProviderInfo {
	if !m.fullInstall || m.primary == nil {
		return nil
	}
	return &android.AndroidMkProviderInfo{
		PrimaryInfo: android.AndroidMkInfo{
			Class:      "ETC",
			OutputFile: android.OptionalPathForPath(m.primary),
		},
	}
}

var _ android.AndroidMkProviderInfoProducer = (*kernelPartitionModules)(nil)
