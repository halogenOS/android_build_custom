// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"strings"

	"android/soong/android"
	"android/soong/phony"

	"github.com/google/blueprint/proptools"
)

// boardKernelNamespace is the Soong config namespace that BoardConfigKernel.mk of the product
// layer fills with the LineageOS-style kernel variables of a board, each under its own name.
const boardKernelNamespace = "board_kernel"

// boardKernelModule is the name of the created kernel_build module. BoardConfigKernel.mk sets
// BOARD_KERNEL_BUILD_MODULE to it when it fills the namespace.
const boardKernelModule = "board_kernel"

const kernelLicense = "linux_kernel_license"

// boardVar is a board variable that BoardConfigKernel.mk passes into the namespace.
type boardVar struct{ name string }

func (v boardVar) String() string { return v.name }

// passedVariablesKey is the key of the namespace under which BoardConfigKernel.mk lists the names of
// the variables that it passes.
const passedVariablesKey = "variables"

// boardVarNames are the names of every board variable that the adapter reads.
var boardVarNames []string

func newBoardVar(name string) boardVar {
	boardVarNames = append(boardVarNames, name)
	return boardVar{name}
}

// The board variables in the namespace.
var (
	targetKernelSource                           = newBoardVar("TARGET_KERNEL_SOURCE")
	targetKernelConfig                           = newBoardVar("TARGET_KERNEL_CONFIG")
	targetKernelArch                             = newBoardVar("TARGET_KERNEL_ARCH")
	boardKernelImageName                         = newBoardVar("BOARD_KERNEL_IMAGE_NAME")
	targetKernelClangPath                        = newBoardVar("TARGET_KERNEL_CLANG_PATH")
	targetKernelClangVersion                     = newBoardVar("TARGET_KERNEL_CLANG_VERSION")
	targetKernelNoGcc                            = newBoardVar("TARGET_KERNEL_NO_GCC")
	targetKernelExtModuleRoot                    = newBoardVar("TARGET_KERNEL_EXT_MODULE_ROOT")
	targetKernelExtModules                       = newBoardVar("TARGET_KERNEL_EXT_MODULES")
	targetModuleAliases                          = newBoardVar("TARGET_MODULE_ALIASES")
	boardUsesQcomMergeDtbsScript                 = newBoardVar("BOARD_USES_QCOM_MERGE_DTBS_SCRIPT")
	targetNeedsDtboimage                         = newBoardVar("TARGET_NEEDS_DTBOIMAGE")
	boardKernelSeparatedDtbo                     = newBoardVar("BOARD_KERNEL_SEPARATED_DTBO")
	boardKernelPagesize                          = newBoardVar("BOARD_KERNEL_PAGESIZE")
	boardVendorKernelModulesLoad                 = newBoardVar("BOARD_VENDOR_KERNEL_MODULES_LOAD")
	boardVendorKernelModulesBlocklistFile        = newBoardVar("BOARD_VENDOR_KERNEL_MODULES_BLOCKLIST_FILE")
	boardVendorRamdiskKernelModulesLoad          = newBoardVar("BOARD_VENDOR_RAMDISK_KERNEL_MODULES_LOAD")
	boardVendorRamdiskRecoveryKernelModulesLoad  = newBoardVar("BOARD_VENDOR_RAMDISK_RECOVERY_KERNEL_MODULES_LOAD")
	boardVendorRamdiskKernelModulesBlocklistFile = newBoardVar("BOARD_VENDOR_RAMDISK_KERNEL_MODULES_BLOCKLIST_FILE")
	bootKernelModules                            = newBoardVar("BOOT_KERNEL_MODULES")
)

// boardVars are the values of the namespace.
type boardVars struct{ config android.VendorConfig }

func boardVarsOf(cfg android.Config) boardVars {
	return boardVars{cfg.VendorConfig(boardKernelNamespace)}
}

func (b boardVars) isSet(v boardVar) bool { return b.config.IsSet(v.name) }

