// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"android/soong/android"

	"github.com/google/blueprint"
)

type kernelUapiHeaderDirsProperties struct {
	// The kernel_uapi_headers module whose installed headers are exported.
	Headers *string

	// Folders below usr of the installed headers, exported as include directories in this order.
	Dirs []string
}

type kernelUapiHeaderDirs struct {
	android.ModuleBase

	properties kernelUapiHeaderDirsProperties

	headerDirs android.Paths
	stamp      android.Path
}

// KernelUapiHeaderDirsFactory makes a kernel_uapi_header_dirs module: it exports chosen folders
// below usr of the headers installed by a kernel_uapi_headers module as generated headers, without
// installing the headers again.
func KernelUapiHeaderDirsFactory() android.Module {
	module := &kernelUapiHeaderDirs{}
	module.AddProperties(&module.properties)
	android.InitAndroidModule(module)
	return module
}

type uapiHeadersDepTag struct{ blueprint.BaseDependencyTag }

var uapiHeadersTag = uapiHeadersDepTag{}

func (d *kernelUapiHeaderDirs) DepsMutator(ctx android.BottomUpMutatorContext) {
	name := d.properties.Headers
	if name == nil {
		return
	}
	// Checked here, so the message names the property and not the missing dependency.
	if !ctx.OtherModuleExists(*name) {
		ctx.PropertyErrorf("headers", "%q leads to no module. Set it to the name of a kernel_uapi_headers module.", *name)
		return
	}
	// A module of another type, such as kernel_build, has variants that this module does not have,
	// and a dependency on it would fail with the missing variant error of Soong.
	if !ctx.OtherModuleDependencyVariantExists(nil, *name) {
		ctx.PropertyErrorf("headers", "%q is not a kernel_uapi_headers module. Set it to the name of a kernel_uapi_headers module.", *name)
		return
	}
	ctx.AddDependency(ctx.Module(), uapiHeadersTag, *name)
}

func (d *kernelUapiHeaderDirs) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	p := &d.properties
	if p.Headers == nil {
		ctx.PropertyErrorf("headers", "is required. Set it to the name of a kernel_uapi_headers module.")
	}
	if len(p.Dirs) == 0 {
		ctx.PropertyErrorf("dirs", "is empty. List the folders below usr to export.")
	}
	checkPaths(p.Dirs, "dirs", func(format string, args ...any) { ctx.PropertyErrorf("dirs", format, args...) }, nil)

	var info KernelUapiHeadersInfo
	var found bool
	ctx.VisitDirectDepsProxyWithTag(uapiHeadersTag, func(m android.ModuleProxy) {
		if info, found = android.OtherModuleProvider(ctx, m, KernelUapiHeadersInfoProvider); !found {
			ctx.PropertyErrorf("headers", "%q is a %s module, not a kernel_uapi_headers module. Set it to the name of a kernel_uapi_headers module.",
				ctx.OtherModuleName(m), ctx.OtherModuleType(m))
		}
	})
	if !found {
		return
	}
	for _, dir := range p.Dirs {
		d.headerDirs = append(d.headerDirs, info.Usr.Join(ctx, dir))
	}
	d.stamp = info.Stamp
	registerGoalPhonies(ctx, d.ExportedToMake(), d.stampDeps())
}

// stampDeps returns the stamp of the header module, or nothing when the module has no header module.
func (d *kernelUapiHeaderDirs) stampDeps() android.Paths {
	if d.stamp == nil {
		return nil
	}
	return android.Paths{d.stamp}
}

// The methods of android.SourceFileGenerator and android.GeneratedTriggerDepsProducer. The stamp of
// the kernel_uapi_headers module is the dependency of every user of the exported folders.

func (d *kernelUapiHeaderDirs) GeneratedSourceFiles() android.Paths { return nil }

func (d *kernelUapiHeaderDirs) GeneratedHeaderDirs() android.Paths { return d.headerDirs }

func (d *kernelUapiHeaderDirs) GeneratedDeps() android.Paths { return d.stampDeps() }

func (d *kernelUapiHeaderDirs) GeneratedTriggerDeps() android.Paths { return d.stampDeps() }

var _ android.SourceFileGenerator = (*kernelUapiHeaderDirs)(nil)
var _ android.GeneratedTriggerDepsProducer = (*kernelUapiHeaderDirs)(nil)
