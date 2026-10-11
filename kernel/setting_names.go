// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"fmt"

	"android/soong/android"
)

// kernelProperty is a property of a kernel module type that a message can refer to.
type kernelProperty struct{ name string }

var (
	propKernelDir           = kernelProperty{"kernel_dir"}
	propExtraSourceDirs     = kernelProperty{"extra_source_dirs"}
	propKernelArch          = kernelProperty{"kernel_arch"}
	propImage               = kernelProperty{"image"}
	propDefconfig           = kernelProperty{"defconfig"}
	propConfigFragments     = kernelProperty{"config_fragments"}
	propClangDir            = kernelProperty{"clang_dir"}
	propExternalModules     = kernelProperty{"external_modules"}
	propExternalModulesRoot = kernelProperty{"external_modules.root"}
	propExternalModulesDirs = kernelProperty{"external_modules.dirs"}
	propModuleRenames       = kernelProperty{"module_renames"}
	propDtImages            = kernelProperty{"dt_images"}
	propDtImagesMerge       = kernelProperty{"dt_images.merge"}
	propDtboPageSize        = kernelProperty{"dt_images.dtbo_page_size"}
)

// settingNames maps a property to the name that the messages of a module use for it, such as the
// board variable that the code creating the module took the value from. A property without an
// entry goes by its own name.
type settingNames map[kernelProperty]string

func (n settingNames) of(p kernelProperty) string {
	if name, ok := n[p]; ok {
		return name
	}
	return p.name
}

// name gives the name that the module's messages use for a property.
func (k *kernelBuild) name(p kernelProperty) string { return k.names.of(p) }

// errorf reports a problem with a property: as an error of the property when the property goes by
// its own name, and as an error of the module that starts with the other name otherwise.
func (k *kernelBuild) errorf(ctx android.BaseModuleContext, p kernelProperty, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if name, ok := k.names[p]; ok {
		ctx.ModuleErrorf("%s: %s", name, msg)
	} else {
		ctx.PropertyErrorf(p.name, "%s", msg)
	}
}

// sourceDirSteps are the next steps for a directory outside the source directories of a kernel:
// add, for files that the kernel build reads from such a directory, and remove, for an entry of
// extra_source_dirs that kernel_build reports. The code that creates a module from settings of
// another form sets them where the name of extra_source_dirs does not make a sentence of its own.
// Steps that are not set are formed with that name.
type sourceDirSteps struct{ add, remove string }

// addSourceDirStep gives the next step for files that the kernel build reads outside its source
// directories.
func (k *kernelBuild) addSourceDirStep() string {
	if k.sourceDirSteps.add != "" {
		return k.sourceDirSteps.add
	}
	return fmt.Sprintf("Add the directory that contains these files to %s.", k.name(propExtraSourceDirs))
}

// removeSourceDirStep gives the next step for an entry of extra_source_dirs that the message is
// about.
func (k *kernelBuild) removeSourceDirStep() string {
	if k.sourceDirSteps.remove != "" {
		return k.sourceDirSteps.remove
	}
	return fmt.Sprintf("Remove it from %s.", k.name(propExtraSourceDirs))
}
