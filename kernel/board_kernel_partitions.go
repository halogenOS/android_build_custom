// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"strconv"
	"strings"

	"android/soong/android"

	"github.com/google/blueprint/proptools"
)

// partitionLists are the kernel modules of one partition as the board lists them, with the board
// variables of the lists as the names of the properties, and whether the partition module installs
// every module of the kernel.
type partitionLists struct {
	partition  kernelModulePartition
	installAll bool
	lists      kernelModuleListsProperties
	names      settingNames
}

// boardPartitionLists gives the partitions that get a kernel_partition_modules module. The modules
// of the vendor side go to vendor_dlkm when the board builds that image, otherwise to vendor, and
// that module installs every module that the kernel builds. The ramdisk module goes to the vendor
// ramdisk, or to the boot ramdisk of a board without a vendor_boot image, as core/Makefile places
// the vendor ramdisk modules there. It installs the modules of BOOT_KERNEL_MODULES together with
// those of its load lists, and it exists when the namespace has any list of the ramdisk.
func boardPartitionLists(ctx android.LoadHookContext, vars boardVars) []partitionLists {
	partitionVars := ctx.Config().ProductVariables().PartitionVarsForSoongMigrationOnlyDoNotUse
	vendor := partitionLists{
		partition:  vendorPartition,
		installAll: true,
		lists: kernelModuleListsProperties{
			Load:      vars.words(boardVendorKernelModulesLoad),
			Blocklist: vars.optional(boardVendorKernelModulesBlocklistFile),
		},
		names: settingNames{
			propLoadList:  boardVendorKernelModulesLoad.name,
			propBlocklist: boardVendorKernelModulesBlocklistFile.name,
		},
	}
	if partitionVars.BuildingVendorDlkmImage {
		vendor.partition = vendorDlkmPartition
	}
	ramdisk := partitionLists{
		partition: vendorRamdiskPartition,
		lists: kernelModuleListsProperties{
			Load:          vars.words(boardVendorRamdiskKernelModulesLoad),
			Recovery_load: vars.words(boardVendorRamdiskRecoveryKernelModulesLoad),
			Install:       vars.words(bootKernelModules),
			Blocklist:     vars.optional(boardVendorRamdiskKernelModulesBlocklistFile),
		},
		names: settingNames{
			propLoadList:         boardVendorRamdiskKernelModulesLoad.name,
			propRecoveryLoadList: boardVendorRamdiskRecoveryKernelModulesLoad.name,
			propInstallList:      bootKernelModules.name,
			propBlocklist:        boardVendorRamdiskKernelModulesBlocklistFile.name,
		},
	}
	if !ramdisk.lists.given() {
		return []partitionLists{vendor}
	}
	if !buildsVendorBootImage(partitionVars) {
		if ramdisk.lists.Recovery_load != nil {
			ctx.ModuleErrorf("%s is set, and the board builds no vendor_boot image, the only image with a modules.load.recovery. "+
				"Remove %s, and add the modules that recovery loads to %s.",
				boardVendorRamdiskRecoveryKernelModulesLoad, boardVendorRamdiskRecoveryKernelModulesLoad, boardVendorRamdiskKernelModulesLoad)
			return []partitionLists{vendor}
		}
		ramdisk.partition = ramdiskPartition
	}
	return []partitionLists{vendor, ramdisk}
}

// buildsVendorBootImage reports whether the board builds a vendor_boot image, by the condition under
// which core/board_config.mk sets BUILDING_VENDOR_BOOT_IMAGE: a boot header version of 3 or higher,
// and PRODUCT_BUILD_VENDOR_BOOT_IMAGE empty or true.
func buildsVendorBootImage(v android.PartitionVariables) bool {
	version, err := strconv.Atoi(v.BoardBootHeaderVersion)
	return err == nil && version >= 3 && (v.ProductBuildVendorBootImage == "" || v.ProductBuildVendorBootImage == "true")
}

func (l partitionLists) partitionModule() string { return "board_kernel_modules_" + l.partition.name }

func (l partitionLists) listsModule() string { return "board_kernel_module_lists_" + l.partition.name }