func (b boardVars) value(v boardVar) string { return b.config.String(v.name) }

// optional gives the value, or nil when the board does not set the variable.
func (b boardVars) optional(v boardVar) *string {
	if !b.isSet(v) {
		return nil
	}
	return proptools.StringPtr(b.value(v))
}

// words gives the words of the value, or nil when the board does not set the variable or its value
// has no word.
func (b boardVars) words(v boardVar) []string {
	if w := strings.Fields(b.value(v)); len(w) > 0 {
		return w
	}
	return nil
}

// isTrue reports a variable that the board sets to true. Any other value counts as false.
func (b boardVars) isTrue(v boardVar) bool { return b.value(v) == "true" }

// createdModuleProperties are the properties of the module base that the created modules set.
type createdModuleProperties struct {
	Name     *string
	Licenses []string
}

// BoardKernelAdapterFactory makes the board_kernel_adapter module, a phony package. When
// BOARD_KERNEL_BUILD_MODULE is set to the created kernel module, its load hook creates that
// kernel_build module from the namespace, and the partition modules, which the package requires.
// For any other board it creates nothing and requires nothing.
func BoardKernelAdapterFactory() android.Module {
	m := phony.PhonyFactory()
	android.AddLoadHook(m, createBoardKernelModules)
	return m
}

// createBoardKernelModules creates the kernel_build module and the partition modules with their
// lists modules from the namespace, and makes the adapter require the partition modules.
func createBoardKernelModules(ctx android.LoadHookContext) {
	if !boardKernelModuleIs(ctx.Config(), ctx.Namespace().Path, boardKernelModule) {
		return
	}
	vars := boardVarsOf(ctx.Config())
	checkPassedVariables(ctx, vars)
	kernel := boardKernelBuildProperties(ctx, vars)
	partitions := boardPartitionLists(ctx, vars)

	// A created module takes the properties of the module base of the creating module first, so
	// the adapter gets its required modules only once the modules exist.
	ctx.CreateModule(kernelBuildFactoryWithNames(kernel.names, kernel.sourceDirSteps), &createdModuleProperties{
		Name:     proptools.StringPtr(boardKernelModule),
		Licenses: []string{kernelLicense},
	}, kernel.properties)
	var required []string
	for _, l := range partitions {
		ctx.CreateModule(kernelModuleListsFactory(l.names), &createdModuleProperties{Name: proptools.StringPtr(l.listsModule())},
			&l.lists)
		placement := l.partition.placement
		ctx.CreateModule(kernelPartitionModulesFactoryWithNames(l.names), &createdModuleProperties{
			Name:     proptools.StringPtr(l.partitionModule()),
			Licenses: []string{kernelLicense},
		}, &placement, l.partitionModuleProperties())
		required = append(required, l.partitionModule())
	}
	ctx.AppendProperties(&struct{ Required []string }{required})
}

// checkPassedVariables reports each variable that BoardConfigKernel.mk passes and the adapter does
// not read, and each one that the adapter reads and BoardConfigKernel.mk does not pass, by the list
// of names that BoardConfigKernel.mk passes with them.
func checkPassedVariables(ctx android.LoadHookContext, vars boardVars) {
	passed := strings.Fields(vars.config.String(passedVariablesKey))
	differ, notRead, notPassed := android.ListSetDifference(passed, boardVarNames)
	if !differ {
		return
	}
	var parts []string
	if len(notRead) > 0 {
		parts = append(parts, "BoardConfigKernel.mk passes "+strings.Join(notRead, ", ")+", which board_kernel_adapter does not read")
	}
	if len(notPassed) > 0 {
		parts = append(parts, "board_kernel_adapter reads "+strings.Join(notPassed, ", ")+", which BoardConfigKernel.mk does not pass")
	}
	ctx.ModuleErrorf("%s. Add each of these variables to the list that lacks it: the variables that BoardConfigKernel.mk "+
		"passes, or the board variables of board_kernel_adapter.", strings.Join(parts, ", and "))
}