// partitionModuleProperties are the properties of the partition module, which reads the modules
// and System.map of the created kernel and the lists of the lists module.
func (l partitionLists) partitionModuleProperties() *kernelPartitionModulesProperties {
	p := &kernelPartitionModulesProperties{System_map: outputRef(boardKernelModule, tagSystemMap)}
	p.Zip.Src = outputRef(boardKernelModule, tagModules)
	if l.installAll {
		// The kernel configuration of a board may enable no module.
		p.Zip.Install_all = proptools.BoolPtr(true)
		p.Zip.Allow_empty = proptools.BoolPtr(true)
	}
	if l.lists.Load != nil {
		p.Load_list = outputRef(l.listsModule(), tagLoadList)
	}
	if l.lists.Recovery_load != nil {
		p.Recovery_load_list = outputRef(l.listsModule(), tagRecoveryLoadList)
	}
	if l.lists.Install != nil {
		p.Install_list = outputRef(l.listsModule(), tagInstallList)
	}
	if l.lists.Blocklist != nil {
		p.Blocklist_file = outputRef(l.listsModule(), tagBlocklist)
	}
	return p
}

// outputRef is the reference to the output of a module with a tag.
func outputRef(module, tag string) *string {
	return proptools.StringPtr(":" + module + "{" + tag + "}")
}

// The tags of the outputs of a kernel module lists module.
const (
	tagLoadList         = ".load"
	tagRecoveryLoadList = ".recovery_load"
	tagInstallList      = ".install"
	tagBlocklist        = ".blocklist"
)

// propBlocklist is the property of a kernel module lists module that its message refers to.
var propBlocklist = kernelProperty{"blocklist"}

// kernelModuleListsProperties are the lists of one partition. A list that the board does not give
// is nil.
type kernelModuleListsProperties struct {
	// The module names of modules.load, in their order.
	Load []string
	// The module names of modules.load.recovery, in their order.
	Recovery_load []string
	// The names of the further modules to install, which are not written to modules.load.
	Install []string
	// The modules.blocklist, relative to the top of the source tree.
	Blocklist *string
}

// given reports whether the board gives any list of the partition.
func (p *kernelModuleListsProperties) given() bool {
	return p.Load != nil || p.Recovery_load != nil || p.Install != nil || p.Blocklist != nil
}

// kernelModuleLists writes the load lists and the install list of one partition, one word per line,
// and sets them and the blocklist as its tagged outputs. A path property of kernel_partition_modules
// cannot point outside the directory of its Android.bp, so the partition module reads the lists as
// outputs of this module. The adapter cannot have these outputs itself: it requires the partition
// module, which would then depend on the adapter in turn.
type kernelModuleLists struct {
	android.ModuleBase

	properties kernelModuleListsProperties

	// names are the names that the message of the module uses for its properties.
	names settingNames
}

// kernelModuleListsFactory gives the factory of kernel module lists modules whose message uses the
// names. Soong makes each variant of a module with its factory and copies only the properties, so
// the names have to come from the factory.
func kernelModuleListsFactory(names settingNames) android.ModuleFactory {
	return func() android.Module {
		m := &kernelModuleLists{names: names}
		m.AddProperties(&m.properties)
		android.InitAndroidArchModule(m, android.DeviceSupported, android.MultilibCommon)
		return m
	}
}

func (m *kernelModuleLists) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	writeList := func(file string, list []string, tag string) {
		if list == nil {
			return
		}
		out := android.PathForModuleOut(ctx, file)
		android.WriteFileRule(ctx, out, strings.Join(list, "\n"))
		ctx.SetOutputFiles(android.Paths{out}, tag)
	}
	writeList("modules.load", m.properties.Load, tagLoadList)
	writeList("modules.load.recovery", m.properties.Recovery_load, tagRecoveryLoadList)
	writeList("install.list", m.properties.Install, tagInstallList)
	if b := m.properties.Blocklist; b != nil {
		blocklist := android.ExistentPathForSource(ctx, *b)
		if !blocklist.Valid() {
			name := m.names.of(propBlocklist)
			ctx.ModuleErrorf("%s is set to %s, which does not exist. Add the file to the device tree, or correct %s.", name, *b, name)
			return
		}
		ctx.SetOutputFiles(android.Paths{blocklist.Path()}, tagBlocklist)
	}
}
